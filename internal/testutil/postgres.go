// Package testutil provides a real, throwaway Postgres instance for
// integration tests, per design spec §8 ("real (test-container) Postgres").
// Not imported by any production binary.
package testutil

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" sql.Open driver used below
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/runcd/runcd/internal/store"
)

// NewPostgres starts a real, throwaway, schema-applied Postgres — what
// every test outside this package should use.
func NewPostgres(t *testing.T) *sql.DB {
	t.Helper()
	db := NewRawPostgres(t)
	if err := store.Apply(context.Background(), db); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

// NewRawPostgres starts a real, throwaway Postgres with no schema applied
// yet — for internal/store's own tests, which need to exercise Apply
// itself (idempotency, concurrent-callers race-safety) against a genuinely
// empty database.
func NewRawPostgres(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	pgc, err := postgres.Run(ctx, "postgres:18-alpine",
		postgres.WithDatabase("runcd"),
		postgres.WithUsername("runcd"),
		postgres.WithPassword("runcd"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })

	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
