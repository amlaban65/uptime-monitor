package store

import (
	"context"
	"fmt"
	"time"
)

// retentionLockKey identifies the retention job's advisory lock.
const retentionLockKey int64 = 0x75707469_6d650003

const pruneBatch = 10_000

// PruneResult reports what a retention run did.
type PruneResult struct {
	Ran           bool // false if another replica held the lock
	Results       int64
	Notifications int64
}

// Prune deletes raw check results older than keep, and delivered or
// abandoned notifications older than 90 days. Daily rollups and incidents
// are kept, so the 90-day bars and incident history survive.
//
// Unlike check scheduling, this is a singleton chore with no natural unit
// of work to share, so it uses pg_try_advisory_lock: whichever replica gets
// the lock does the run, the others skip it. The lock is session-scoped,
// so if the holder crashes Postgres releases it when the connection drops.
//
// Rows are deleted in batches so that no single transaction holds locks on,
// or generates WAL for, millions of rows at once.
func (s *Store) Prune(ctx context.Context, keep time.Duration) (PruneResult, error) {
	var res PruneResult
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer conn.Close()

	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, retentionLockKey).Scan(&got); err != nil {
		return res, err
	}
	if !got {
		return res, nil
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, retentionLockKey) //nolint:errcheck // released on disconnect anyway
	res.Ran = true

	for {
		r, err := conn.ExecContext(ctx, `
			DELETE FROM check_results WHERE id IN (
				SELECT id FROM check_results
				WHERE checked_at < now() - make_interval(secs => $1)
				LIMIT $2)`, keep.Seconds(), pruneBatch)
		if err != nil {
			return res, fmt.Errorf("prune check results: %w", err)
		}
		n, _ := r.RowsAffected()
		res.Results += n
		if n < pruneBatch {
			break
		}
	}
	r, err := conn.ExecContext(ctx, `
		DELETE FROM notifications
		WHERE status IN ('sent', 'failed') AND created_at < now() - interval '90 days'`)
	if err != nil {
		return res, fmt.Errorf("prune notifications: %w", err)
	}
	res.Notifications, _ = r.RowsAffected()
	return res, nil
}
