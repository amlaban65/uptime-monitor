package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/incident"
	"github.com/amlaban65/uptime-monitor/internal/monitor"
	"github.com/amlaban65/uptime-monitor/internal/notify"
)

// RecordResult stores a check result, updates the daily rollup and runs the
// incident state machine, all in one transaction. The monitor_state row is
// locked FOR UPDATE, so concurrent results for the same monitor (which
// should not happen, but could after a misconfiguration or a clock jump)
// are applied one at a time and can never open two incidents.
func (s *Store) RecordResult(ctx context.Context, m monitor.Monitor, r monitor.Result) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var status sql.NullInt32
		if r.StatusCode != 0 {
			status = sql.NullInt32{Int32: int32(r.StatusCode), Valid: true} //nolint:gosec // HTTP status fits
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO check_results (monitor_id, checked_at, ok, latency_us, status_code, error)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			m.ID, r.CheckedAt, r.OK, r.Latency.Microseconds(), status, r.Error); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO daily_stats (monitor_id, day, total, ok)
			VALUES ($1, ($2::timestamptz AT TIME ZONE 'UTC')::date, 1, $3)
			ON CONFLICT (monitor_id, day)
			DO UPDATE SET total = daily_stats.total + 1, ok = daily_stats.ok + EXCLUDED.ok`,
			m.ID, r.CheckedAt, boolToInt(r.OK)); err != nil {
			return err
		}

		st, err := lockState(ctx, tx, m.ID)
		if err != nil {
			return err
		}
		next, tr := incident.Next(st, r.OK, r.CheckedAt, incident.Thresholds{
			Failures:  m.FailureThreshold,
			Successes: m.RecoveryThreshold,
		})
		var streak sql.NullTime
		if !next.StreakStart.IsZero() {
			streak = sql.NullTime{Time: next.StreakStart, Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE monitor_state
			SET status = $2, consecutive_failures = $3, consecutive_successes = $4,
			    streak_started_at = $5, last_checked_at = $6, last_latency_us = $7, last_error = $8
			WHERE monitor_id = $1`,
			m.ID, string(next.Status), next.ConsecutiveFailures, next.ConsecutiveSuccesses,
			streak, r.CheckedAt, r.Latency.Microseconds(), r.Error); err != nil {
			return err
		}

		var incidentID int64
		switch tr {
		case incident.Opened:
			err = tx.QueryRowContext(ctx, `
				INSERT INTO incidents (monitor_id, started_at, cause) VALUES ($1, $2, $3)
				RETURNING id`,
				m.ID, next.StreakStart, r.Error).Scan(&incidentID)
			if err != nil {
				return err
			}
			return s.enqueueAlerts(ctx, tx, incidentID, notify.EventOpened)
		case incident.Resolved:
			err = tx.QueryRowContext(ctx, `
				UPDATE incidents SET resolved_at = $2
				WHERE monitor_id = $1 AND resolved_at IS NULL
				RETURNING id`,
				m.ID, next.StreakStart).Scan(&incidentID)
			if errors.Is(err, sql.ErrNoRows) {
				return nil // nothing open (for example it was deleted); nothing to alert
			}
			if err != nil {
				return err
			}
			return s.enqueueAlerts(ctx, tx, incidentID, notify.EventResolved)
		}
		return nil
	})
}

func lockState(ctx context.Context, tx *sql.Tx, monitorID int64) (incident.State, error) {
	// Create the row on first use so there is always something to lock.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO monitor_state (monitor_id) VALUES ($1) ON CONFLICT DO NOTHING`, monitorID); err != nil {
		return incident.State{}, err
	}
	var (
		st     incident.State
		status string
		streak sql.NullTime
	)
	err := tx.QueryRowContext(ctx, `
		SELECT status, consecutive_failures, consecutive_successes, streak_started_at
		FROM monitor_state WHERE monitor_id = $1 FOR UPDATE`, monitorID).
		Scan(&status, &st.ConsecutiveFailures, &st.ConsecutiveSuccesses, &streak)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	st.Status = incident.Status(status)
	if streak.Valid {
		st.StreakStart = streak.Time
	}
	return st, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Incident is a stored incident.
type Incident struct {
	ID          int64      `json:"id"`
	MonitorID   int64      `json:"monitor_id"`
	MonitorName string     `json:"monitor_name"`
	StartedAt   time.Time  `json:"started_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Cause       string     `json:"cause"`
}

// RecentIncidents returns incidents that are open or started after since,
// newest first.
func (s *Store) RecentIncidents(ctx context.Context, since time.Time, limit int) ([]Incident, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.monitor_id, m.name, i.started_at, i.resolved_at, i.cause
		FROM incidents i JOIN monitors m ON m.id = i.monitor_id
		WHERE i.resolved_at IS NULL OR i.started_at >= $1
		ORDER BY i.started_at DESC
		LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var (
			in       Incident
			resolved sql.NullTime
		)
		if err := rows.Scan(&in.ID, &in.MonitorID, &in.MonitorName, &in.StartedAt, &resolved, &in.Cause); err != nil {
			return nil, err
		}
		if resolved.Valid {
			t := resolved.Time
			in.ResolvedAt = &t
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
