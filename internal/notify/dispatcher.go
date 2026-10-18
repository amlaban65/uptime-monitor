package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/clock"
)

// Delivery is one pending alert for one channel, claimed from the outbox.
type Delivery struct {
	ID       int64
	Channel  string
	Attempts int // including the current attempt
	Event    Event
}

// Outbox is the durable queue of deliveries.
type Outbox interface {
	// ClaimDeliveries leases up to limit due deliveries for lease. A lease
	// that expires (because this process died mid-send) makes the delivery
	// due again, so delivery is at-least-once.
	ClaimDeliveries(ctx context.Context, limit int, lease time.Duration) ([]Delivery, error)
	MarkDelivered(ctx context.Context, id int64) error
	// MarkFailed records a failed attempt. If final, the delivery is given
	// up on; otherwise it is retried after retryIn.
	MarkFailed(ctx context.Context, id int64, errMsg string, retryIn time.Duration, final bool) error
}

// DispatcherConfig tunes delivery and retry behavior.
type DispatcherConfig struct {
	MaxAttempts  int
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	SendTimeout  time.Duration
	PollInterval time.Duration
	BatchSize    int
	// OnAttempt, if set, is called after every attempt (for metrics).
	OnAttempt func(channel string, err error)
}

// DefaultDispatcherConfig retries for roughly an hour before giving up.
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		MaxAttempts:  8,
		BaseBackoff:  30 * time.Second,
		MaxBackoff:   30 * time.Minute,
		SendTimeout:  20 * time.Second,
		PollInterval: 2 * time.Second,
		BatchSize:    20,
	}
}

// Dispatcher moves deliveries from the outbox to notifiers.
type Dispatcher struct {
	outbox    Outbox
	notifiers map[string]Notifier
	clock     clock.Clock
	cfg       DispatcherConfig
	log       *slog.Logger
}

func NewDispatcher(o Outbox, notifiers map[string]Notifier, clk clock.Clock, cfg DispatcherConfig, log *slog.Logger) *Dispatcher {
	return &Dispatcher{outbox: o, notifiers: notifiers, clock: clk, cfg: cfg, log: log}
}

// Run delivers alerts until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		if _, err := d.RunOnce(ctx); err != nil && ctx.Err() == nil {
			d.log.Error("dispatch notifications", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-d.clock.After(d.cfg.PollInterval):
		}
	}
}

// RunOnce claims one batch and attempts each delivery once. It returns how
// many deliveries were attempted.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	// The lease must outlast the send timeout, or a slow send could be
	// claimed and sent again by another replica.
	batch, err := d.outbox.ClaimDeliveries(ctx, d.cfg.BatchSize, 2*d.cfg.SendTimeout)
	if err != nil {
		return 0, err
	}
	for _, del := range batch {
		d.deliver(ctx, del)
	}
	return len(batch), nil
}

func (d *Dispatcher) deliver(ctx context.Context, del Delivery) {
	n, ok := d.notifiers[del.Channel]
	var err error
	if !ok {
		err = Permanent(fmt.Errorf("no notifier configured for channel %q", del.Channel))
	} else {
		sendCtx, cancel := context.WithTimeout(ctx, d.cfg.SendTimeout)
		err = n.Notify(sendCtx, del.Event)
		cancel()
	}
	if d.cfg.OnAttempt != nil {
		d.cfg.OnAttempt(del.Channel, err)
	}

	log := d.log.With("delivery", del.ID, "channel", del.Channel, "incident", del.Event.IncidentID, "attempt", del.Attempts)
	// Bookkeeping must not be skipped because shutdown began mid-send.
	bctx := context.WithoutCancel(ctx)
	if err == nil {
		if merr := d.outbox.MarkDelivered(bctx, del.ID); merr != nil {
			log.Error("mark delivered", "err", merr)
		}
		log.Info("alert sent", "event", del.Event.Kind)
		return
	}

	final := IsPermanent(err) || del.Attempts >= d.cfg.MaxAttempts
	retryIn := Backoff(del.Attempts, d.cfg.BaseBackoff, d.cfg.MaxBackoff)
	if merr := d.outbox.MarkFailed(bctx, del.ID, err.Error(), retryIn, final); merr != nil {
		log.Error("mark failed", "err", merr)
	}
	if final {
		log.Error("alert abandoned", "err", err)
	} else {
		log.Warn("alert failed, will retry", "err", err, "retry_in", retryIn)
	}
}

// Backoff returns base * 2^(attempt-1), capped at max.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	return min(d, max)
}
