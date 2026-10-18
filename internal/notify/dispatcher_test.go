package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/clock"
)

// fakeOutbox is an in-memory outbox. Leases and retry delays are tracked in
// fake-clock time so tests can step through a retry schedule.
type fakeOutbox struct {
	mu    sync.Mutex
	clk   *clock.Fake
	items []*fakeItem
}

type fakeItem struct {
	d       Delivery
	status  string
	dueAt   time.Time
	lastErr string
	retries []time.Duration
}

func (o *fakeOutbox) add(channel string) *fakeItem {
	o.mu.Lock()
	defer o.mu.Unlock()
	it := &fakeItem{d: Delivery{ID: int64(len(o.items) + 1), Channel: channel, Event: Event{IncidentID: 1, Kind: EventOpened, MonitorName: "api"}}, status: "pending", dueAt: o.clk.Now()}
	o.items = append(o.items, it)
	return it
}

func (o *fakeOutbox) ClaimDeliveries(_ context.Context, limit int, lease time.Duration) ([]Delivery, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.clk.Now()
	var out []Delivery
	for _, it := range o.items {
		if len(out) < limit && it.status == "pending" && !it.dueAt.After(now) {
			it.d.Attempts++
			it.dueAt = now.Add(lease)
			out = append(out, it.d)
		}
	}
	return out, nil
}

func (o *fakeOutbox) MarkDelivered(_ context.Context, id int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.items[id-1].status = "sent"
	return nil
}

func (o *fakeOutbox) MarkFailed(_ context.Context, id int64, msg string, retryIn time.Duration, final bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	it := o.items[id-1]
	it.lastErr = msg
	if final {
		it.status = "failed"
		return nil
	}
	it.retries = append(it.retries, retryIn)
	it.dueAt = o.clk.Now().Add(retryIn)
	return nil
}

// scriptedNotifier returns errors from a script, then succeeds.
type scriptedNotifier struct {
	mu     sync.Mutex
	script []error
	sent   int
	calls  int
}

func (n *scriptedNotifier) Notify(context.Context, Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	if len(n.script) > 0 {
		err := n.script[0]
		n.script = n.script[1:]
		return err
	}
	n.sent++
	return nil
}

func newTestDispatcher(n Notifier) (*Dispatcher, *fakeOutbox, *clock.Fake) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	ob := &fakeOutbox{clk: clk}
	cfg := DispatcherConfig{MaxAttempts: 4, BaseBackoff: 30 * time.Second, MaxBackoff: time.Minute, SendTimeout: time.Second, PollInterval: time.Second, BatchSize: 10}
	d := NewDispatcher(ob, map[string]Notifier{"slack": n}, clk, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return d, ob, clk
}

// drain runs the dispatcher, advancing fake time by step, for steps rounds.
func drain(t *testing.T, d *Dispatcher, clk *clock.Fake, step time.Duration, steps int) {
	t.Helper()
	for range steps {
		if _, err := d.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		clk.Advance(step)
	}
}

func TestDispatcherSendsOnce(t *testing.T) {
	n := &scriptedNotifier{}
	d, ob, clk := newTestDispatcher(n)
	it := ob.add("slack")
	drain(t, d, clk, time.Minute, 10)
	if n.sent != 1 || n.calls != 1 || it.status != "sent" {
		t.Fatalf("sent=%d calls=%d status=%s", n.sent, n.calls, it.status)
	}
}

func TestDispatcherRetriesWithBackoff(t *testing.T) {
	transient := errors.New("connection reset")
	n := &scriptedNotifier{script: []error{transient, transient, transient}}
	d, ob, clk := newTestDispatcher(n)
	it := ob.add("slack")

	// Step in 10s increments so retries happen as soon as they are due.
	drain(t, d, clk, 10*time.Second, 30)

	if it.status != "sent" || n.calls != 4 || n.sent != 1 {
		t.Fatalf("status=%s calls=%d sent=%d", it.status, n.calls, n.sent)
	}
	want := []time.Duration{30 * time.Second, time.Minute, time.Minute} // capped at MaxBackoff
	if len(it.retries) != len(want) {
		t.Fatalf("retries = %v, want %v", it.retries, want)
	}
	for i := range want {
		if it.retries[i] != want[i] {
			t.Fatalf("retries = %v, want %v", it.retries, want)
		}
	}
}

func TestDispatcherGivesUpAfterMaxAttempts(t *testing.T) {
	fail := errors.New("503")
	n := &scriptedNotifier{script: []error{fail, fail, fail, fail, fail, fail}}
	d, ob, clk := newTestDispatcher(n)
	it := ob.add("slack")
	drain(t, d, clk, time.Minute, 20)
	if it.status != "failed" || n.calls != 4 || it.lastErr != "503" {
		t.Fatalf("status=%s calls=%d lastErr=%q", it.status, n.calls, it.lastErr)
	}
}

func TestDispatcherDoesNotRetryPermanentErrors(t *testing.T) {
	n := &scriptedNotifier{script: []error{Permanent(errors.New("HTTP 404: no_team"))}}
	d, ob, clk := newTestDispatcher(n)
	it := ob.add("slack")
	drain(t, d, clk, time.Minute, 10)
	if it.status != "failed" || n.calls != 1 {
		t.Fatalf("status=%s calls=%d", it.status, n.calls)
	}
}

func TestDispatcherUnknownChannel(t *testing.T) {
	d, ob, clk := newTestDispatcher(&scriptedNotifier{})
	it := ob.add("pagerduty")
	drain(t, d, clk, time.Minute, 3)
	if it.status != "failed" || it.d.Attempts != 1 {
		t.Fatalf("status=%s attempts=%d", it.status, it.d.Attempts)
	}
}

func TestBackoff(t *testing.T) {
	base, maxB := 30*time.Second, 30*time.Minute
	tests := map[int]time.Duration{
		1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 6: 16 * time.Minute, 7: 30 * time.Minute, 50: 30 * time.Minute,
	}
	for attempt, want := range tests {
		if got := Backoff(attempt, base, maxB); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

func TestIsPermanent(t *testing.T) {
	err := Permanent(errors.New("bad"))
	if !IsPermanent(err) || !IsPermanent(errors.Join(errors.New("x"), err)) || IsPermanent(errors.New("bad")) {
		t.Fatal("IsPermanent misclassified")
	}
}
