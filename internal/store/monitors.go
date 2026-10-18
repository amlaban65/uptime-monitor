package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
)

const monitorColumns = `id, name, kind, target, method, expected_status, body_contains,
	interval_ms, timeout_ms, failure_threshold, recovery_threshold, enabled, source,
	created_at, updated_at`

// monitorColumnsM is monitorColumns qualified with the alias m, for queries
// where a bare column name would be ambiguous.
const monitorColumnsM = `m.id, m.name, m.kind, m.target, m.method, m.expected_status, m.body_contains,
	m.interval_ms, m.timeout_ms, m.failure_threshold, m.recovery_threshold, m.enabled, m.source,
	m.created_at, m.updated_at`

// fileSyncLockKey serializes monitors-file sync across replicas starting
// at the same time.
const fileSyncLockKey int64 = 0x75707469_6d650002

type rowScanner interface{ Scan(dest ...any) error }

func scanMonitor(row rowScanner) (monitor.Monitor, error) {
	var (
		m                     monitor.Monitor
		kind, source          string
		intervalMS, timeoutMS int64
		enabled               bool
	)
	err := row.Scan(&m.ID, &m.Name, &kind, &m.Target, &m.Method, &m.ExpectedStatus, &m.BodyContains,
		&intervalMS, &timeoutMS, &m.FailureThreshold, &m.RecoveryThreshold, &enabled, &source,
		&m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.Kind = monitor.Kind(kind)
	m.Source = monitor.Source(source)
	m.Interval = monitor.Duration(time.Duration(intervalMS) * time.Millisecond)
	m.Timeout = monitor.Duration(time.Duration(timeoutMS) * time.Millisecond)
	m.Enabled = &enabled
	return m, err
}

func specArgs(s monitor.Spec) []any {
	return []any{s.Name, string(s.Kind), s.Target, s.Method, s.ExpectedStatus, s.BodyContains,
		s.Interval.Std().Milliseconds(), s.Timeout.Std().Milliseconds(),
		s.FailureThreshold, s.RecoveryThreshold, s.IsEnabled()}
}

func (s *Store) ListMonitors(ctx context.Context) ([]monitor.Monitor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+monitorColumns+` FROM monitors ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []monitor.Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetMonitor(ctx context.Context, id int64) (monitor.Monitor, error) {
	return scanMonitor(s.db.QueryRowContext(ctx, `SELECT `+monitorColumns+` FROM monitors WHERE id = $1`, id))
}

// CreateMonitor stores a new API-managed monitor. The spec must already be
// normalized and validated.
func (s *Store) CreateMonitor(ctx context.Context, spec monitor.Spec) (monitor.Monitor, error) {
	m, err := scanMonitor(s.db.QueryRowContext(ctx, `
		INSERT INTO monitors (name, kind, target, method, expected_status, body_contains,
			interval_ms, timeout_ms, failure_threshold, recovery_threshold, enabled, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'api')
		RETURNING `+monitorColumns, specArgs(spec)...))
	return m, mapConstraintError(err)
}

// UpdateMonitor replaces an API-managed monitor's spec and makes it due
// immediately, so the change is visible on the next poll.
func (s *Store) UpdateMonitor(ctx context.Context, id int64, spec monitor.Spec) (monitor.Monitor, error) {
	args := append([]any{id}, specArgs(spec)...)
	m, err := scanMonitor(s.db.QueryRowContext(ctx, `
		UPDATE monitors SET name = $2, kind = $3, target = $4, method = $5, expected_status = $6,
			body_contains = $7, interval_ms = $8, timeout_ms = $9, failure_threshold = $10,
			recovery_threshold = $11, enabled = $12, next_run_at = now(), updated_at = now()
		WHERE id = $1 AND source = 'api'
		RETURNING `+monitorColumns, args...))
	if errors.Is(err, ErrNotFound) {
		return m, s.whyNotWritable(ctx, id)
	}
	return m, mapConstraintError(err)
}

// DeleteMonitor removes an API-managed monitor and, by cascade, its history.
func (s *Store) DeleteMonitor(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM monitors WHERE id = $1 AND source = 'api'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return s.whyNotWritable(ctx, id)
	}
	return nil
}

func (s *Store) whyNotWritable(ctx context.Context, id int64) error {
	var source string
	err := s.db.QueryRowContext(ctx, `SELECT source FROM monitors WHERE id = $1`, id).Scan(&source)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	default:
		return ErrReadOnly
	}
}

// SyncFileMonitors makes the file-managed monitors match specs: new names
// are inserted, existing ones updated, and file monitors no longer listed
// are deleted. API-managed monitors are never touched; a name clash with
// one is an error.
func (s *Store) SyncFileMonitors(ctx context.Context, specs []monitor.Spec) (int, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, fileSyncLockKey); err != nil {
			return err
		}
		names := make([]string, 0, len(specs))
		for _, spec := range specs {
			names = append(names, spec.Name)
			res, err := tx.ExecContext(ctx, `
				INSERT INTO monitors (name, kind, target, method, expected_status, body_contains,
					interval_ms, timeout_ms, failure_threshold, recovery_threshold, enabled, source)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'file')
				ON CONFLICT (name) DO UPDATE SET kind = EXCLUDED.kind, target = EXCLUDED.target,
					method = EXCLUDED.method, expected_status = EXCLUDED.expected_status,
					body_contains = EXCLUDED.body_contains, interval_ms = EXCLUDED.interval_ms,
					timeout_ms = EXCLUDED.timeout_ms, failure_threshold = EXCLUDED.failure_threshold,
					recovery_threshold = EXCLUDED.recovery_threshold, enabled = EXCLUDED.enabled,
					updated_at = now()
				WHERE monitors.source = 'file'`, specArgs(spec)...)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("monitor %q from file: name is already used by an API monitor: %w", spec.Name, ErrConflict)
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM monitors WHERE source = 'file' AND NOT (name = ANY($1))`, names)
		return err
	})
	return len(specs), err
}

// mapConstraintError turns a unique violation into ErrConflict.
func mapConstraintError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: a monitor with that name already exists", ErrConflict)
	}
	return err
}
