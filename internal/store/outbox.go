package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/notify"
)

// enqueueAlerts adds one outbox row per alert channel. ON CONFLICT DO
// NOTHING plus the unique key makes this idempotent.
func (s *Store) enqueueAlerts(ctx context.Context, tx *sql.Tx, incidentID int64, event notify.EventKind) error {
	for _, ch := range s.alertChannels {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO notifications (incident_id, event, channel) VALUES ($1, $2, $3)
			ON CONFLICT (incident_id, event, channel) DO NOTHING`,
			incidentID, string(event), ch); err != nil {
			return err
		}
	}
	return nil
}

// ClaimDeliveries leases due deliveries using FOR UPDATE SKIP LOCKED, so
// concurrent dispatchers on different replicas never pick the same row.
// Pushing next_attempt_at forward is the lease; attempts counts this try.
func (s *Store) ClaimDeliveries(ctx context.Context, limit int, lease time.Duration) ([]notify.Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH due AS (
			SELECT id FROM notifications
			WHERE status = 'pending' AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE notifications n
		SET next_attempt_at = now() + make_interval(secs => $2), attempts = n.attempts + 1
		FROM due, incidents i, monitors m
		WHERE n.id = due.id AND i.id = n.incident_id AND m.id = i.monitor_id
		RETURNING n.id, n.channel, n.attempts, n.event, i.id, m.name, m.target, i.started_at, i.resolved_at, i.cause`,
		limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []notify.Delivery
	for rows.Next() {
		var (
			d        notify.Delivery
			event    string
			resolved sql.NullTime
		)
		if err := rows.Scan(&d.ID, &d.Channel, &d.Attempts, &event, &d.Event.IncidentID,
			&d.Event.MonitorName, &d.Event.Target, &d.Event.StartedAt, &resolved, &d.Event.Cause); err != nil {
			return nil, err
		}
		d.Event.Kind = notify.EventKind(event)
		if resolved.Valid {
			t := resolved.Time
			d.Event.ResolvedAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) MarkDelivered(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE notifications SET status = 'sent', sent_at = now(), last_error = '' WHERE id = $1`, id)
	return err
}

func (s *Store) MarkFailed(ctx context.Context, id int64, errMsg string, retryIn time.Duration, final bool) error {
	status := "pending"
	if final {
		status = "failed"
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE notifications
		SET status = $2, last_error = $3, next_attempt_at = now() + make_interval(secs => $4)
		WHERE id = $1`, id, status, truncate(errMsg, 1000), retryIn.Seconds())
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
