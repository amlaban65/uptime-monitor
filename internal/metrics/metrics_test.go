package metrics

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/store"
)

type fakeGauges struct {
	g   store.Gauges
	err error
}

func (f fakeGauges) Gauges(context.Context) (store.Gauges, error) { return f.g, f.err }

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestMetrics(t *testing.T) {
	src := fakeGauges{g: store.Gauges{
		MonitorStatus: map[string]string{"api": "up", "db": "down", "new": "unknown"},
		OpenIncidents: 1, PendingAlerts: 2, FailedAlerts: 3,
	}}
	m := New(src, func() int { return 4 }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mon := monitor.Monitor{Spec: monitor.Spec{Name: "api"}}
	m.ObserveCheck(mon, monitor.Result{OK: true, Latency: 30 * time.Millisecond})
	m.ObserveCheck(mon, monitor.Result{OK: false, Latency: 2 * time.Second})
	m.ObserveAlert("slack", nil)
	m.ObserveAlert("slack", errors.New("boom"))

	out := scrape(t, m)
	for _, want := range []string{
		`uptime_checks_total{monitor="api",result="success"} 1`,
		`uptime_checks_total{monitor="api",result="failure"} 1`,
		`uptime_check_duration_seconds_count{monitor="api"} 2`,
		`uptime_alert_attempts_total{channel="slack",result="failure"} 1`,
		`uptime_checks_in_flight 4`,
		`uptime_monitor_up{monitor="api"} 1`,
		`uptime_monitor_up{monitor="db"} 0`,
		`uptime_open_incidents 1`,
		`uptime_alerts_pending 2`,
		`uptime_alerts_failed 3`,
		`uptime_db_scrape_success 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	if strings.Contains(out, `monitor="new"`) {
		t.Error("unknown monitors should not report up/down")
	}
}

func TestMetricsDatabaseDown(t *testing.T) {
	m := New(fakeGauges{err: errors.New("connection refused")}, func() int { return 0 }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	out := scrape(t, m)
	if !strings.Contains(out, "uptime_db_scrape_success 0") {
		t.Fatalf("expected scrape failure gauge, got:\n%s", out)
	}
}
