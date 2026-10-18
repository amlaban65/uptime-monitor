package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/store"
	"github.com/amlaban65/uptime-monitor/internal/testdb"
)

func TestPrune(t *testing.T) {
	dsn := testdb.MigratedURL(t)
	db := testdb.Open(t, dsn)
	s := store.New(db)
	ctx := context.Background()
	m := insertMonitor(t, db, "api", 3, 2)
	now := time.Now()
	for _, age := range []time.Duration{time.Hour, 10 * 24 * time.Hour, 40 * 24 * time.Hour, 50 * 24 * time.Hour} {
		if err := s.RecordResult(ctx, m, monitor.Result{MonitorID: m.ID, CheckedAt: now.Add(-age), OK: true}); err != nil {
			t.Fatal(err)
		}
	}

	// Another replica holds the lock: this one skips the run.
	other := testdb.Open(t, dsn)
	conn, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(0x75707469_6d650003)); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(ctx, 35*24*time.Hour)
	if err != nil || res.Ran {
		t.Fatalf("expected skip while locked, got %+v err %v", res, err)
	}

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, int64(0x75707469_6d650003)); err != nil {
		t.Fatal(err)
	}
	res, err = s.Prune(ctx, 35*24*time.Hour)
	if err != nil || !res.Ran || res.Results != 2 {
		t.Fatalf("prune = %+v err %v, want 2 rows deleted", res, err)
	}

	var raw, days int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM check_results), (SELECT count(*) FROM daily_stats)`).Scan(&raw, &days); err != nil {
		t.Fatal(err)
	}
	if raw != 2 || days != 4 {
		t.Fatalf("after prune: %d raw results, %d daily rows; want 2 and 4 (rollups kept)", raw, days)
	}
}
