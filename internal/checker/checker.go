// Package checker performs individual HTTP and TCP checks. Checkers honor
// the context deadline (the scheduler sets it from the monitor's timeout)
// and report failures as results, not errors: a failed check is data.
package checker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
)

// Checker runs one check for a monitor.
type Checker interface {
	Check(ctx context.Context, m monitor.Monitor) monitor.Result
}

// ByKind dispatches to the checker for the monitor's kind.
type ByKind map[monitor.Kind]Checker

func (b ByKind) Check(ctx context.Context, m monitor.Monitor) monitor.Result {
	c, ok := b[m.Kind]
	if !ok {
		return monitor.Result{MonitorID: m.ID, CheckedAt: time.Now(), Error: fmt.Sprintf("no checker for type %q", m.Kind)}
	}
	return c.Check(ctx, m)
}

// Default returns the production checkers.
func Default() ByKind {
	return ByKind{
		monitor.KindHTTP: NewHTTP(),
		monitor.KindTCP:  NewTCP(),
	}
}

// describe turns low-level errors into short messages fit for a status page.
func describe(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns: " + dnsErr.Err
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Op + ": " + opErr.Err.Error()
	}
	return err.Error()
}
