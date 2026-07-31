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

func NewPostgres(t *testing.T) *sql.DB {
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

	if _, err := db.ExecContext(ctx, store.Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}
