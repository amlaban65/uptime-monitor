package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/stats"
	"github.com/amlaban65/uptime-monitor/internal/status"
)

type staticStatus status.Snapshot

func (s staticStatus) Snapshot(context.Context) (status.Snapshot, error) {
	return status.Snapshot(s), nil
}

func TestStatusPageAndJSON(t *testing.T) {
	now := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	up := 99.95
	snap := status.Snapshot{
		Title: "Acme <Status>", GeneratedAt: now, Overall: status.OverallPartial,
		Monitors: []status.MonitorStatus{
			{ID: 1, Name: "checkout", Status: "down", Uptime24h: &up, Bars: stats.DailyBars(nil, now, 90)},
		},
		Incidents: []status.Incident{{MonitorName: "checkout", StartedAt: now.Add(-time.Minute), Ongoing: true, Duration: "1m0s"}},
	}
	srv := New(Deps{Status: staticStatus(snap), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("code %d type %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{"Acme &lt;Status&gt;", "Partial outage", "checkout", "24h 99.95%", "Ongoing for 1m0s"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if n := strings.Count(body, `class="bar `); n != 90 {
		t.Errorf("rendered %d bars, want 90", n)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	var got status.Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Overall != status.OverallPartial || len(got.Monitors) != 1 || len(got.Monitors[0].Bars) != 90 {
		t.Fatalf("json snapshot = %+v", got)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path: %d", rec.Code)
	}
}
