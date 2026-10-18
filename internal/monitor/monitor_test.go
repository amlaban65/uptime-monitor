package monitor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNormalizeAppliesDefaults(t *testing.T) {
	s := Spec{Name: " api ", Kind: "HTTP", Target: "https://example.com"}
	s.Normalize()
	if s.Name != "api" || s.Kind != KindHTTP || s.Method != "GET" || s.ExpectedStatus != 200 {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if s.Interval.Std() != DefaultInterval || s.Timeout.Std() != DefaultTimeout {
		t.Fatalf("interval=%s timeout=%s", s.Interval, s.Timeout)
	}
	if !s.IsEnabled() {
		t.Fatal("expected enabled by default")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func TestNormalizeShortIntervalShrinksTimeout(t *testing.T) {
	s := Spec{Name: "x", Kind: KindTCP, Target: "db:5432", Interval: Duration(10 * time.Second)}
	s.Normalize()
	if s.Timeout.Std() != 5*time.Second {
		t.Fatalf("timeout = %s, want 5s", s.Timeout)
	}
}

func TestValidate(t *testing.T) {
	base := func() Spec {
		s := Spec{Name: "x", Kind: KindHTTP, Target: "http://a"}
		s.Normalize()
		return s
	}
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
	}{
		{"valid", func(*Spec) {}, ""},
		{"bad kind", func(s *Spec) { s.Kind = "icmp" }, "type must be"},
		{"bad url", func(s *Spec) { s.Target = "ftp://a" }, "http:// or https://"},
		{"bad method", func(s *Spec) { s.Method = "DELETE" }, "not supported"},
		{"timeout not below interval", func(s *Spec) { s.Timeout = s.Interval }, "shorter than interval"},
		{"interval too short", func(s *Spec) { s.Interval = Duration(time.Second); s.Timeout = Duration(time.Millisecond) }, "interval must be"},
		{"tcp without port", func(s *Spec) { s.Kind = KindTCP; s.Target = "db"; s.Method = ""; s.ExpectedStatus = 0 }, "host:port"},
		{"tcp with http fields", func(s *Spec) { s.Kind = KindTCP; s.Target = "db:1" }, "apply only to http"},
		{"empty name", func(s *Spec) { s.Name = "" }, "name must be"},
		{"zero threshold", func(s *Spec) { s.FailureThreshold = 0 }, "failure_threshold"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base()
			tt.mutate(&s)
			err := s.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	var s Spec
	if err := json.Unmarshal([]byte(`{"interval":"30s","timeout":"1.5s"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Interval.Std() != 30*time.Second || s.Timeout.Std() != 1500*time.Millisecond {
		t.Fatalf("got %s %s", s.Interval, s.Timeout)
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"interval":"30s"`) {
		t.Fatalf("marshal: %s", b)
	}
	if err := json.Unmarshal([]byte(`{"interval":"soon"}`), &s); err == nil {
		t.Fatal("expected error for bad duration")
	}
}
