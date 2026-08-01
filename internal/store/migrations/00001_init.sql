-- Phase 0 schema (§5.2): last-known state, audit trail, leader lease.
-- Deliberately minimal, no ORM abstraction beyond what's needed.
--
-- goose (github.com/pressly/goose/v3) tracks which migrations have already
-- run in its own goose_db_version table, so — unlike this repo's earlier
-- hand-rolled Apply — a migration only ever executes once. Every statement
-- is still written idempotent (IF NOT EXISTS / ON CONFLICT DO NOTHING)
-- anyway, as cheap defense in depth.

-- +goose Up

CREATE TABLE IF NOT EXISTS applications (
  name             text NOT NULL,
  target_gcp_project text NOT NULL,
  desired_image    text NOT NULL,
  live_image       text,
  status           text NOT NULL
    CHECK (status IN ('Synced', 'OutOfSync', 'Progressing', 'Degraded', 'Missing', 'Invalid')),
  health           text NOT NULL
    CHECK (health IN ('Healthy', 'Progressing', 'Degraded', 'Missing', 'Invalid')),
  last_reconciled_at timestamptz NOT NULL,
  PRIMARY KEY (name, target_gcp_project)
);

CREATE TABLE IF NOT EXISTS sync_events (
  id            bigserial PRIMARY KEY,
  application   text NOT NULL,
  target_gcp_project text NOT NULL,
  trigger       text NOT NULL CHECK (trigger IN ('auto', 'manual')),
  actor         text,               -- OAuth email for manual; 'runcd-controller' for auto
  from_image    text,
  to_image      text NOT NULL,
  started_at    timestamptz NOT NULL,
  finished_at   timestamptz,
  result        text NOT NULL CHECK (result IN ('in_progress', 'succeeded', 'failed')),
  error         text,
  FOREIGN KEY (application, target_gcp_project) REFERENCES applications(name, target_gcp_project)
);

-- sync history is looked up per (application, target_gcp_project) — the
-- FK columns aren't automatically indexed on the referencing side in
-- Postgres, and this table is append-only/never deleted (§5.2).
CREATE INDEX IF NOT EXISTS sync_events_application_project_idx ON sync_events (application, target_gcp_project, started_at DESC);

-- single-row-per-lease leader election (NFR3) — no external coordination service required
CREATE TABLE IF NOT EXISTS leader_lease (
  id            int PRIMARY KEY DEFAULT 1,
  holder_id     text NOT NULL,
  expires_at    timestamptz NOT NULL
);

-- seed the single lease row, already expired, so the first replica to poll claims it
INSERT INTO leader_lease (id, holder_id, expires_at) VALUES (1, '', 'epoch') ON CONFLICT (id) DO NOTHING;
