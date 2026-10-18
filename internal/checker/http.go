package checker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
)

// maxBodyBytes bounds how much of a response is read when matching
// body_contains, so a huge or endless response cannot exhaust memory.
const maxBodyBytes = 1 << 20

// HTTP checks HTTP(S) endpoints.
type HTTP struct {
	client *http.Client
}

// NewHTTP returns an HTTP checker. Keep-alives are disabled so every check
// pays for DNS, TCP and TLS setup like a first-time visitor would; reusing
// pooled connections would hide exactly the failures we want to catch.
func NewHTTP() *HTTP {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 0, // bounded by the per-check context instead
	}
	return &HTTP{client: &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("stopped after %d redirects", len(via))
			}
			return nil
		},
	}}
}

func (h *HTTP) Check(ctx context.Context, m monitor.Monitor) monitor.Result {
	res := monitor.Result{MonitorID: m.ID, CheckedAt: time.Now()}
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, m.Method, m.Target, nil)
	if err != nil {
		res.Error = "invalid request: " + err.Error()
		return res
	}
	req.Header.Set("User-Agent", "uptime-monitor/1.0")

	resp, err := h.client.Do(req)
	if err != nil {
		res.Latency = time.Since(start)
		res.Error = describe(ctx, err)
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode

	var body []byte
	if m.BodyContains != "" {
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	} else {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
	}
	// Latency covers the full response, which is what a user would wait for.
	res.Latency = time.Since(start)
	if err != nil {
		res.Error = "reading body: " + describe(ctx, err)
		return res
	}

	switch {
	case resp.StatusCode != m.ExpectedStatus:
		res.Error = fmt.Sprintf("status %d, expected %d", resp.StatusCode, m.ExpectedStatus)
	case m.BodyContains != "" && !bytes.Contains(body, []byte(m.BodyContains)):
		res.Error = fmt.Sprintf("body does not contain %q", m.BodyContains)
	default:
		res.OK = true
	}
	return res
}
