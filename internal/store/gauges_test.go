package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/store"
	"github.com/amlaban65/uptime-monitor/internal/testdb"
)

func TestGauges(t *testing.T) {
	db := testdb.New(t)
	s := store.New(db, store.WithAlertChannels("slack"))
	ctx := context.Background()
	down := insertMonitor(t, db, "down", 1, 1)
	up := insertMonitor(t, db, "up", 1, 1)
	insertMonitor(t, db, "fresh", 1, 1)

	if err := s.RecordResult(ctx, down, monitor.Result{CheckedAt: time.Now(), Error: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult(ctx, up, monitor.Result{CheckedAt: time.Now(), OK: true}); err != nil {
		t.Fatal(err)
	}
	g, err := s.Gauges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g.MonitorStatus["down"] != "down" || g.MonitorStatus["up"] != "up" || g.MonitorStatus["fresh"] != "unknown" {
		t.Fatalf("statuses %v", g.MonitorStatus)
	}
	if g.OpenIncidents != 1 || g.PendingAlerts != 1 || g.FailedAlerts != 0 {
		t.Fatalf("gauges %+v", g)
	}
}
