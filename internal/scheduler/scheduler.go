// Package scheduler runs due checks with bounded concurrency.
//
// The scheduler does not keep timers per monitor. Instead each replica polls
// the store for due monitors, and the store atomically claims them (moving
// next_run_at forward) so that no other replica can take the same run. This
// keeps the schedule in one place, survives restarts, and works the same with
// one replica or many.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/checker"
	"github.com/amlaban65/uptime-monitor/internal/clock"
	"github.com/amlaban65/uptime-monitor/internal/monitor"
)

// Store is the persistence the scheduler needs.
type Store interface {
	// ClaimDue returns up to limit monitors that are due and reschedules
	// them, so a monitor is returned at most once per interval.
	ClaimDue(ctx context.Context, limit int) ([]monitor.Monitor, error)
	// RecordResult persists a result and applies incident logic.
	RecordResult(ctx context.Context, m monitor.Monitor, r monitor.Result) error
}

// Config tunes the scheduler.
type Config struct {
	Concurrency  int           // max checks in flight
	PollInterval time.Duration // delay between looking for due work
	// OnResult, if set, is called after each result is recorded. It is the
	// hook for metrics and must not block.
	OnResult func(m monitor.Monitor, r monitor.Result)
}

type Scheduler struct {
	store   Store
	checker checker.Checker
	clock   clock.Clock
	cfg     Config
	log     *slog.Logger
	slots   chan struct{}
	wg      sync.WaitGroup
}

func New(store Store, c checker.Checker, clk clock.Clock, cfg Config, log *slog.Logger) *Scheduler {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	return &Scheduler{
		store:   store,
		checker: c,
		clock:   clk,
		cfg:     cfg,
		log:     log,
		slots:   make(chan struct{}, cfg.Concurrency),
	}
}

// InFlight returns the number of checks currently running.
func (s *Scheduler) InFlight() int { return len(s.slots) }

// Run polls for due monitors until ctx is cancelled, then waits for
// in-flight checks to finish before returning.
func (s *Scheduler) Run(ctx context.Context) {
	defer s.wg.Wait()
	for {
		s.dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(s.cfg.PollInterval):
		}
	}
}

// dispatch claims only as many monitors as there are free slots. Claiming
// more would make this replica sit on work that another replica with free
// capacity could have run on time.
func (s *Scheduler) dispatch(ctx context.Context) {
	free := cap(s.slots) - len(s.slots)
	if free == 0 || ctx.Err() != nil {
		return
	}
	due, err := s.store.ClaimDue(ctx, free)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("claim due monitors", "err", err)
		}
		return
	}
	for _, m := range due {
		// Never blocks: only this goroutine acquires slots, and we asked for
		// at most the number that were free.
		s.slots <- struct{}{}
		s.wg.Add(1)
		go func() {
			defer func() { <-s.slots; s.wg.Done() }()
			s.runOne(ctx, m)
		}()
	}
}

func (s *Scheduler) runOne(ctx context.Context, m monitor.Monitor) {
	// A claimed check is finished even during shutdown: cancelling it would
	// record a bogus failure. The per-check timeout still bounds how long
	// shutdown can take.
	ctx = context.WithoutCancel(ctx)
	checkCtx, cancel := context.WithTimeout(ctx, m.Timeout.Std())
	r := s.checker.Check(checkCtx, m)
	cancel()
	r.MonitorID = m.ID

	if err := s.store.RecordResult(ctx, m, r); err != nil {
		s.log.Error("record result", "monitor", m.Name, "err", err)
		return
	}
	if s.cfg.OnResult != nil {
		s.cfg.OnResult(m, r)
	}
}
