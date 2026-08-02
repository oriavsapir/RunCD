package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/runcd/runcd/internal/store"
	"github.com/runcd/runcd/internal/testutil"
)

// TestApply_IdempotentOnAlreadyAppliedSchema checks that a second Apply
// against a database that already has the schema (e.g. a controller
// restart, or Apply having been run by hand before) doesn't error — every
// statement in Schema must be safe to re-run.
func TestApply_IdempotentOnAlreadyAppliedSchema(t *testing.T) {
	db := testutil.NewPostgres(t)
	if err := store.Apply(context.Background(), db); err != nil {
		t.Fatalf("second Apply failed: %v", err)
	}
}

// TestApply_ConcurrentCallersBothSucceed checks that two replicas booting
// at the same moment and both calling Apply against a genuinely empty
// database don't race each other's DDL — without the advisory lock in
// store.Apply, this reliably fails with a Postgres catalog-uniqueness
// error instead of both succeeding.
func TestApply_ConcurrentCallersBothSucceed(t *testing.T) {
	db := testutil.NewRawPostgres(t)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = store.Apply(context.Background(), db)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Apply #%d failed: %v", i, err)
		}
	}
}
