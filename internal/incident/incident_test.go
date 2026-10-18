package incident

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }
	th := Thresholds{Failures: 3, Successes: 2}

	tests := []struct {
		name       string
		results    []bool
		wantStatus Status
		wantTrans  []Transition // transition after each result
		wantStreak int          // index of the result that started the final streak
	}{
		{
			name:       "first success reports up without an incident",
			results:    []bool{true},
			wantStatus: StatusUp,
			wantTrans:  []Transition{None},
		},
		{
			name:       "failures below threshold do not open",
			results:    []bool{true, false, false},
			wantStatus: StatusUp,
			wantTrans:  []Transition{None, None, None},
			wantStreak: 1,
		},
		{
			name:       "threshold failures open exactly once",
			results:    []bool{true, false, false, false, false, false},
			wantStatus: StatusDown,
			wantTrans:  []Transition{None, None, None, Opened, None, None},
			wantStreak: 1,
		},
		{
			name:       "opens from unknown when the very first checks fail",
			results:    []bool{false, false, false},
			wantStatus: StatusDown,
			wantTrans:  []Transition{None, None, Opened},
		},
		{
			name:       "flapping never opens",
			results:    []bool{false, false, true, false, false, true, false, false, true},
			wantStatus: StatusUp,
			wantTrans:  []Transition{None, None, None, None, None, None, None, None, None},
			wantStreak: 8,
		},
		{
			name:       "single success while down does not resolve",
			results:    []bool{false, false, false, true, false},
			wantStatus: StatusDown,
			wantTrans:  []Transition{None, None, Opened, None, None},
			wantStreak: 4,
		},
		{
			name:       "recovery threshold resolves",
			results:    []bool{false, false, false, true, true, true},
			wantStatus: StatusUp,
			wantTrans:  []Transition{None, None, Opened, None, Resolved, None},
			wantStreak: 3,
		},
		{
			name:       "can reopen after resolving",
			results:    []bool{false, false, false, true, true, false, false, false},
			wantStatus: StatusDown,
			wantTrans:  []Transition{None, None, Opened, None, Resolved, None, None, Opened},
			wantStreak: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s State
			for i, ok := range tt.results {
				var tr Transition
				s, tr = Next(s, ok, at(i), th)
				if tr != tt.wantTrans[i] {
					t.Fatalf("result %d (ok=%v): transition = %v, want %v", i, ok, tr, tt.wantTrans[i])
				}
			}
			if s.Status != tt.wantStatus {
				t.Fatalf("status = %v, want %v", s.Status, tt.wantStatus)
			}
			if !s.StreakStart.Equal(at(tt.wantStreak)) {
				t.Fatalf("streak start = %v, want %v", s.StreakStart, at(tt.wantStreak))
			}
		})
	}
}

func TestNextIncidentTimesUseStreakStart(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	th := Thresholds{Failures: 2, Successes: 2}
	var s State
	var tr Transition

	s, _ = Next(s, true, t0, th)
	s, _ = Next(s, false, t0.Add(1*time.Minute), th)
	s, tr = Next(s, false, t0.Add(2*time.Minute), th)
	if tr != Opened || !s.StreakStart.Equal(t0.Add(1*time.Minute)) {
		t.Fatalf("opened=%v start=%v, want start at first failure", tr, s.StreakStart)
	}
	s, _ = Next(s, true, t0.Add(3*time.Minute), th)
	s, tr = Next(s, true, t0.Add(4*time.Minute), th)
	if tr != Resolved || !s.StreakStart.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("resolved=%v end=%v, want end at first success", tr, s.StreakStart)
	}
}

func TestThresholdOfOneIsImmediate(t *testing.T) {
	th := Thresholds{Failures: 1, Successes: 1}
	s, tr := Next(State{Status: StatusUp}, false, time.Now(), th)
	if tr != Opened {
		t.Fatalf("got %v, want Opened", tr)
	}
	if _, tr = Next(s, true, time.Now(), th); tr != Resolved {
		t.Fatalf("got %v, want Resolved", tr)
	}
}
