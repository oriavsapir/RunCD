# RBAC

`rbac.yaml` (§5.9) is a separate file from `runcd.yaml` — lives in the same
GitHub repo/branch (`RUNCD_CONFIG_REPO`), at `RBAC_PATH` (default
`rbac.yaml`). It controls who may trigger a manual sync, and for which
apps/environments/projects; every IAP-authenticated caller already has
read-only access to every dashboard view regardless of this file.

- `role`: `"admin"` or `"syncer"` — both grant the same sync permission
  today; `admin` exists for a future finer-grained split.
- `scope` entries (first match wins): `"*"` (everything),
  `"env:<name>"` (every app in that environment, any project),
  `"app:<app>@<project>"` (one specific app in one specific project), or
  `"folder:<id>"` (every project under that GCP folder).

See [`rbac.yaml`](rbac.yaml).
