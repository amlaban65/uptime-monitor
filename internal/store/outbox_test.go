package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/notify"
	"github.com/amlaban65/uptime-monitor/internal/store"
	"github.com/amlaban65/uptime-monitor/internal/testdb"
)

func TestTransitionsEnqueueAlertsOncePerChannel(t *testing.T) {
	db := testdb.New(t)
	s := store.New(db, store.WithAlertChannels("slack", "email"))
	ctx := context.Background()
	m := insertMonitor(t, db, "web", 1, 1)

	now := time.Now()
	for i, ok := range []bool{false, false, true, true} {
		if err := s.RecordResult(ctx, m, monitor.Result{MonitorID: m.ID, CheckedAt: now.Add(time.Duration(i) * time.Second), OK: ok, Error: "boom"}); err != nil {
			t.Fatal(err)
		}
	}

	// Replaying the enqueue (as a retried transaction might) must not duplicate.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO notifications (incident_id, event, channel)
		SELECT incident_id, event, channel FROM notifications
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}

	got, err := s.ClaimDeliveries(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, d := range got {
		counts[string(d.Event.Kind)+"/"+d.Channel]++
		if d.Attempts != 1 || d.Event.MonitorName != "web" {
			t.Fatalf("delivery %+v", d)
		}
		if d.Event.Kind == notify.EventResolved && d.Event.ResolvedAt == nil {
			t.Fatal("resolved event without resolved time")
		}
	}
	want := map[string]int{"opened/slack": 1, "opened/email": 1, "resolved/slack": 1, "resolved/email": 1}
	if len(counts) != len(want) {
		t.Fatalf("deliveries = %v, want %v", counts, want)
	}
	for k, v := range want {
		if counts[k] != v {
			t.Fatalf("deliveries = %v, want %v", counts, want)
		}
	}

	// Leased rows are not handed out again until the lease expires.
	again, err := s.ClaimDeliveries(ctx, 10, time.Minute)
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim returned %d rows, err %v", len(again), err)
	}
}

func TestConcurrentDispatchersClaimEachDeliveryOnce(t *testing.T) {
	dsn := testdb.MigratedURL(t)
	db := testdb.Open(t, dsn)
	ctx := context.Background()
	s := store.New(db, store.WithAlertChannels("slack"))

	for i := range 30 {
		m := insertMonitor(t, db, "svc-"+string(rune('a'+i)), 1, 1)
		if err := s.RecordResult(ctx, m, monitor.Result{MonitorID: m.ID, CheckedAt: time.Now(), Error: "down"}); err != nil {
			t.Fatal(err)
		}
	}

	replicas := []*store.Store{store.New(testdb.Open(t, dsn)), store.New(testdb.Open(t, dsn)), store.New(testdb.Open(t, dsn))}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for _, r := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				ds, err := r.ClaimDeliveries(ctx, 2, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				for _, d := range ds {
					seen[d.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 30 {
		t.Fatalf("claimed %d distinct deliveries, want 30", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("delivery %d claimed %d times", id, n)
		}
	}
}

func TestMarkFailedSchedulesRetry(t *testing.T) {
	db := testdb.New(t)
	s := store.New(db, store.WithAlertChannels("slack"))
	ctx := context.Background()
	m := insertMonitor(t, db, "api", 1, 1)
	if err := s.RecordResult(ctx, m, monitor.Result{MonitorID: m.ID, CheckedAt: time.Now(), Error: "down"}); err != nil {
		t.Fatal(err)
	}
	ds, err := s.ClaimDeliveries(ctx, 10, time.Minute)
	if err != nil || len(ds) != 1 {
		t.Fatalf("claim: %v %v", ds, err)
	}
	// Retry immediately: due again, with the attempt counted.
	if err := s.MarkFailed(ctx, ds[0].ID, "timeout", 0, false); err != nil {
		t.Fatal(err)
	}
	ds, err = s.ClaimDeliveries(ctx, 10, time.Minute)
	if err != nil || len(ds) != 1 || ds[0].Attempts != 2 {
		t.Fatalf("reclaim: %+v %v", ds, err)
	}
	if err := s.MarkDelivered(ctx, ds[0].ID); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM notifications WHERE id = $1`, ds[0].ID).Scan(&status); err != nil || status != "sent" {
		t.Fatalf("status %q err %v", status, err)
	}
}
