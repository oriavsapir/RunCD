// Package store holds the Postgres schema (§5.2): applications, sync_events,
// leader_lease.
package store

import _ "embed"

//go:embed migrations/0001_init.sql
var Schema string
