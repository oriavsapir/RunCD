// Package store holds the Postgres schema (§5.2): applications, sync_events,
// leader_lease, notification_debounce, sync_locks.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsDir is migrationsFS rooted at the migrations directory itself —
// goose.NewProvider expects migration files directly at fsys's root, not
// nested under a subdirectory.
var migrationsDir = func() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err) // migrations/ is embedded above; this can't fail
	}
	return sub
}()

// Apply runs every pending migration in migrations/, idempotently — safe to
// call on every controller boot. goose tracks which migrations have already
// run in its own goose_db_version table (created on first Apply), so a
// migration only ever executes once, unlike this repo's earlier hand-rolled
// "re-run the whole idempotent blob every boot" approach.
//
// WithSessionLocker serializes concurrent Apply calls (e.g. two replicas
// booting at the same moment) via a Postgres session-level advisory lock,
// internally acquired with pg_try_advisory_lock in a retry loop rather than
// a blocking pg_advisory_lock call — the same pattern this package used to
// hand-roll to avoid a real deadlock class where a migration using
// CREATE INDEX CONCURRENTLY (see migrations/00004_metrics_index.sql, which
// also needs goose's "NO TRANSACTION" annotation since CONCURRENTLY can't
// run inside a transaction block) would otherwise wait on another replica's
// session that's itself blocked trying to acquire the lock.
func Apply(ctx context.Context, database *sql.DB) error {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("create goose session locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrationsDir, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	_, err = provider.Up(ctx)
	return err
}
