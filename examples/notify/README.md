# Notify

`notify` (§5.8): the Slack notification rule engine, routed per
environment — RunCD's take on ArgoCD's named notification services +
per-Application subscription, expressed as static config since there's no
CR here to annotate.

- **`notify.slack`** — named webhook sinks, not one URL. `"default"` is
  required once `notify.rules` is non-empty, and is what any environment
  with no `notify.slack` override uses.
- **`notify.rules`** — every configured rule; an environment's
  `notify.rules` narrows to a subset (e.g. prod subscribes to everything,
  dev only wants `syncFailed`). A rule needs `name` only when it would
  otherwise be ambiguous to that kind of reference — two unnamed rules
  sharing an `on` (e.g. a 15-minute early warning and a 60-minute
  escalation) still debounce independently either way.
- **Rule types**: `syncFailed`, `healthDegraded` (needs `forMinutes`),
  `outOfSyncGated` (needs `forHours`), and `healthRecovered` — which fires
  once Health leaves Degraded, but only for a sibling `healthDegraded` rule
  that actually notified last time (tracked via `notification_debounce`
  state), clearing that rule's debounce marker so the next Degraded
  episode notifies immediately rather than waiting out the original
  window.
- **`environments[env].notify`** — per-environment override: `slack`
  (which named sink) and/or `rules` (which subset).

See [`runcd.yaml`](runcd.yaml).
