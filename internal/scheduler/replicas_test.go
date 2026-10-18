package scheduler

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/clock"
	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/store"
	"github.com/amlaban65/uptime-monitor/internal/testdb"
)

type countingChecker struct {
	mu    sync.Mutex
	calls map[int64]int
}

func (c *countingChecker) Check(_ context.Context, m monitor.Monitor) monitor.Result {
	time.Sleep(5 * time.Millisecond) // long enough for replicas to overlap
	c.mu.Lock()
	c.calls[m.ID]++
	c.mu.Unlock()
	return monitor.Result{CheckedAt: time.Now(), OK: true, Latency: 5 * time.Millisecond}
}

// TestReplicasRunEachCheckOnce runs three real schedulers against one
// Postgres database, as three replicas would, and checks that every
// monitor was checked exactly once in its interval.
func TestReplicasRunEachCheckOnce(t *testing.T) {
	dsn := testdb.MigratedURL(t)
	ctx := context.Background()
	admin := store.New(testdb.Open(t, dsn))
	const monitors = 40
	for i := range monitors {
		s := monitor.Spec{Name: fmt.Sprintf("svc-%02d", i), Kind: monitor.KindTCP, Target: "localhost:1", Interval: monitor.Duration(time.Hour)}
		s.Normalize()
		if _, err := admin.CreateMonitor(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	chk := &countingChecker{calls: map[int64]int{}}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for range 3 {
		st := store.New(testdb.Open(t, dsn), store.WithJitter(0.1))
		s := New(st, chk, clock.Real{}, Config{Concurrency: 4, PollInterval: 10 * time.Millisecond}, quietLog())
		wg.Add(1)
		go func() { defer wg.Done(); s.Run(runCtx) }()
	}
	time.Sleep(time.Second)
	cancel()
	wg.Wait()

	if len(chk.calls) != monitors {
		t.Fatalf("%d of %d monitors checked", len(chk.calls), monitors)
	}
	for id, n := range chk.calls {
		if n != 1 {
			t.Errorf("monitor %d checked %d times", id, n)
		}
	}
	db := testdb.Open(t, dsn)
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM check_results`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != monitors {
		t.Fatalf("%d results stored, want %d", rows, monitors)
	}
}
