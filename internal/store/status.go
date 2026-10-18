package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/stats"
)

// MonitorStats is the raw per-monitor data behind the status page.
type MonitorStats struct {
	ID              int64
	Name            string
	Status          string
	LastCheckedAt   *time.Time
	Total24h, OK24h int
	Total7d, OK7d   int
	Total30d, OK30d int
	// Latency percentiles over successful checks in the last 24h. Failed
	// checks are excluded: a timeout is not a latency sample.
	P50, P95 *time.Duration
}

// MonitorStats returns window counts and latency percentiles for every
// enabled monitor. Aggregation happens in Postgres so raw samples never
// leave the database; percentile_disc is the nearest-rank percentile, the
// same definition as stats.Percentile.
func (s *Store) MonitorStats(ctx context.Context) ([]MonitorStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.name, coalesce(st.status, 'unknown'), st.last_checked_at,
		       w.total_24h, w.ok_24h, w.total_7d, w.ok_7d, w.total_30d, w.ok_30d, w.p50, w.p95
		FROM monitors m
		LEFT JOIN monitor_state st ON st.monitor_id = m.id
		CROSS JOIN LATERAL (
			SELECT count(*) FILTER (WHERE r.checked_at > now() - interval '24 hours')          AS total_24h,
			       count(*) FILTER (WHERE r.ok AND r.checked_at > now() - interval '24 hours') AS ok_24h,
			       count(*) FILTER (WHERE r.checked_at > now() - interval '7 days')            AS total_7d,
			       count(*) FILTER (WHERE r.ok AND r.checked_at > now() - interval '7 days')   AS ok_7d,
			       count(*)                                                                    AS total_30d,
			       count(*) FILTER (WHERE r.ok)                                                AS ok_30d,
			       percentile_disc(0.5) WITHIN GROUP (ORDER BY r.latency_us)
			           FILTER (WHERE r.ok AND r.checked_at > now() - interval '24 hours')     AS p50,
			       percentile_disc(0.95) WITHIN GROUP (ORDER BY r.latency_us)
			           FILTER (WHERE r.ok AND r.checked_at > now() - interval '24 hours')     AS p95
			FROM check_results r
			WHERE r.monitor_id = m.id AND r.checked_at > now() - interval '30 days'
		) w
		WHERE m.enabled
		ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonitorStats
	for rows.Next() {
		var (
			ms       MonitorStats
			last     sql.NullTime
			p50, p95 sql.NullInt64
		)
		if err := rows.Scan(&ms.ID, &ms.Name, &ms.Status, &last,
			&ms.Total24h, &ms.OK24h, &ms.Total7d, &ms.OK7d, &ms.Total30d, &ms.OK30d, &p50, &p95); err != nil {
			return nil, err
		}
		if last.Valid {
			t := last.Time
			ms.LastCheckedAt = &t
		}
		ms.P50 = usToDuration(p50)
		ms.P95 = usToDuration(p95)
		out = append(out, ms)
	}
	return out, rows.Err()
}

// DailyCounts returns per-day totals from the rollup for the last days days
// (including today, UTC), keyed by monitor.
func (s *Store) DailyCounts(ctx context.Context, days int) (map[int64][]stats.DayCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT monitor_id, day, total, ok FROM daily_stats
		WHERE day > (now() AT TIME ZONE 'UTC')::date - $1::int`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]stats.DayCount{}
	for rows.Next() {
		var (
			id int64
			c  stats.DayCount
		)
		if err := rows.Scan(&id, &c.Day, &c.Total, &c.OK); err != nil {
			return nil, err
		}
		out[id] = append(out[id], c)
	}
	return out, rows.Err()
}

func usToDuration(v sql.NullInt64) *time.Duration {
	if !v.Valid {
		return nil
	}
	d := time.Duration(v.Int64) * time.Microsecond
	return &d
}
