-- Persists manifest.Image.Track/Version/Repository alongside desired_image
-- so the dashboard can show what an app is actually tracking (internal
-- imageupdater's git-write-back target), not just its resolved digest —
-- none of the three are ever compared against live state, so unlike
-- desired_image/live_image they carry no CHECK constraint of their own.

-- +goose Up

ALTER TABLE applications
  ADD COLUMN IF NOT EXISTS track text,
  ADD COLUMN IF NOT EXISTS version text,
  ADD COLUMN IF NOT EXISTS repository text;
