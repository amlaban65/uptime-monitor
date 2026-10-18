package store_test

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/stats"
	"github.com/amlaban65/uptime-monitor/internal/store"
	"github.com/amlaban65/uptime-monitor/internal/testdb"
)

func TestMonitorStatsWindowsAndPercentiles(t *testing.T) {
	db := testdb.New(t)
	s := store.New(db)
	ctx := context.Background()
	m := insertMonitor(t, db, "api", 3, 2)
	insertMonitor(t, db, "never-checked", 3, 2)

	now := time.Now()
	record := func(age time.Duration, ok bool, latency time.Duration) {
		t.Helper()
		if err := s.RecordResult(ctx, m, monitor.Result{MonitorID: m.ID, CheckedAt: now.Add(-age), OK: ok, Latency: latency}); err != nil {
			t.Fatal(err)
		}
	}

	// Last 24h: 200 successes with random latencies and 2 slow failures
	// that must not count as latency samples.
	rng := rand.New(rand.NewPCG(1, 2))
	var samples []time.Duration
	for i := range 200 {
		lat := time.Duration(1000+rng.IntN(300_000)) * time.Microsecond
		samples = append(samples, lat)
		record(time.Duration(i)*time.Minute, true, lat)
	}
	record(5*time.Hour, false, 10*time.Second)
	record(6*time.Hour, false, 10*time.Second)
	// 2-7 days ago: 8 results, 2 failed. 10-30 days ago: 10, all failed.
	// Older than 30 days: ignored.
	for i := range 8 {
		record(48*time.Hour+time.Duration(i)*time.Hour, i >= 2, time.Millisecond)
	}
	for i := range 10 {
		record(10*24*time.Hour+time.Duration(i)*time.Hour, false, time.Millisecond)
	}
	record(40*24*time.Hour, true, time.Millisecond)

	rows, err := s.MonitorStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	got := rows[0]
	if got.Name != "api" {
		t.Fatalf("rows not ordered by name: %+v", rows)
	}
	if got.Total24h != 202 || got.OK24h != 200 || got.Total7d != 210 || got.OK7d != 206 || got.Total30d != 220 || got.OK30d != 206 {
		t.Fatalf("windows = %+v", got)
	}
	// SQL percentile_disc must agree with the reference implementation.
	wantP50, _ := stats.Percentile(samples, 0.5)
	wantP95, _ := stats.Percentile(samples, 0.95)
	if got.P50 == nil || got.P95 == nil || *got.P50 != wantP50 || *got.P95 != wantP95 {
		t.Fatalf("p50=%v p95=%v, want %v %v", got.P50, got.P95, wantP50, wantP95)
	}

	empty := rows[1]
	if empty.Status != "unknown" || empty.Total30d != 0 || empty.P50 != nil {
		t.Fatalf("monitor without results = %+v", empty)
	}

	daily, err := s.DailyCounts(ctx, 90)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, d := range daily[m.ID] {
		total += d.Total
	}
	// Unlike the 30-day window, the 90-day rollup includes the 40-day-old row.
	if total != 221 {
		t.Fatalf("daily total = %d, want 221", total)
	}
}
