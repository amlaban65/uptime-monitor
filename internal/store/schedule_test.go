package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/amlaban65/uptime-monitor/internal/store"
	"github.com/amlaban65/uptime-monitor/internal/testdb"
)

// TestClaimDueAcrossReplicas is the core multi-replica guarantee: replicas
// racing for the same due monitors each get a disjoint set, and every due
// monitor is claimed exactly once per interval.
func TestClaimDueAcrossReplicas(t *testing.T) {
	dsn := testdb.MigratedURL(t)
	db := testdb.Open(t, dsn)
	ctx := context.Background()
	const monitors, replicas = 60, 4
	for i := range monitors {
		insertMonitor(t, db, fmt.Sprintf("m%02d", i), 3, 2)
	}

	var (
		mu     sync.Mutex
		claims = map[int64]int{}
		wg     sync.WaitGroup
		start  = make(chan struct{})
	)
	for range replicas {
		s := store.New(testdb.Open(t, dsn), store.WithJitter(0.1))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 40 {
				got, err := s.ClaimDue(ctx, 3)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				for _, m := range got {
					claims[m.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(claims) != monitors {
		t.Fatalf("%d of %d monitors claimed", len(claims), monitors)
	}
	for id, n := range claims {
		if n != 1 {
			t.Errorf("monitor %d claimed %d times", id, n)
		}
	}
	assertRescheduled(t, db, 60*time.Second, 66*time.Second)
}

// assertRescheduled checks every next_run_at moved forward by between lo
// and hi (interval plus jitter), give or take test runtime.
func assertRescheduled(t *testing.T, db *sql.DB, lo, hi time.Duration) {
	t.Helper()
	var minS, maxS float64
	if err := db.QueryRowContext(context.Background(), `
		SELECT min(extract(epoch FROM next_run_at - now())), max(extract(epoch FROM next_run_at - now()))
		FROM monitors`).Scan(&minS, &maxS); err != nil {
		t.Fatal(err)
	}
	slack := 5 * time.Second
	if time.Duration(minS*float64(time.Second)) < lo-slack || time.Duration(maxS*float64(time.Second)) > hi {
		t.Fatalf("next_run_at offsets %.1fs..%.1fs, want within %s..%s", minS, maxS, lo, hi)
	}
}

func TestClaimDueSkipsDisabledAndNotDue(t *testing.T) {
	db := testdb.New(t)
	s := store.New(db)
	ctx := context.Background()
	due := insertMonitor(t, db, "due", 3, 2)
	disabled := insertMonitor(t, db, "disabled", 3, 2)
	later := insertMonitor(t, db, "later", 3, 2)
	if _, err := db.ExecContext(ctx, `UPDATE monitors SET enabled = false WHERE id = $1`, disabled.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE monitors SET next_run_at = now() + interval '1 minute' WHERE id = $1`, later.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimDue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != due.ID || got[0].Interval.Std() != time.Minute {
		t.Fatalf("claimed %+v", got)
	}
}
