# Examples

One folder per feature — each has a `README.md` explaining it and a
`runcd.yaml` (or `rbac.yaml`) demonstrating it, self-contained and
parseable on its own — plus [`full/`](full/), which composes all of them
into one realistic repo layout. Enforced by `internal/config`'s
`TestExamplesParse`/`TestExamplesExpand`, `internal/rbac`'s
`TestExamplesParse`, and `internal/manifest`'s `TestExamplesParse` — a
schema change that breaks one of these fails CI, not just this directory
going stale silently.

| Folder | Feature |
| --- | --- |
| [`basic/`](basic/) | Minimal shape: one app, one environment |
| [`folders/`](folders/) | `environments[env].folders` — GCP folder-based project discovery |
| [`region-and-image-overrides/`](region-and-image-overrides/) | `apps[].overrides` — per-project region/track/version |
| [`exclude/`](exclude/) | `apps[].exclude` — drop a project out of an app's expansion |
| [`ignore-fields-and-preconditions/`](ignore-fields-and-preconditions/) | `apps[].ignoreFields` / `apps[].ignorePreconditions` |
| [`sync-windows/`](sync-windows/) | `sync.syncWindows` — allow/deny auto-sync by day/hour |
| [`observe-mode/`](observe-mode/) | `sync.observe` — shadow mode for onboarding |
| [`notify/`](notify/) | `notify` — Slack routing per environment, `healthRecovered` |
| [`rbac/`](rbac/) | The separate RBAC file format (`internal/rbac`), not `runcd.yaml` itself |
| [`full/`](full/) | Everything above, composed into one realistic repo (`runcd.yaml` + `rbac.yaml` + `services/*/service.yaml`) |

See `internal/config` for the full field list — the single-feature
examples show each concept in isolation; `full/` shows how they interact.

**Changing a config feature?** Update the matching example (and its
README) in the same change, and run `go test ./internal/config/...
./internal/rbac/... ./internal/manifest/...` —
`TestExamplesParse`/`TestExamplesExpand` will fail if an example no longer
parses or expands under the new schema.
