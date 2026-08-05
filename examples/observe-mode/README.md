# Observe mode

`sync.observe` (§6): shadow mode.

The reconcile loop still fetches, diffs, and assesses health every tick
exactly as normal — Status/Health reflect real drift from the first tick —
but never deploys, not on auto-sync and not on a manual sync's `force`
either.

Meant for onboarding a project/environment onto runcd gradually: prove the
desired state matches reality (or see exactly where it doesn't) before
granting runcd any authority to actually change anything.

See [`runcd.yaml`](runcd.yaml).
