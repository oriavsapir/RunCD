// Package store holds the Postgres schema (§5.2): applications, sync_events,
// leader_lease, notification_debounce, sync_locks.
package store

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed migrations/0001_init.sql
var migration0001 string

//go:embed migrations/0002_notify.sql
var migration0002 string

//go:embed migrations/0003_sync_lock.sql
var migration0003 string

// Schema is every migration concatenated in order. Every statement in it is
// idempotent (IF NOT EXISTS / ON CONFLICT DO NOTHING — see the migration
// files themselves), so applying it wholesale is safe on a fresh database
// or a database that already has it.
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
