package store

import (
	"context"
	"database/sql"

	"github.com/amlaban65/uptime-monitor/internal/monitor"
)

// ClaimDue returns up to limit due monitors and pushes their next_run_at
// forward by one interval plus random jitter.
func (s *Store) ClaimDue(ctx context.Context, limit int) ([]monitor.Monitor, error) {
	var out []monitor.Monitor
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT `+monitorColumns+` FROM monitors
			WHERE enabled AND next_run_at <= now()
			ORDER BY next_run_at
			LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			m, err := scanMonitor(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
			ids = append(ids, m.ID)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE monitors
			SET next_run_at = now() + make_interval(secs => interval_ms / 1000.0 * (1 + random() * $2))
			WHERE id = ANY($1)`, ids, s.jitter)
		return err
	})
	return out, err
}
