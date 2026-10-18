package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amlaban65/uptime-monitor/internal/api"
	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/store"
	"github.com/amlaban65/uptime-monitor/internal/testdb"
)

const token = "s3cret"

func newServer(t *testing.T, adminToken string) (*httptest.Server, *store.Store) {
	t.Helper()
	st := store.New(testdb.New(t))
	srv := httptest.NewServer(api.New(st, adminToken, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(srv.Close)
	return srv, st
}

func do(t *testing.T, method, url, auth, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestMonitorCRUD(t *testing.T) {
	srv, _ := newServer(t, token)
	base := srv.URL + "/api/v1/monitors"

	code, body := do(t, "POST", base, token, `{"name":"api","type":"http","target":"https://example.com/health","interval":"30s","body_contains":"ok"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	if body["method"] != "GET" || body["expected_status"].(float64) != 200 || body["interval"] != "30s" || body["timeout"] != "10s" {
		t.Fatalf("defaults not applied: %v", body)
	}
	id := jsonNum(body["id"])
	item := base + "/" + id

	if code, body = do(t, "POST", base, token, `{"name":"api","type":"tcp","target":"db:5432"}`); code != http.StatusConflict {
		t.Fatalf("duplicate name: %d %v", code, body)
	}

	code, body = do(t, "PUT", item, token, `{"name":"api","type":"tcp","target":"db:5432","enabled":false}`)
	if code != http.StatusOK || body["type"] != "tcp" || body["enabled"] != false {
		t.Fatalf("update: %d %v", code, body)
	}

	code, body = do(t, "GET", base, token, "")
	if code != http.StatusOK || len(body["monitors"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}

	if code, _ = do(t, "DELETE", item, token, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ = do(t, "GET", item, token, ""); code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", code)
	}
}

func TestValidationErrorsAreListed(t *testing.T) {
	srv, _ := newServer(t, token)
	code, body := do(t, "POST", srv.URL+"/api/v1/monitors", token, `{"name":"","type":"http","target":"nope","interval":"1s"}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("code %d", code)
	}
	if details := body["details"].([]any); len(details) < 3 {
		t.Fatalf("expected every problem listed, got %v", details)
	}

	code, _ = do(t, "POST", srv.URL+"/api/v1/monitors", token, `{"name":"x","type":"http","target":"http://a","colour":"red"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown field: code %d", code)
	}
}

func TestAuth(t *testing.T) {
	srv, _ := newServer(t, token)
	if code, _ := do(t, "GET", srv.URL+"/api/v1/monitors", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := do(t, "GET", srv.URL+"/api/v1/monitors", "wrong", ""); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}

	disabled, _ := newServer(t, "")
	if code, _ := do(t, "GET", disabled.URL+"/api/v1/monitors", "", ""); code != http.StatusForbidden {
		t.Fatalf("no admin token configured: %d", code)
	}
	if code, _ := do(t, "GET", disabled.URL+"/healthz", "", ""); code != http.StatusOK {
		t.Fatalf("healthz: %d", code)
	}
	if code, _ := do(t, "GET", disabled.URL+"/readyz", "", ""); code != http.StatusOK {
		t.Fatalf("readyz: %d", code)
	}
}

func TestFileMonitorsAreReadOnly(t *testing.T) {
	srv, st := newServer(t, token)
	specs, err := monitor.ParseFile([]byte("monitors:\n  - {name: web, type: http, target: 'http://web'}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SyncFileMonitors(context.Background(), specs); err != nil {
		t.Fatal(err)
	}
	ms, _ := st.ListMonitors(context.Background())
	item := srv.URL + "/api/v1/monitors/" + jsonNum(float64(ms[0].ID))

	if code, body := do(t, "PUT", item, token, `{"name":"web","type":"http","target":"http://other"}`); code != http.StatusConflict {
		t.Fatalf("update file monitor: %d %v", code, body)
	}
	if code, _ := do(t, "DELETE", item, token, ""); code != http.StatusConflict {
		t.Fatalf("delete file monitor: %d", code)
	}

	// Removing it from the file deletes it; an API monitor with the same
	// name as a file monitor is rejected.
	if _, err := st.SyncFileMonitors(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if code, _ := do(t, "GET", item, token, ""); code != http.StatusNotFound {
		t.Fatalf("file monitor should be gone: %d", code)
	}
	do(t, "POST", srv.URL+"/api/v1/monitors", token, `{"name":"web","type":"tcp","target":"web:80"}`)
	if _, err := st.SyncFileMonitors(context.Background(), specs); err == nil {
		t.Fatal("expected name clash with API monitor")
	}
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
