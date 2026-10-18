// Package testdb gives integration tests an isolated, migrated Postgres
// database. Tests that use it are skipped unless TEST_DATABASE_URL is set,
// so `go test ./...` works without a database.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/store"
)

// URL returns a connection string for a brand-new empty database that is
// dropped when the test ends.
func URL(t testing.TB) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "uptime_test_" + hex.EncodeToString(b)
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// New returns a connection to a fresh, fully migrated database.
func New(t testing.TB) *sql.DB {
	t.Helper()
	db := Open(t, URL(t))
	if _, err := store.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// Open opens an extra connection pool to an existing test database, which is
// how tests simulate separate replicas.
func Open(t testing.TB, dsn string) *sql.DB {
	t.Helper()
	db, err := store.Open(context.Background(), dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
