// Package store is the Postgres persistence layer. It uses database/sql with
// the pgx driver and hand-written SQL: the queries are few, and several rely
// on Postgres features (SKIP LOCKED, advisory locks, partial indexes,
// percentile_disc) that are clearer written out than generated.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrReadOnly = errors.New("monitor is managed by the monitors file")
)

// Open connects to Postgres and waits up to wait for it to accept
// connections, which smooths over container start ordering.
func Open(ctx context.Context, url string, wait time.Duration) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxIdleTime(5 * time.Minute)

	deadline := time.Now().Add(wait)
	for {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = db.PingContext(pctx)
		cancel()
		if err == nil {
			return db, nil
		}
		if time.Now().After(deadline) {
			db.Close()
			return nil, fmt.Errorf("database not reachable: %w", err)
		}
		select {
		case <-ctx.Done():
			db.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Store wraps the database handle.
type Store struct {
	db            *sql.DB
	alertChannels []string
}

// Option configures a Store.
type Option func(*Store)

// WithAlertChannels sets the channels (for example "slack", "email") that
// get an outbox row when an incident opens or resolves.
func WithAlertChannels(channels ...string) Option {
	return func(s *Store) { s.alertChannels = channels }
}

func New(db *sql.DB, opts ...Option) *Store {
	s := &Store{db: db}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Ping reports whether the database is reachable, for readiness checks.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// inTx runs fn in a transaction, committing on success.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
