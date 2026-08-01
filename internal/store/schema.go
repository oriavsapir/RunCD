// Package store holds the Postgres schema (§5.2): applications, sync_events,
// leader_lease, notification_debounce, sync_locks.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"time"
)

//go:embed migrations/0001_init.sql
var migration0001 string

//go:embed migrations/0002_notify.sql
var migration0002 string

//go:embed migrations/0003_sync_lock.sql
var migration0003 string

//go:embed migrations/0004_metrics_index.sql
var migration0004 string

// Schema is every migration except 0004 concatenated in order. Every
// statement in it is idempotent (IF NOT EXISTS / ON CONFLICT DO NOTHING —
// see the migration files themselves), so applying it wholesale is safe on
// a fresh database or a database that already has it.
//
// migration0004 (CREATE INDEX CONCURRENTLY) is applied separately, by
// applyIndexMigration below — CONCURRENTLY can't run inside a transaction
// block, and the simple-query protocol used to run this multi-statement
// blob executes it as exactly that (implicit transaction wrapping every
// ;-separated statement in one Query message).
//
// gosec's G202 flags this as "SQL string concatenation" — its heuristic
// can't tell this apart from building a query out of untrusted input.
// Every operand here is a //go:embed'd literal fixed at compile time, not
// runtime data.
var Schema = migration0001 + "\n" + migration0002 + "\n" + migration0003 //nolint:gosec

// schemaLockKey is an arbitrary constant advisory-lock key, unique only
// within this app's own lock-key space (nothing else in runcd takes an
// advisory lock). It exists solely to serialize concurrent Apply calls.
const schemaLockKey = 84031

// indexMigrationLockKey serializes concurrent migration0004 runs — a
// distinct key from schemaLockKey, not shared with it (see Apply's comment
// on why the two must never both be held via a *blocking* lock call at the
// same time).
const indexMigrationLockKey = 84032

// indexMigrationPollInterval is how often a replica that lost the race for
// indexMigrationLockKey checks again.
const indexMigrationPollInterval = 200 * time.Millisecond

// Apply creates/updates every table this schema needs, idempotently — safe
// to call on every controller boot, including against a database that
// already has some or all of it (from a previous Apply, or from Schema
// having been run by hand before Apply existed).
//
// IF NOT EXISTS / ADD COLUMN IF NOT EXISTS are not race-safe in Postgres on
// their own: two replicas booting at the same moment and both running
// Schema concurrently can hit "duplicate key value violates unique
// constraint pg_type_typname_nsp_index" instead of one silently no-opping.
// The advisory lock below serializes them — pinned to a single *sql.Conn,
// since pg_advisory_lock/unlock are session-scoped (releasing on a
// different connection would be a no-op, and the lock would outlive this
// call on the pool's connection until that connection happens to close).
func Apply(ctx context.Context, database *sql.DB) error {
	if err := applySchema(ctx, database); err != nil {
		return err
	}
	return applyIndexMigration(ctx, database)
}

// applyIndexMigration runs migration0004 (CREATE INDEX CONCURRENTLY),
// deliberately separate from applySchema's transactional blob — CONCURRENTLY
// can't run inside a transaction block.
//
// Two concurrent CREATE INDEX CONCURRENTLY builds on the same table can
// genuinely deadlock in Postgres if either session is ever left *blocked
// mid-statement* while the other's build is in flight (each build must wait
// for every other session's current transaction to finish, including one
// that's merely sitting blocked on an unrelated statement) — which is
// exactly what a blocking pg_advisory_lock call would do here. So this uses
// pg_try_advisory_lock in a poll loop instead: a replica that loses the race
// gets an immediate false back (no lingering open statement for the other
// replica's build to wait on) and just retries after a short sleep.
func applyIndexMigration(ctx context.Context, database *sql.DB) error {
	conn, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	for {
		var acquired bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", indexMigrationLockKey).Scan(&acquired); err != nil {
			return err
		}
		if acquired {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(indexMigrationPollInterval):
		}
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", indexMigrationLockKey)
	}()

	_, err = conn.ExecContext(ctx, migration0004)
	return err
}

func applySchema(ctx context.Context, database *sql.DB) error {
	conn, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", schemaLockKey); err != nil {
		return err
	}
	// Best-effort unlock on a best-effort background context: if Schema
	// itself failed, ctx may already be near its deadline, and either way
	// the lock is released the moment this pooled connection is closed
	// (deferred above) even if this explicit unlock is skipped.
	defer func() { _, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", schemaLockKey) }()

	// Argument-free on purpose: pgx only falls back to the simple query
	// protocol (which supports the multiple ;-separated statements in
	// Schema) when there are zero arguments to bind — see
	// internal/testutil/postgres.go, which relies on the same thing.
	_, err = conn.ExecContext(ctx, Schema)
	return err
}
