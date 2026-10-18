// Package api is the HTTP layer: the management API for monitors, health
// endpoints, and (in other files) the public status page and metrics.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/store"
)

// Store is the persistence the API needs.
type Store interface {
	ListMonitors(ctx context.Context) ([]monitor.Monitor, error)
	GetMonitor(ctx context.Context, id int64) (monitor.Monitor, error)
	CreateMonitor(ctx context.Context, spec monitor.Spec) (monitor.Monitor, error)
	UpdateMonitor(ctx context.Context, id int64, spec monitor.Spec) (monitor.Monitor, error)
	DeleteMonitor(ctx context.Context, id int64) error
	Ping(ctx context.Context) error
}

// Deps are the server's collaborators.
type Deps struct {
	Store      Store
	Status     StatusSource
	AdminToken string       // empty disables the management API
	Metrics    http.Handler // optional Prometheus handler
	Log        *slog.Logger
}

type Server struct {
	store      Store
	status     StatusSource
	adminToken string
	metrics    http.Handler
	log        *slog.Logger
}

func New(d Deps) *Server {
	return &Server{store: d.Store, status: d.Status, adminToken: d.AdminToken, metrics: d.Metrics, log: d.Log}
}

// Handler returns the routed HTTP handler. Go 1.22's ServeMux supports
// method and path-parameter patterns, which is all this API needs.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)

	if s.metrics != nil {
		mux.Handle("GET /metrics", s.metrics)
	}

	mux.HandleFunc("GET /{$}", s.statusPage)
	mux.HandleFunc("GET /api/v1/status", s.statusJSON)

	mux.Handle("GET /api/v1/monitors", s.admin(s.listMonitors))
	mux.Handle("POST /api/v1/monitors", s.admin(s.createMonitor))
	mux.Handle("GET /api/v1/monitors/{id}", s.admin(s.getMonitor))
	mux.Handle("PUT /api/v1/monitors/{id}", s.admin(s.updateMonitor))
	mux.Handle("DELETE /api/v1/monitors/{id}", s.admin(s.deleteMonitor))

	return s.logRequests(recoverPanics(mux, s.log))
}

// healthz reports that the process is alive (liveness).
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz reports whether the process can serve traffic (readiness).
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) listMonitors(w http.ResponseWriter, r *http.Request) {
	ms, err := s.store.ListMonitors(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	if ms == nil {
		ms = []monitor.Monitor{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"monitors": ms})
}

func (s *Server) getMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	m, err := s.store.GetMonitor(r.Context(), id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) createMonitor(w http.ResponseWriter, r *http.Request) {
	spec, ok := decodeSpec(w, r)
	if !ok {
		return
	}
	m, err := s.store.CreateMonitor(r.Context(), spec)
	if err != nil {
		s.storeError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/monitors/"+strconv.FormatInt(m.ID, 10))
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) updateMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	spec, ok := decodeSpec(w, r)
	if !ok {
		return
	}
	m, err := s.store.UpdateMonitor(r.Context(), id, spec)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteMonitor(r.Context(), id); err != nil {
		s.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// admin requires the bearer token. With no token configured the management
// API is disabled rather than open.
func (s *Server) admin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken == "" {
			writeError(w, http.StatusForbidden, "management API is disabled: set ADMIN_TOKEN")
			return
		}
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || subtle.ConstantTimeCompare([]byte(token), []byte(s.adminToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="uptime-monitor"`)
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next(w, r)
	})
}

func decodeSpec(w http.ResponseWriter, r *http.Request) (monitor.Spec, bool) {
	var spec monitor.Spec
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return spec, false
	}
	spec.Normalize()
	if err := spec.Validate(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":   "validation failed",
			"details": strings.Split(err.Error(), "\n"),
		})
		return spec, false
	}
	return spec, true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid monitor id")
		return 0, false
	}
	return id, true
}

func (s *Server) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "monitor not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrReadOnly):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.internalError(w, err)
	}
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
