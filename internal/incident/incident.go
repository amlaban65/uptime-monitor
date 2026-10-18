// Package incident implements flap-resistant up/down detection as a pure
// state machine. It has no I/O so every edge case can be table-tested; the
// store persists State and applies the returned Transition atomically.
package incident

import "time"

// Status is the confirmed health of a monitor.
type Status string

const (
	StatusUnknown Status = "unknown" // no check has completed yet
	StatusUp      Status = "up"
	StatusDown    Status = "down"
)

// Transition is what a single result caused, if anything.
type Transition int

const (
	None Transition = iota
	Opened
	Resolved
)

func (t Transition) String() string {
	switch t {
	case Opened:
		return "opened"
	case Resolved:
		return "resolved"
	default:
		return "none"
	}
}

// State is everything the state machine needs to remember between checks.
type State struct {
	Status               Status
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	// StreakStart is when the current run of identical results began. It is
	// used as the incident start (first failure) or end (first success), so
	// incident durations are not inflated by the confirmation thresholds.
	StreakStart time.Time
}

// Thresholds control hysteresis: how many consecutive results it takes to
// change the confirmed status.
type Thresholds struct {
	Failures  int // consecutive failures before an incident opens
	Successes int // consecutive successes before an incident resolves
}

// Next applies one check result and returns the new state plus the
// transition it caused. When the transition is Opened or Resolved, the
// returned State.StreakStart is the incident's start or end time.
func Next(s State, ok bool, at time.Time, th Thresholds) (State, Transition) {
	if s.Status == "" {
		s.Status = StatusUnknown
	}
	if ok {
		if s.ConsecutiveSuccesses == 0 {
			s.StreakStart = at
		}
		s.ConsecutiveSuccesses++
		s.ConsecutiveFailures = 0
		switch s.Status {
		case StatusUnknown:
			// Nothing to resolve, so one success is enough to report "up".
			s.Status = StatusUp
		case StatusDown:
			if s.ConsecutiveSuccesses >= th.Successes {
				s.Status = StatusUp
				return s, Resolved
			}
		}
		return s, None
	}

	if s.ConsecutiveFailures == 0 {
		s.StreakStart = at
	}
	s.ConsecutiveFailures++
	s.ConsecutiveSuccesses = 0
	if s.Status != StatusDown && s.ConsecutiveFailures >= th.Failures {
		s.Status = StatusDown
		return s, Opened
	}
	return s, None
}
