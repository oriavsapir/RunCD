-- Phase 0 schema (§5.2): last-known state, audit trail, leader lease.
-- Deliberately minimal, no ORM abstraction beyond what's needed.

CREATE TABLE applications (
  name             text NOT NULL,
  target_gcp_project text NOT NULL,
  desired_image    text NOT NULL,
  live_image       text,
  status           text NOT NULL,   -- Synced | OutOfSync | Progressing | Degraded | Missing | Invalid
  health           text NOT NULL,
  last_reconciled_at timestamptz NOT NULL,
  PRIMARY KEY (name, target_gcp_project)
);

CREATE TABLE sync_events (
  id            bigserial PRIMARY KEY,
  application   text NOT NULL,
  target_gcp_project text NOT NULL,
  trigger       text NOT NULL,      -- auto | manual
  actor         text,               -- OAuth email for manual; 'argorun-controller' for auto
  from_image    text,
  to_image      text NOT NULL,
  started_at    timestamptz NOT NULL,
  finished_at   timestamptz,
  result        text NOT NULL,      -- in_progress | succeeded | failed
  error         text,
  FOREIGN KEY (application, target_gcp_project) REFERENCES applications(name, target_gcp_project)
);

-- single-row-per-lease leader election (NFR3) — no external coordination service required
CREATE TABLE leader_lease (
  id            int PRIMARY KEY DEFAULT 1,
  holder_id     text NOT NULL,
  expires_at    timestamptz NOT NULL
);

-- seed the single lease row, already expired, so the first replica to poll claims it
INSERT INTO leader_lease (id, holder_id, expires_at) VALUES (1, '', 'epoch');
