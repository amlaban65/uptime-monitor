package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockKey is an arbitrary constant identifying the migration
// advisory lock. Any 64-bit value works as long as nothing else uses it.
const migrationLockKey int64 = 0x75707469_6d650001

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate applies any pending migrations embedded in the binary. Every
// replica calls it at startup; a session-level advisory lock makes them take
// turns, so the first applies the migrations and the rest find nothing to do.
// Each migration runs in its own transaction together with its bookkeeping
// row, so a failed migration leaves no partial schema behind.
func Migrate(ctx context.Context, db *sql.DB) (applied []string, err error) {
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}

	// Session-level advisory locks belong to a connection, so lock and unlock
	// must use the same one rather than whatever the pool hands out.
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Use a fresh context so the lock is released even if ctx is done.
		_, uerr := conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
		if err == nil && uerr != nil {
			err = fmt.Errorf("release migration lock: %w", uerr)
		}
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer PRIMARY KEY,
		name       text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, err
	}

	done := map[int]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		done[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, m := range migrations {
		if done[m.version] {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return applied, fmt.Errorf("migration %s: %w", m.name, err)
		}
		applied = append(applied, m.name)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
		return err
	}
	return tx.Commit()
}

// loadMigrations reads files named NNNN_description.sql, sorted by version.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil || !strings.HasSuffix(e.Name(), ".sql") {
			return nil, fmt.Errorf("bad migration file name %q", e.Name())
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("duplicate migration version %d: %s and %s", v, prev, e.Name())
		}
		seen[v] = e.Name()
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
