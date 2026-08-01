-- Separates "an attempt is in flight" from "a notification was actually
-- sent" — internal/notify.maybeNotify claims by setting claim_expires_at
-- (a short TTL, like sync_locks), then only sets last_notified_at once
-- Sink.Send actually succeeds. Without this split, a process crash (or a
-- DB error on the best-effort revert) between claiming and sending left a
-- real failure notification silently dropped for up to the full debounce
-- window — a stuck claim now just expires on its own, no dependency on any
-- single follow-up write succeeding.

-- +goose Up

ALTER TABLE notification_debounce
  ADD COLUMN IF NOT EXISTS claim_expires_at timestamptz;
