// Package monitor defines the core domain types: what to check and the
// outcome of a single check.
package monitor

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Kind is the type of check a monitor performs.
type Kind string

const (
	KindHTTP Kind = "http"
	KindTCP  Kind = "tcp"
)

// Source records where a monitor definition came from. Monitors loaded from
// the YAML file are owned by that file and are read-only through the API.
type Source string

const (
	SourceAPI  Source = "api"
	SourceFile Source = "file"
)

// Defaults applied by Spec.Normalize when a field is left empty.
const (
	DefaultInterval          = 60 * time.Second
	DefaultTimeout           = 10 * time.Second
	DefaultFailureThreshold  = 3
	DefaultRecoveryThreshold = 2
	MinInterval              = 5 * time.Second
	MaxInterval              = 24 * time.Hour
)

// Spec is the user-supplied definition of a monitor, shared by the JSON API
// and the YAML file.
type Spec struct {
	Name              string   `json:"name" yaml:"name"`
	Kind              Kind     `json:"type" yaml:"type"`
	Target            string   `json:"target" yaml:"target"`
	Method            string   `json:"method,omitempty" yaml:"method"`
	ExpectedStatus    int      `json:"expected_status,omitempty" yaml:"expected_status"`
	BodyContains      string   `json:"body_contains,omitempty" yaml:"body_contains"`
	Interval          Duration `json:"interval" yaml:"interval"`
	Timeout           Duration `json:"timeout" yaml:"timeout"`
	FailureThreshold  int      `json:"failure_threshold,omitempty" yaml:"failure_threshold"`
	RecoveryThreshold int      `json:"recovery_threshold,omitempty" yaml:"recovery_threshold"`
	Enabled           *bool    `json:"enabled,omitempty" yaml:"enabled"`
}

// Monitor is a stored monitor.
type Monitor struct {
	ID int64 `json:"id"`
	Spec
	Source    Source    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsEnabled reports whether the monitor should be scheduled.
func (s Spec) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// Normalize fills defaults in place. Call it before Validate.
func (s *Spec) Normalize() {
	s.Name = strings.TrimSpace(s.Name)
	s.Target = strings.TrimSpace(s.Target)
	s.Kind = Kind(strings.ToLower(string(s.Kind)))
	if s.Interval == 0 {
		s.Interval = Duration(DefaultInterval)
	}
	if s.Timeout == 0 {
		s.Timeout = Duration(min(DefaultTimeout, s.Interval.Std()/2))
	}
	if s.FailureThreshold == 0 {
		s.FailureThreshold = DefaultFailureThreshold
	}
	if s.RecoveryThreshold == 0 {
		s.RecoveryThreshold = DefaultRecoveryThreshold
	}
	if s.Enabled == nil {
		t := true
		s.Enabled = &t
	}
	if s.Kind == KindHTTP {
		s.Method = strings.ToUpper(s.Method)
		if s.Method == "" {
			s.Method = http.MethodGet
		}
		if s.ExpectedStatus == 0 {
			s.ExpectedStatus = http.StatusOK
		}
	}
}

// Validate returns every problem with the spec joined into one error, so API
// clients can fix them all in one round trip.
func (s Spec) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if s.Name == "" || len(s.Name) > 100 {
		add("name must be 1-100 characters")
	}
	switch s.Kind {
	case KindHTTP:
		u, err := url.Parse(s.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("target must be an http:// or https:// URL")
		}
		switch s.Method {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodOptions:
		default:
			add("method %q is not supported", s.Method)
		}
		if s.ExpectedStatus < 100 || s.ExpectedStatus > 599 {
			add("expected_status must be between 100 and 599")
		}
	case KindTCP:
		host, port, err := net.SplitHostPort(s.Target)
		if err != nil || host == "" || port == "" {
			add("target must be host:port for tcp monitors")
		}
		if s.Method != "" || s.BodyContains != "" || s.ExpectedStatus != 0 {
			add("method, expected_status and body_contains apply only to http monitors")
		}
	default:
		add("type must be %q or %q", KindHTTP, KindTCP)
	}
	if s.Interval.Std() < MinInterval || s.Interval.Std() > MaxInterval {
		add("interval must be between %s and %s", MinInterval, MaxInterval)
	}
	// A timeout shorter than the interval guarantees one run finishes before
	// the next is due, so runs of the same monitor never overlap.
	if s.Timeout <= 0 || s.Timeout >= s.Interval {
		add("timeout must be positive and shorter than interval")
	}
	if s.FailureThreshold < 1 || s.FailureThreshold > 100 {
		add("failure_threshold must be between 1 and 100")
	}
	if s.RecoveryThreshold < 1 || s.RecoveryThreshold > 100 {
		add("recovery_threshold must be between 1 and 100")
	}
	return errors.Join(errs...)
}

// Result is the outcome of one check.
type Result struct {
	MonitorID  int64
	CheckedAt  time.Time
	OK         bool
	Latency    time.Duration
	StatusCode int // HTTP only; 0 when not applicable
	Error      string
}
