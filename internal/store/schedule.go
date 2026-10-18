package store

import (
	"context"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
)

// ClaimDue atomically claims up to limit due monitors for this replica and
// returns them.
//
// The claim is the UPDATE that pushes next_run_at forward by one interval
// (plus jitter): once it commits, the monitor is no longer due for anyone
// else. FOR UPDATE SKIP LOCKED makes concurrent replicas skip rows another
// replica is claiming right now, instead of blocking on them and then
// claiming them a second time. The result: each due run goes to exactly one
// replica, and replicas share the work.
//
// If a replica dies after claiming, that one run is skipped and the monitor
// is picked up again next interval. Checks are at-most-once per interval,
// which is the right trade for a monitor: a missed sample is harmless, a
// doubled sample skews uptime.
func (s *Store) ClaimDue(ctx context.Context, limit int) ([]monitor.Monitor, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH due AS (
			SELECT id FROM monitors
			WHERE enabled AND next_run_at <= now()
			ORDER BY next_run_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE monitors m
		SET next_run_at = now() + make_interval(secs => m.interval_ms / 1000.0 * (1 + random() * $2))
		FROM due
		WHERE m.id = due.id
		RETURNING `+monitorColumnsM, limit, s.jitter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []monitor.Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
