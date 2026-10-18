package api

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"

	"github.com/amlaban65/uptime-monitor/internal/status"
)

//go:embed templates/*.html
var templateFS embed.FS

// StatusSource provides the public status snapshot.
type StatusSource interface {
	Snapshot(ctx context.Context) (status.Snapshot, error)
}

var statusTemplate = template.Must(template.New("status.html").Funcs(template.FuncMap{
	"pct": func(v *float64) string {
		if v == nil {
			return "no data"
		}
		return fmt.Sprintf("%.2f%%", *v)
	},
	"ms": func(v *float64) string {
		if v == nil {
			return "n/a"
		}
		return fmt.Sprintf("%.0f ms", *v)
	},
	"statusText": func(s string) string {
		switch s {
		case "up":
			return "Operational"
		case "down":
			return "Down"
		default:
			return "No data"
		}
	},
	"overallText": func(s string) string {
		switch s {
		case status.OverallOperational:
			return "All systems operational"
		case status.OverallPartial:
			return "Partial outage"
		case status.OverallMajor:
			return "Major outage"
		default:
			return "Status unknown"
		}
	},
}).ParseFS(templateFS, "templates/status.html"))

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request) {
	snap, err := s.status.Snapshot(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	// Render into a buffer so a template error produces a clean 500
	// instead of half a page.
	var buf bytes.Buffer
	if err := statusTemplate.Execute(&buf, snap); err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=15")
	_, _ = buf.WriteTo(w)
}

func (s *Server) statusJSON(w http.ResponseWriter, r *http.Request) {
	snap, err := s.status.Snapshot(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	writeJSON(w, http.StatusOK, snap)
}
