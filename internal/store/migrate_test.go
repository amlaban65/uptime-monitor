package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/amlaban65/uptime-monitor/internal/store"
	"github.com/amlaban65/uptime-monitor/internal/testdb"
)

func TestMigrateConcurrentReplicas(t *testing.T) {
	dsn := testdb.URL(t)
	ctx := context.Background()

	// Several "replicas" start at once; exactly one should apply each file.
	const replicas = 4
	results := make([][]string, replicas)
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i := range replicas {
		db := testdb.Open(t, dsn)
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = store.Migrate(ctx, db)
		}()
	}
	wg.Wait()

	total := 0
	for i := range replicas {
		if errs[i] != nil {
			t.Fatalf("replica %d: %v", i, errs[i])
		}
		total += len(results[i])
	}
	db := testdb.Open(t, dsn)
	var recorded int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if total != recorded || recorded == 0 {
		t.Fatalf("applied %d migrations across replicas, %d recorded", total, recorded)
	}

	// Running again is a no-op.
	again, err := store.Migrate(ctx, db)
	if err != nil || len(again) != 0 {
		t.Fatalf("second run applied %v, err %v", again, err)
	}
}
