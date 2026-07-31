-- Per-unit deploy lock (§5.3/§5.9): a manual sync (any replica) and the
-- leader's auto-reconcile pass can otherwise race to deploy the same unit
-- concurrently. TTL-based (not session-held) so a crashed holder can't
-- deadlock future syncs — see internal/reconcile's lock acquire/release.
CREATE TABLE IF NOT EXISTS sync_locks (
  application         text NOT NULL,
  target_gcp_project  text NOT NULL,
  holder              text NOT NULL,
  expires_at          timestamptz NOT NULL,
  PRIMARY KEY (application, target_gcp_project)
);
