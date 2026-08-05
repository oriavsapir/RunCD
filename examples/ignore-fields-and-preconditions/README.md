# Ignore fields and preconditions

`apps[].ignoreFields` (§7): subtracts from `defaults.managedFields` for
this app only (ArgoCD's `resource.exclusions`, at field granularity) — e.g.
a service whose traffic split is managed by hand outside runcd, so runcd
should never touch it even though traffic is generally managed elsewhere.

`apps[].ignorePreconditions` (§5.10): skips specific `requires` entries
from the app's manifest, each named `"type:name"` (matching
`manifest.Precondition`, e.g. `"pubsubTopic:orders-events"`) — an escape
hatch for a precondition that's legitimately not applicable to one app,
not a way to routinely bypass the gate.

See [`runcd.yaml`](runcd.yaml).
