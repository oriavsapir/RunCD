# runcd

ArgoCD-equivalent for Google Cloud Run: reconciles declared manifests against
live Cloud Run services/jobs/worker-pools, gated by preconditions and RBAC.
Go, stdlib-first (no web framework), Postgres via `pgx`.

## Commands

Backend (repo root):
```
go build ./...
go test ./... -race -shuffle=on      # tests use testcontainers-go (real ephemeral Postgres, no mocks for DB logic)
gofmt -l .                            # must be empty
go vet ./...
golangci-lint run ./...               # config: .golangci.yml
govulncheck ./...
nilaway ./...
terraform fmt -recursive -check -diff terraform/
```

Dashboard (`web/`):
```
npm run build
npm run lint
npm test          # vitest, component tests only — no E2E per §8
```

CI (`.github/workflows/ci.yml`) runs the backend checks above plus a Docker
build. See `PROGRESS.md` for the authoritative "what's done / what's tested /
what's deliberately deferred" checklist — check it before assuming something
is or isn't built.

## Architecture

`cmd/controller/main.go` — entrypoint. Leader-gates the auto-reconcile loop
(only the leader deploys; a `leadershipContext` cancels in-flight work the
instant leadership is lost), serves the manual-sync + dashboard-read API on
every replica, loads `runcd.yaml`/`rbac.yaml` from GitHub via the App
client. Applies the Postgres schema on boot (`store.Apply`) and hot-reloads
config/RBAC/notify settings on every `RECONCILE_INTERVAL` tick — see Phase 5
below.

`cmd/runcd/main.go` — a small CLI client for the same HTTP API the
dashboard uses (`internal/api`): list units, show a diff, view history,
trigger a sync (or preview one with `--dry-run`), list RBAC roles, list
orphaned services. Independent of the dashboard/controller processes —
just an HTTP client with its own copy of the JSON shapes (same
relationship `web/src/lib/types.ts` has). Auth: shells out to `gcloud auth
print-identity-token` when `RUNCD_IAP_AUDIENCE` is set.

