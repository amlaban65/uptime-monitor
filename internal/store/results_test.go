package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/store"
	"github.com/amlaban65/uptime-monitor/internal/testdb"
)

// insertMonitor adds a monitor with raw SQL so these tests do not depend on
// the CRUD code.
func insertMonitor(t *testing.T, db *sql.DB, name string, failures, recoveries int) monitor.Monitor {
	t.Helper()
	m := monitor.Monitor{Spec: monitor.Spec{
		Name: name, Kind: monitor.KindTCP, Target: "localhost:1",
		Interval:         monitor.Duration(time.Minute),
		Timeout:          monitor.Duration(10 * time.Second),
		FailureThreshold: failures, RecoveryThreshold: recoveries,
	}}
	err := db.QueryRowContext(context.Background(), `
		INSERT INTO monitors (name, kind, target, interval_ms, timeout_ms, failure_threshold, recovery_threshold)
		VALUES ($1, 'tcp', $2, 60000, 10000, $3, $4) RETURNING id`,
		name, m.Target, failures, recoveries).Scan(&m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRecordResultOpensAndResolvesIncident(t *testing.T) {
	db := testdb.New(t)
	s := store.New(db)
	ctx := context.Background()
	m := insertMonitor(t, db, "api", 2, 2)

	base := time.Date(2026, 5, 1, 23, 58, 0, 0, time.UTC)
	seq := []bool{true, false, false, false, true, true}
	for i, ok := range seq {
		r := monitor.Result{MonitorID: m.ID, CheckedAt: base.Add(time.Duration(i) * time.Minute), OK: ok, Latency: 15 * time.Millisecond}
		if !ok {
			r.Error = "connection refused"
		}
		if err := s.RecordResult(ctx, m, r); err != nil {
			t.Fatalf("result %d: %v", i, err)
		}
	}

	incidents, err := s.RecentIncidents(ctx, base.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 1 {
		t.Fatalf("got %d incidents, want 1", len(incidents))
	}
	in := incidents[0]
	if !in.StartedAt.Equal(base.Add(time.Minute)) || in.ResolvedAt == nil || !in.ResolvedAt.Equal(base.Add(4*time.Minute)) {
		t.Fatalf("incident window = %v .. %v", in.StartedAt, in.ResolvedAt)
	}
	if in.Cause != "connection refused" || in.MonitorName != "api" {
		t.Fatalf("incident = %+v", in)
	}

	// Results straddle midnight UTC: 2 on May 1, 4 on May 2 (1 failure).
	rows, err := db.QueryContext(ctx, `SELECT day::text, total, ok FROM daily_stats WHERE monitor_id = $1 ORDER BY day`, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var day string
		var total, ok int
		if err := rows.Scan(&day, &total, &ok); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%d/%d", day, ok, total))
	}
	if want := []string{"2026-05-01:1/2", "2026-05-02:2/4"}; !slices.Equal(got, want) {
		t.Fatalf("daily_stats = %v, want %v", got, want)
	}

	var status string
	var successes int
	if err := db.QueryRowContext(ctx, `SELECT status, consecutive_successes FROM monitor_state WHERE monitor_id = $1`, m.ID).Scan(&status, &successes); err != nil {
		t.Fatal(err)
	}
	if status != "up" || successes != 2 {
		t.Fatalf("state = %s/%d", status, successes)
	}
}

func TestConcurrentFailuresOpenOneIncident(t *testing.T) {
	dsn := testdb.MigratedURL(t)
	db := testdb.Open(t, dsn)
	m := insertMonitor(t, db, "flaky", 1, 1)

	// Two "replicas" with separate pools record failures at the same time.
	replicas := []*store.Store{store.New(db), store.New(testdb.Open(t, dsn))}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := monitor.Result{MonitorID: m.ID, CheckedAt: time.Now(), Error: "timeout"}
			errs <- replicas[i%2].RecordResult(context.Background(), m, r)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var open, failures int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM incidents WHERE monitor_id = $1`, m.ID).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT consecutive_failures FROM monitor_state WHERE monitor_id = $1`, m.ID).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if open != 1 || failures != 40 {
		t.Fatalf("incidents = %d, consecutive failures = %d; want 1 and 40", open, failures)
	}
}
