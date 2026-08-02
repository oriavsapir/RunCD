-- Persists manifest.ServiceDefinition.ResourceType alongside track/version/
-- repository (migration 00006) so the dashboard can tell a job apart from a
-- service/workerPool without re-fetching the manifest — a job runs to
-- completion and stops, so "Health" for one really means "did the most
-- recent execution succeed," a fundamentally different signal than a
-- continuously-running service's up/down state, and the dashboard needs to
-- know which kind of unit it's showing to label that correctly.

-- +goose Up

ALTER TABLE applications
  ADD COLUMN IF NOT EXISTS resource_type text;
