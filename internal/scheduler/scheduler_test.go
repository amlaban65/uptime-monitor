package scheduler

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/clock"
	"github.com/amlaban65/uptime-monitor/internal/monitor"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeStore keeps next-run times in memory and uses the fake clock to decide
// what is due, mirroring what the Postgres store does with now().
type fakeStore struct {
	mu       sync.Mutex
	clk      *clock.Fake
	monitors []monitor.Monitor
	nextRun  map[int64]time.Time
	limits   []int
	recorded chan monitor.Result
}

func newFakeStore(clk *clock.Fake, ms ...monitor.Monitor) *fakeStore {
	f := &fakeStore{clk: clk, monitors: ms, nextRun: map[int64]time.Time{}, recorded: make(chan monitor.Result, 100)}
	for _, m := range ms {
		f.nextRun[m.ID] = clk.Now()
	}
	return f
}

func (f *fakeStore) ClaimDue(_ context.Context, limit int) ([]monitor.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limits = append(f.limits, limit)
	now := f.clk.Now()
	var out []monitor.Monitor
	for _, m := range f.monitors {
		if len(out) == limit {
			break
		}
		if !f.nextRun[m.ID].After(now) {
			f.nextRun[m.ID] = now.Add(m.Interval.Std())
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStore) RecordResult(_ context.Context, _ monitor.Monitor, r monitor.Result) error {
	f.recorded <- r
	return nil
}

// fakeChecker counts calls and can block until released.
type fakeChecker struct {
	mu       sync.Mutex
	calls    map[int64]int
	inFlight int
	maxSeen  int
	release  chan struct{} // nil means do not block
	started  chan struct{}
	deadline map[int64]time.Duration
}

func newFakeChecker() *fakeChecker {
	return &fakeChecker{calls: map[int64]int{}, deadline: map[int64]time.Duration{}, started: make(chan struct{}, 100)}
}

func (c *fakeChecker) Check(ctx context.Context, m monitor.Monitor) monitor.Result {
	c.mu.Lock()
	c.calls[m.ID]++
	c.inFlight++
	c.maxSeen = max(c.maxSeen, c.inFlight)
	if dl, ok := ctx.Deadline(); ok {
		c.deadline[m.ID] = time.Until(dl)
	}
	c.mu.Unlock()
	c.started <- struct{}{}

	var err string
	if c.release != nil {
		<-c.release
		if ctx.Err() != nil {
			err = "cancelled"
		}
	}
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return monitor.Result{OK: err == "", Error: err}
}

func (c *fakeChecker) count(id int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[id]
}

func mon(id int64, interval time.Duration) monitor.Monitor {
	return monitor.Monitor{ID: id, Spec: monitor.Spec{
		Name: "m", Kind: monitor.KindHTTP,
		Interval: monitor.Duration(interval), Timeout: monitor.Duration(interval / 2),
	}}
}

// pollUntilStarted advances the clock one poll at a time until a new check
// starts. More than one poll may be needed because a finished check frees
// its slot just after its result is recorded.
func pollUntilStarted(t *testing.T, clk *clock.Fake, chk *fakeChecker) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		clk.BlockUntil(1)
		clk.Advance(time.Second)
		select {
		case <-chk.started:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("no check started")
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitResults(t *testing.T, ch <-chan monitor.Result, n int) []monitor.Result {
	t.Helper()
	var out []monitor.Result
	for range n {
		select {
		case r := <-ch:
			out = append(out, r)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for result %d of %d", len(out)+1, n)
		}
	}
	return out
}

func TestRunsEachMonitorOncePerInterval(t *testing.T) {
	clk := clock.NewFake(t0)
	store := newFakeStore(clk, mon(1, 10*time.Second), mon(2, 30*time.Second))
	chk := newFakeChecker()
	s := New(store, chk, clk, Config{Concurrency: 4, PollInterval: time.Second}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	// Both run immediately, then advance 60s one poll at a time. After
	// each tick, wait for the checks that tick made due, so fake time never
	// runs ahead of goroutines that have not been scheduled yet.
	waitResults(t, store.recorded, 2)
	for sec := 1; sec <= 60; sec++ {
		clk.BlockUntil(1)
		clk.Advance(time.Second)
		due := 0
		if sec%10 == 0 {
			due++
		}
		if sec%30 == 0 {
			due++
		}
		waitResults(t, store.recorded, due)
	}
	cancel()
	<-done

	if got := chk.count(1); got != 7 {
		t.Errorf("monitor 1 ran %d times, want 7", got)
	}
	if got := chk.count(2); got != 3 {
		t.Errorf("monitor 2 ran %d times, want 3", got)
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	clk := clock.NewFake(t0)
	var ms []monitor.Monitor
	for i := range 10 {
		ms = append(ms, mon(int64(i+1), time.Hour))
	}
	store := newFakeStore(clk, ms...)
	chk := newFakeChecker()
	chk.release = make(chan struct{})
	s := New(store, chk, clk, Config{Concurrency: 3, PollInterval: time.Second}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	// Release checks one at a time; each poll may only claim the freed slots.
	for range 3 {
		<-chk.started
	}
	for range 7 {
		chk.release <- struct{}{}
		waitResults(t, store.recorded, 1)
		pollUntilStarted(t, clk, chk)
	}
	close(chk.release)
	waitResults(t, store.recorded, 3)
	cancel()
	<-done

	if chk.maxSeen != 3 {
		t.Fatalf("max in flight = %d, want 3", chk.maxSeen)
	}
	for _, l := range store.limits {
		if l < 1 || l > 3 {
			t.Fatalf("claimed with limit %d; must never exceed free slots", l)
		}
	}
}

func TestPerCheckTimeout(t *testing.T) {
	clk := clock.NewFake(t0)
	m := mon(1, 10*time.Second) // timeout 5s
	store := newFakeStore(clk, m)
	chk := newFakeChecker()
	s := New(store, chk, clk, Config{Concurrency: 1, PollInterval: time.Second}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	waitResults(t, store.recorded, 1)
	cancel()

	chk.mu.Lock()
	defer chk.mu.Unlock()
	if d := chk.deadline[1]; d <= 4*time.Second || d > 5*time.Second {
		t.Fatalf("check context deadline in %v, want about 5s", d)
	}
}

func TestShutdownFinishesInFlightChecks(t *testing.T) {
	clk := clock.NewFake(t0)
	store := newFakeStore(clk, mon(1, time.Minute))
	chk := newFakeChecker()
	chk.release = make(chan struct{})
	s := New(store, chk, clk, Config{Concurrency: 1, PollInterval: time.Second}, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	<-chk.started
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned while a check was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(chk.release)
	<-done

	r := waitResults(t, store.recorded, 1)[0]
	if !r.OK || r.MonitorID != 1 {
		t.Fatalf("in-flight check was not completed cleanly: %+v", r)
	}
}
