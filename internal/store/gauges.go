package store

import "context"

// Gauges is current state for the metrics endpoint.
type Gauges struct {
	MonitorStatus map[string]string // enabled monitors by name
	OpenIncidents int
	PendingAlerts int
	FailedAlerts  int
}

func (s *Store) Gauges(ctx context.Context) (Gauges, error) {
	g := Gauges{MonitorStatus: map[string]string{}}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.name, coalesce(st.status, 'unknown')
		FROM monitors m LEFT JOIN monitor_state st ON st.monitor_id = m.id
		WHERE m.enabled`)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, status string
		if err := rows.Scan(&name, &status); err != nil {
			return g, err
		}
		g.MonitorStatus[name] = status
	}
	if err := rows.Err(); err != nil {
		return g, err
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM incidents WHERE resolved_at IS NULL),
		       (SELECT count(*) FROM notifications WHERE status = 'pending'),
		       (SELECT count(*) FROM notifications WHERE status = 'failed')`).
		Scan(&g.OpenIncidents, &g.PendingAlerts, &g.FailedAlerts)
	return g, err
}