`internal/`, roughly in dependency order:
- `config` — parses `runcd.yaml` (environments, apps, notify rules), validates references. `environments[env].folders` (GCP folder IDs) is parsed here but never resolved here — `config.Parse` does no I/O; `internal/folders.ResolveConfig` merges a folder's child projects into `Projects` before `expander.Expand` ever runs
- `manifest` — per-app manifest format (§5.1), digest-pin validation. `traffic.latestRevisionPercent` only accepts exactly 100 (matching `cloudrun.validatedPercent`'s own restriction — accepting a wider [0,100] range here would parse fine and then fail every deploy attempt forever). `env`/`secrets` are one managed field ("env") covering both plain vars and Secret Manager refs together, since Cloud Run has one unified env var list
- `expander` — expands config into `SyncUnit`s (one per app × environment)
- `folders` — `Resolver` interface (`ProjectsInFolder`) + `GCPResolver`, the real Cloud Resource Manager v3 implementation; `ResolveConfig` merges `environments[env].folders` into `Projects`, `ResolveMembership` builds the folder-ID → project-IDs map `rbac.CanSyncFolders` needs for `"folder:<id>"` scopes — two independent resolution passes over the same underlying API, not a shared cache
- `store` — Postgres schema/migrations (`migrations/*.sql`, embedded via `schema.go`); every migration is idempotent, and `Apply` (advisory-lock-guarded against concurrent replicas) runs the whole thing on every boot — no separate migration step
- `leader` — Postgres-backed leader election (conditional UPDATE lease claim)
- `githubapp` — GitHub App auth (RS256 JWT, stdlib `crypto/rsa`) + Contents API file fetches — no local git clone, keeps the distroless runtime image git-free
- `gitsource` — `reconcile.ManifestSource` on top of `githubapp`, with a short-TTL cache + singleflight coalescing (many sync units often share one manifest)
- `cloudrun` — `AdminClient` interface; `GCPAdminClient` is the real Cloud Run Admin API v2 implementation (client construction coalesced via `singleflight`, using `context.WithoutCancel` so one caller's cancelled context can't spuriously fail concurrent callers)
- `precondition` — `Checker` interface; `GCPChecker` is the real Pub/Sub-backed implementation
- `diff` — computes Synced/OutOfSync status from desired vs. live state. `env` is skipped for `resourceType: job` (see `manifest`'s note); `nil` desired `EnvVars`/`SecretRefs` means "env not managed for this unit," the same nil-means-unmanaged convention `TrafficLatestRevisionPercent` already uses
- `health` — assesses Healthy/Degraded per resource type (service/job/workerPool)
- `reconcile` — the core loop: fetch → diff → precondition-gate (respecting each app's `ignorePreconditions`) → sync-window check (auto-sync only) → deploy → re-fetch → persist → notify. Writes `sync_events` as an audit trail. A per-unit TTL lock (`sync_locks` table) serializes a manual sync against the auto-reconcile loop (or two concurrent manual syncs) deploying the same unit at once — a losing attempt gets `ErrSyncInProgress`. `DryRun` runs the same fetch/diff/health path with deploy short-circuited, no lock taken, nothing persisted. `DetectOrphans` lists live Cloud Run services per project/region a unit still targets and flags any not declared by a current unit there (prune, read-only).
- `rbac` — role/scope matching (`env:x`, `app:x@project`, `folder:x`) for who may trigger a manual sync. `Store` holds both the parsed `*Config` and a separately-swapped folder-membership map (`SetFolderMembership`/`FolderMembership`) — the two are hot-reloaded independently, same looseness `cmd/controller/main.go`'s reconcileLoop already has between config/notify/RBAC reloads. `HasAnyGrant` (used by orphan detection, which has no single unit to scope a check against) requires a rule with a **non-empty** `Scope`, not just a matching `Subject` — a `scope: []` row grants nothing under `CanSync` either, so it must not count here
- `auth` — identity verification. `IAPAuthenticator` (default, wired in `main.go`) verifies Identity-Aware Proxy's signed assertion header, defense-in-depth on top of IAP/IAM actually gating access; `GoogleAuthenticator` (direct Google OAuth token) is kept as an option for non-IAP deployments but not wired by default.
- `notify` — Slack notifications on sync-failed / health-degraded / stuck-out-of-sync, debounced via a Postgres transaction (commits the debounce claim only after the send succeeds)
- `api` — HTTP handlers: `GET /api/units` (list), `GET /api/units/{project}/{app}` (detail/diff), `GET /api/units/{project}/{app}/history` (sync_events), `GET /api/units/{project}/{app}/dry-run` (preview a sync without deploying), `GET /api/rbac` (configured roles), `GET /api/config` (runtime config), `GET /api/orphans` (prune/orphan detection), `POST /api/sync/{project}/{app}` (manual sync), `GET /metrics` (OTel-backed Prometheus exposition). Most read endpoints are open to any authenticated caller; sync, dry-run, and history are RBAC-gated (dry-run makes the same real Cloud Run/Pub-Sub calls a sync does; history's `sync_events.error` column carries raw deploy/DB error text — the same class of detail the sync response itself never echoes, so viewing it needs the same grant as triggering a sync, not just being logged in), same for orphans (`HasAnyGrant`, since it fans out live calls with no single unit to scope a check against). `/metrics` is the one unauthenticated route, matching the controller's no-IAP posture. Go 1.22+ pattern routing. `Handler.Reconciler` is an `*atomic.Pointer[reconcile.Reconciler]`, not a plain pointer — swappable so a config hot-reload doesn't race a concurrent manual sync.

`web/` — the Next.js dashboard (Phase 4). App Router, TypeScript, Tailwind
CSS v4, shadcn/ui + lucide-react. Calls the Go API same-origin
(`credentials: "include"`), relying on both sitting behind the same
IAP-protected perimeter — no auth code of its own in the frontend.

`terraform/controller-sa/` — Terraform module provisioning the shared
controller service account (§5.5). Not invoked directly — see its
`examples/minimal/`, which is what CI actually validates.

## Conventions

- No comments unless they explain a non-obvious WHY (hidden constraint,
  workaround, subtle invariant). Never restate what code already says.
- Interface + fake pattern for anything that would otherwise require live
  GCP/Google API calls in tests (`cloudrun.AdminClient`, `precondition.Checker`,
  `auth.Authenticator`). Real Postgres via testcontainers, not mocks, for
  anything DB-backed.
- `errgroup.Group{}` (plain, not `.WithContext`) in the reconcile loop's
  worker pool — `WithContext` would cancel every other in-flight unit on one
  unit's failure. Don't reintroduce `WithContext` here.
- Any code path that re-checks live state after a deploy must make a genuine
  fresh API call — never reuse a pre-deploy snapshot/closure. (Caught a real
  bug here: see `reconcile.go`'s `fetch` closures.)
- `.golangci.yml` disables `revive`'s exported-doc-comment rule deliberately
  — don't re-enable it to silence findings; add real doc comments only where
  genuinely non-obvious instead.
- UI (`web/`): no emoji, icon libraries only, prefer established libraries
  over custom code, professional/non-AI-looking polish.
- In React data-fetching effects, fetch inline in the effect body (with a
  `cancelled` flag in cleanup) rather than calling a separately-defined
  `load()`-style callback from inside `useEffect` — the latter trips
  `eslint-plugin-react-hooks`'s `set-state-in-effect` rule in this
  Next.js/eslint version.

## Phased build plan

Phases 0–4 are all done (foundations, reconcile loop, job/worker-pool
support, sync+audit trail, gated sync/RBAC/notifications, live GCP/GitHub
wiring, IAP auth, Next.js dashboard, dashboard settings page). Phase 5
(schema migrations applied on boot, full config/RBAC/notify hot-reload, a
real per-unit deploy lock) is also done. Phase 6 (sync windows, dry-run
preview, per-app resource exclusions, an OTel-backed `/metrics` endpoint,
prune/orphan detection) is also done. Phase 7 (GCP folder support in
config + RBAC) and Phase 8 (managed env vars/Secret Manager refs) are
also done — see `PROGRESS.md` for what's still explicitly deferred (a
handful of smaller flagged-not-fixed items from the review passes, plus
deploy-time steps like actually enabling IAP/IAM DB
auth on the real infra, which aren't code in this repo).
