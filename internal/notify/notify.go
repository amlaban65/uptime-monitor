// Package notify delivers incident alerts. Notifier is the interface each
// channel implements; Dispatcher drains the database outbox and handles
// retries, so channel implementations only need to make one attempt.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EventKind is what happened to an incident.
type EventKind string

const (
	EventOpened   EventKind = "opened"
	EventResolved EventKind = "resolved"
)

// Event is the content of one alert.
type Event struct {
	IncidentID  int64
	Kind        EventKind
	MonitorName string
	Target      string
	StartedAt   time.Time
	ResolvedAt  *time.Time
	Cause       string
}

// Notifier sends an alert over one channel.
type Notifier interface {
	Notify(ctx context.Context, e Event) error
}

// permanentError marks failures that retrying cannot fix, such as a revoked
// webhook (HTTP 404) or a rejected recipient (SMTP 5xx).
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent wraps err so the dispatcher gives up instead of retrying.
func Permanent(err error) error { return permanentError{err} }

// IsPermanent reports whether err was wrapped with Permanent.
func IsPermanent(err error) bool { return errors.As(err, new(permanentError)) }

// Subject is a one-line summary shared by all channels.
func (e Event) Subject() string {
	name := strings.NewReplacer("\r", " ", "\n", " ").Replace(e.MonitorName)
	if e.Kind == EventResolved {
		return fmt.Sprintf("[RESOLVED] %s is back up", name)
	}
	return fmt.Sprintf("[DOWN] %s is failing", name)
}

// Body is a short plain-text description shared by all channels.
func (e Event) Body(statusURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Monitor: %s (%s)\n", e.MonitorName, e.Target)
	fmt.Fprintf(&b, "Started: %s\n", e.StartedAt.UTC().Format(time.RFC1123))
	if e.ResolvedAt != nil {
		fmt.Fprintf(&b, "Resolved: %s (down for %s)\n", e.ResolvedAt.UTC().Format(time.RFC1123),
			e.ResolvedAt.Sub(e.StartedAt).Round(time.Second))
	}
	if e.Cause != "" {
		fmt.Fprintf(&b, "Cause: %s\n", e.Cause)
	}
	if statusURL != "" {
		fmt.Fprintf(&b, "Status page: %s\n", statusURL)
	}
	return b.String()
}
