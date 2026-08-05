# Sync windows

`sync.syncWindows` (§6, ArgoCD's `AppProject.syncWindows` without a cron
dependency): gates **auto-sync only** — never a manual or forced sync — to
specific days and UTC hours.

- `deny` always wins over `allow`.
- With no `allow` window declared at all, auto-sync is permitted everywhere
  a `deny` window doesn't match.
- `days` is a subset of `Mon`..`Sun`; empty means every day.
- `startHour`/`endHour` are UTC hours in `[0,24]`; equal (including the
  zero value) means "all day". `startHour > endHour` wraps past midnight
  (e.g. `22`/`6` covers 22:00–06:00 UTC).

See [`runcd.yaml`](runcd.yaml) for a prod deny window (never auto-deploy on
Fridays) and a staging allow window (business hours only).
