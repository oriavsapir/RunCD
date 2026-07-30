// Package store holds the Postgres schema (§5.2): applications, sync_events,
// leader_lease, notification_debounce.
package store

import _ "embed"

//go:embed migrations/0001_init.sql
var migration0001 string

//go:embed migrations/0002_notify.sql
var migration0002 string

// Schema is every migration concatenated in order, applied wholesale by
// tests and (for now) any caller standing up a fresh database — there's no
// migration-runner tool yet, just ordered files applied together.
var Schema = migration0001 + "\n" + migration0002
