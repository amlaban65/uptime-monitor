package monitor

import (
	"strings"
	"testing"
	"time"
)

func TestParseFile(t *testing.T) {
	specs, err := ParseFile([]byte(`
monitors:
  - name: homepage
    type: http
    target: https://example.com
    interval: 30s
    body_contains: Welcome
  - name: database
    type: tcp
    target: db:5432
    enabled: false
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("got %d specs", len(specs))
	}
	if specs[0].Interval.Std() != 30*time.Second || specs[0].Method != "GET" || specs[0].BodyContains != "Welcome" {
		t.Fatalf("spec 0 = %+v", specs[0])
	}
	if specs[1].IsEnabled() {
		t.Fatal("spec 1 should be disabled")
	}
}

func TestParseFileErrors(t *testing.T) {
	tests := map[string]string{
		"unknown key": "monitors:\n  - name: a\n    type: http\n    target: http://a\n    intervall: 5s\n",
		"invalid":     "monitors:\n  - name: a\n    type: http\n    target: not-a-url\n",
		"duplicate":   "monitors:\n  - {name: a, type: tcp, target: 'a:1'}\n  - {name: a, type: tcp, target: 'b:1'}\n",
	}
	wants := map[string]string{"unknown key": "intervall", "invalid": "http:// or https://", "duplicate": "duplicate name"}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFile([]byte(doc))
			if err == nil || !strings.Contains(err.Error(), wants[name]) {
				t.Fatalf("err = %v, want containing %q", err, wants[name])
			}
		})
	}
}

func TestParseEmptyFile(t *testing.T) {
	specs, err := ParseFile(nil)
	if err != nil || len(specs) != 0 {
		t.Fatalf("specs=%v err=%v", specs, err)
	}
}
