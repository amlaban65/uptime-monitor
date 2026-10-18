package status

import (
	"context"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/stats"
	"github.com/amlaban65/uptime-monitor/internal/store"
)

type fakeSource struct {
	calls     int
	rows      []store.MonitorStats
	daily     map[int64][]stats.DayCount
	incidents []store.Incident
}

func (f *fakeSource) MonitorStats(context.Context) ([]store.MonitorStats, error) {
	f.calls++
	return f.rows, nil
}
func (f *fakeSource) DailyCounts(context.Context, int) (map[int64][]stats.DayCount, error) {
	return f.daily, nil
}
func (f *fakeSource) RecentIncidents(context.Context, time.Time, int) ([]store.Incident, error) {
	return f.incidents, nil
}

func TestSnapshot(t *testing.T) {
	now := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	p50, p95 := 12500*time.Microsecond, 80*time.Millisecond
	resolved := now.Add(-time.Hour)
	src := &fakeSource{
		rows: []store.MonitorStats{
			{ID: 1, Name: "api", Status: "up", Total24h: 1000, OK24h: 999, Total7d: 7000, OK7d: 7000, Total30d: 0, P50: &p50, P95: &p95},
			{ID: 2, Name: "db", Status: "down"},
		},
		daily: map[int64][]stats.DayCount{1: {{Day: now, Total: 10, OK: 10}}},
		incidents: []store.Incident{
			{MonitorName: "db", StartedAt: now.Add(-90 * time.Second)},
			{MonitorName: "api", StartedAt: resolved.Add(-5 * time.Minute), ResolvedAt: &resolved},
		},
	}
	svc := NewService(src, "Acme", time.Minute)
	svc.now = func() time.Time { return now }

	snap, err := svc.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Overall != OverallPartial || len(snap.Monitors) != 2 {
		t.Fatalf("overall=%s monitors=%d", snap.Overall, len(snap.Monitors))
	}
	api := snap.Monitors[0]
	if *api.Uptime24h != 99.9 || *api.Uptime7d != 100 || api.Uptime30d != nil {
		t.Fatalf("uptime %v %v %v", *api.Uptime24h, *api.Uptime7d, api.Uptime30d)
	}
	if *api.LatencyP50Ms != 12.5 || *api.LatencyP95Ms != 80 {
		t.Fatalf("latency %v %v", *api.LatencyP50Ms, *api.LatencyP95Ms)
	}
	if len(api.Bars) != 90 || api.Bars[89].Level != stats.LevelGood {
		t.Fatalf("bars: %d, last %+v", len(api.Bars), api.Bars[89])
	}
	if snap.Monitors[1].LatencyP50Ms != nil {
		t.Fatal("no samples should mean no latency")
	}
	if !snap.Incidents[0].Ongoing || snap.Incidents[0].Duration != "1m30s" || snap.Incidents[1].Duration != "5m0s" {
		t.Fatalf("incidents %+v", snap.Incidents)
	}

	// Cached within the TTL, rebuilt after it.
	_, _ = svc.Snapshot(context.Background())
	if src.calls != 1 {
		t.Fatalf("source called %d times within TTL", src.calls)
	}
	now = now.Add(2 * time.Minute)
	_, _ = svc.Snapshot(context.Background())
	if src.calls != 2 {
		t.Fatalf("source called %d times after TTL", src.calls)
	}
}

func TestOverall(t *testing.T) {
	tests := []struct {
		up, down, total int
		want            string
	}{
		{0, 0, 0, OverallUnknown},
		{0, 0, 3, OverallUnknown},
		{3, 0, 3, OverallOperational},
		{2, 0, 3, OverallOperational}, // one still unknown
		{2, 1, 3, OverallPartial},
		{0, 3, 3, OverallMajor},
	}
	for _, tt := range tests {
		if got := overall(tt.up, tt.down, tt.total); got != tt.want {
			t.Errorf("overall(%d,%d,%d) = %s, want %s", tt.up, tt.down, tt.total, got, tt.want)
		}
	}
}
