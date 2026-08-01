-- Phase 3 schema additions (§5.8, §5.9): timestamps for how long a sync
-- unit has held its current status/health (needed by the Notifier's
-- healthDegraded/outOfSyncGated duration rules), and per-(unit, rule)
-- debounce state so a sustained failure doesn't spam Slack.

-- +goose Up

ALTER TABLE applications
  ADD COLUMN IF NOT EXISTS status_since timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS health_since timestamptz NOT NULL DEFAULT now();

-- rule is the notify rule's type, and for threshold-bearing rule types
-- (healthDegraded, outOfSyncGated) also its threshold — e.g.
-- "healthDegraded:5" — so two rules of the same type with different
-- thresholds (an early warning plus an escalation) debounce independently
-- instead of colliding on one row.
CREATE TABLE IF NOT EXISTS notification_debounce (
  application        text NOT NULL,
  target_gcp_project  text NOT NULL,
  rule                text NOT NULL CHECK (rule ~ '^(syncFailed|healthDegraded:[0-9]+|outOfSyncGated:[0-9]+)$'),
  last_notified_at    timestamptz NOT NULL,
  PRIMARY KEY (application, target_gcp_project, rule)
);
