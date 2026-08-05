-- Widens notification_debounce's rule CHECK constraint for two additions to
-- internal/notify/internal/config: a healthRecovered rule type (no
-- threshold suffix), and named rules (config.NotifyRule.Name) so two
-- rules of the same "on" can be selected independently by
-- environments[].notify.rules — a user-chosen Name can be any
-- alphanumeric/underscore/hyphen string, not just the two fixed
-- "<on>:<threshold>" shapes this constraint previously enumerated.
-- config.Parse restricts Name to the same charset at load time, so this is
-- the actual constraint that matters going forward rather than an
-- enumeration of every rule type.

-- +goose Up

ALTER TABLE notification_debounce
  DROP CONSTRAINT notification_debounce_rule_check;

ALTER TABLE notification_debounce
  ADD CONSTRAINT notification_debounce_rule_check CHECK (rule ~ '^[A-Za-z0-9_-]+(:[0-9]+)?$');
