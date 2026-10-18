package clock

import (
	"sync"
	"time"
)

// Fake is a manually advanced Clock for tests. Channels returned by After
// fire when Advance moves the clock past their deadline.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	changed chan struct{}
}

type waiter struct {
	deadline time.Time
	ch       chan time.Time
}

// NewFake returns a Fake clock set to t.
func NewFake(t time.Time) *Fake {
	return &Fake{now: t, changed: make(chan struct{})}
}

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, waiter{deadline: f.now.Add(d), ch: ch})
	f.notifyLocked()
	return ch
}

// Advance moves the clock forward and fires every expired waiter.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	kept := f.waiters[:0]
	for _, w := range f.waiters {
		if !w.deadline.After(f.now) {
			w.ch <- f.now
		} else {
			kept = append(kept, w)
		}
	}
	f.waiters = kept
}

// BlockUntil waits until at least n goroutines are blocked in After. Tests
// use it to avoid advancing the clock before the code under test is waiting.
func (f *Fake) BlockUntil(n int) {
	for {
		f.mu.Lock()
		count, changed := len(f.waiters), f.changed
		f.mu.Unlock()
		if count >= n {
			return
		}
		<-changed
	}
}

func (f *Fake) notifyLocked() {
	close(f.changed)
	f.changed = make(chan struct{})
}
