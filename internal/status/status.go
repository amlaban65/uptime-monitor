// Package status assembles the public status snapshot shown on the status
// page and returned by the status JSON API. Both render the same Snapshot,
// so they can never disagree.
package status

import (
	"context"
	"sync"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/stats"
	"github.com/amlaban65/uptime-monitor/internal/store"
)

const (
	barDays        = 90
	incidentWindow = 30 * 24 * time.Hour
	maxIncidents   = 20
)

// Source is the data the snapshot is built from.
type Source interface {
	MonitorStats(ctx context.Context) ([]store.MonitorStats, error)
	DailyCounts(ctx context.Context, days int) (map[int64][]stats.DayCount, error)
	RecentIncidents(ctx context.Context, since time.Time, limit int) ([]store.Incident, error)
}

// Overall states.
const (
	OverallOperational = "operational"
	OverallPartial     = "partial_outage"
	OverallMajor       = "major_outage"
	OverallUnknown     = "unknown"
)

type Snapshot struct {
	Title       string          `json:"title"`
	GeneratedAt time.Time       `json:"generated_at"`
	Overall     string          `json:"overall"`
	Monitors    []MonitorStatus `json:"monitors"`
	Incidents   []Incident      `json:"incidents"`
}

// MonitorStatus deliberately omits the target and raw error text: the page
// is public and those can reveal internal hostnames.
type MonitorStatus struct {
	ID            int64       `json:"id"`
	Name          string      `json:"name"`
	Status        string      `json:"status"`
	LastCheckedAt *time.Time  `json:"last_checked_at"`
	Uptime24h     *float64    `json:"uptime_24h"`
	Uptime7d      *float64    `json:"uptime_7d"`
	Uptime30d     *float64    `json:"uptime_30d"`
	LatencyP50Ms  *float64    `json:"latency_p50_ms"`
	LatencyP95Ms  *float64    `json:"latency_p95_ms"`
	Bars          []stats.Bar `json:"days"`
}

type Incident struct {
	MonitorName string     `json:"monitor"`
	StartedAt   time.Time  `json:"started_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`
	Ongoing     bool       `json:"ongoing"`
	Duration    string     `json:"duration"`
}

// Service builds snapshots and caches them briefly. The window queries scan
// up to 30 days of results per monitor, and a public page can be hit hard,
// so every replica serves a cached copy for ttl.
type Service struct {
	src   Source
	title string
	ttl   time.Duration
	now   func() time.Time

	mu       sync.Mutex
	cached   Snapshot
	cachedAt time.Time
}

func NewService(src Source, title string, ttl time.Duration) *Service {
	return &Service{src: src, title: title, ttl: ttl, now: time.Now}
}

// Snapshot returns the cached snapshot, rebuilding it when stale. The lock
// is held during the rebuild on purpose: concurrent requests wait for one
// query instead of all hitting the database at once.
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.cachedAt.IsZero() && now.Sub(s.cachedAt) < s.ttl {
		return s.cached, nil
	}
	snap, err := s.build(ctx, now)
	if err != nil {
		return Snapshot{}, err
	}
	s.cached, s.cachedAt = snap, now
	return snap, nil
}

func (s *Service) build(ctx context.Context, now time.Time) (Snapshot, error) {
	rows, err := s.src.MonitorStats(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	daily, err := s.src.DailyCounts(ctx, barDays)
	if err != nil {
		return Snapshot{}, err
	}
	incidents, err := s.src.RecentIncidents(ctx, now.Add(-incidentWindow), maxIncidents)
	if err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{Title: s.title, GeneratedAt: now.UTC(), Monitors: []MonitorStatus{}, Incidents: []Incident{}}
	up, down := 0, 0
	for _, r := range rows {
		ms := MonitorStatus{
			ID: r.ID, Name: r.Name, Status: r.Status, LastCheckedAt: r.LastCheckedAt,
			Uptime24h:    uptime(r.OK24h, r.Total24h),
			Uptime7d:     uptime(r.OK7d, r.Total7d),
			Uptime30d:    uptime(r.OK30d, r.Total30d),
			LatencyP50Ms: millis(r.P50),
			LatencyP95Ms: millis(r.P95),
			Bars:         stats.DailyBars(daily[r.ID], now, barDays),
		}
		switch r.Status {
		case "up":
			up++
		case "down":
			down++
		}
		snap.Monitors = append(snap.Monitors, ms)
	}
	snap.Overall = overall(up, down, len(rows))

	for _, in := range incidents {
		end := now
		if in.ResolvedAt != nil {
			end = *in.ResolvedAt
		}
		snap.Incidents = append(snap.Incidents, Incident{
			MonitorName: in.MonitorName,
			StartedAt:   in.StartedAt.UTC(),
			ResolvedAt:  in.ResolvedAt,
			Ongoing:     in.ResolvedAt == nil,
			Duration:    end.Sub(in.StartedAt).Round(time.Second).String(),
		})
	}
	return snap, nil
}

func overall(up, down, total int) string {
	switch {
	case total == 0 || up+down == 0:
		return OverallUnknown
	case down == 0:
		return OverallOperational
	case up == 0:
		return OverallMajor
	default:
		return OverallPartial
	}
}

func uptime(ok, total int) *float64 {
	if pct, has := stats.Uptime(ok, total); has {
		return &pct
	}
	return nil
}

func millis(d *time.Duration) *float64 {
	if d == nil {
		return nil
	}
	ms := float64(d.Microseconds()) / 1000
	return &ms
}
