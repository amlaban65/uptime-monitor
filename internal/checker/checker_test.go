package checker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
)

func httpMonitor(target string, mutate func(*monitor.Spec)) monitor.Monitor {
	s := monitor.Spec{Name: "t", Kind: monitor.KindHTTP, Target: target}
	if mutate != nil {
		mutate(&s)
	}
	s.Normalize()
	return monitor.Monitor{ID: 7, Spec: s}
}

func TestHTTPCheck(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "all systems operational") })
	mux.HandleFunc("/fail", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	mux.HandleFunc("/created", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tests := []struct {
		name       string
		path       string
		mutate     func(*monitor.Spec)
		timeout    time.Duration
		wantOK     bool
		wantStatus int
		wantErr    string
	}{
		{name: "200 ok", path: "/ok", wantOK: true, wantStatus: 200},
		{name: "unexpected status", path: "/fail", wantStatus: 503, wantErr: "status 503, expected 200"},
		{name: "expected non-200 status", path: "/fail", mutate: func(s *monitor.Spec) { s.ExpectedStatus = 503 }, wantOK: true, wantStatus: 503},
		{name: "method is used", path: "/created", mutate: func(s *monitor.Spec) { s.Method = "POST"; s.ExpectedStatus = 201 }, wantOK: true, wantStatus: 201},
		{name: "body contains", path: "/ok", mutate: func(s *monitor.Spec) { s.BodyContains = "operational" }, wantOK: true, wantStatus: 200},
		{name: "body missing substring", path: "/ok", mutate: func(s *monitor.Spec) { s.BodyContains = "degraded" }, wantStatus: 200, wantErr: `body does not contain "degraded"`},
		{name: "follows redirects", path: "/redirect", wantOK: true, wantStatus: 200},
		{name: "redirect loop", path: "/loop", wantErr: "stopped after 5 redirects"},
		{name: "timeout", path: "/slow", timeout: 100 * time.Millisecond, wantErr: "timeout"},
	}
	h := NewHTTP()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}
			m := httpMonitor(srv.URL+tt.path, tt.mutate)
			start := time.Now()
			r := h.Check(ctx, m)
			if r.OK != tt.wantOK || r.StatusCode != tt.wantStatus || !strings.Contains(r.Error, tt.wantErr) {
				t.Fatalf("got ok=%v status=%d err=%q; want ok=%v status=%d err~%q", r.OK, r.StatusCode, r.Error, tt.wantOK, tt.wantStatus, tt.wantErr)
			}
			if tt.wantOK && r.Error != "" {
				t.Fatalf("ok result has error %q", r.Error)
			}
			if r.MonitorID != 7 || r.Latency <= 0 {
				t.Fatalf("monitor id %d latency %v", r.MonitorID, r.Latency)
			}
			if tt.timeout > 0 && time.Since(start) > time.Second {
				t.Fatalf("check took %v, deadline not honored", time.Since(start))
			}
		})
	}
}

func TestHTTPConnectionRefused(t *testing.T) {
	addr := closedPort(t)
	r := NewHTTP().Check(context.Background(), httpMonitor("http://"+addr, nil))
	if r.OK || !strings.Contains(r.Error, "connection refused") {
		t.Fatalf("got ok=%v err=%q", r.OK, r.Error)
	}
}

func TestTCPCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	m := monitor.Monitor{ID: 1, Spec: monitor.Spec{Kind: monitor.KindTCP, Target: ln.Addr().String()}}
	if r := NewTCP().Check(context.Background(), m); !r.OK || r.Error != "" {
		t.Fatalf("open port: %+v", r)
	}

	m.Target = closedPort(t)
	if r := NewTCP().Check(context.Background(), m); r.OK || !strings.Contains(r.Error, "connection refused") {
		t.Fatalf("closed port: %+v", r)
	}
}

func TestByKindUnknown(t *testing.T) {
	r := Default().Check(context.Background(), monitor.Monitor{ID: 3, Spec: monitor.Spec{Kind: "icmp"}})
	if r.OK || r.MonitorID != 3 || !strings.Contains(r.Error, "no checker") {
		t.Fatalf("got %+v", r)
	}
}

// closedPort returns an address that refuses connections.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}
