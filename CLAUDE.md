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
- `config` — parses `runcd.yaml` (environments, apps, notify rules), validates references. `environments[env].folders` (GCP folder IDs) is parsed here but never resolved here — `config.Parse` does no I/O; `internal/folders.ResolveConfig` merges a folder's child projects into `Projects` before `expander.Expand` ever runs. App names only need to be unique per-project, not globally — every Postgres table/API route/lookup map downstream keys on `(app, project)`, never `app` alone, so the same app name reused across different environments/projects (a normal pattern) is allowed; only a real `(app, project)` collision is rejected, and `expander.Expand` re-checks this itself since `Parse` only sees each environment's explicitly-declared `Projects` (no I/O) and can't see a folder-resolved collision. `sync.retry`/`sync.selfHeal` are rejected if set (parsed but never consumed anywhere in `reconcile` — silent no-ops that looked load-bearing); `sync.interval` is accepted but documented as informational-only
- `manifest` — per-app manifest format (§5.1), digest-pin validation. `traffic.latestRevisionPercent` only accepts exactly 100 (matching `cloudrun.validatedPercent`'s own restriction — accepting a wider [0,100] range here would parse fine and then fail every deploy attempt forever). `env`/`secrets` are one managed field ("env") covering both plain vars and Secret Manager refs together, since Cloud Run has one unified env var list. `image.track`/`image.version` are resolver metadata for the optional `imageupdater` add-on below — `image.repository` is required alongside either (there's nothing else in the manifest identifying which Artifact Registry image to resolve against, since `image.digest` is always validated as a bare digest, never a full reference) — it must be the same registry/project/repo prefix every environment this app targets already deploys from, since deploy time (`cloudrun.withDigest`) splices the resolved digest onto whatever image prefix is already live on each target service and never reads `image.repository` itself; a mismatch resolves and commits a digest cleanly but then fails every deploy attempt forever, visible only in that unit's `sync_events.error`
- `expander` — expands config into `SyncUnit`s (one per app × environment)
- `folders` — `Resolver` interface (`ProjectsInFolder`) + `GCPResolver`, the real Cloud Resource Manager v3 implementation; `ResolveConfig` merges `environments[env].folders` into `Projects`, `ResolveMembership` builds the folder-ID → project-IDs map `rbac.CanSyncFolders` needs for `"folder:<id>"` scopes — two independent resolution passes over the same underlying API, `GCPResolver`'s own cache TTL (60s, comfortably longer than the default `RECONCILE_INTERVAL`) absorbs the second call within a tick rather than a shared cache between the two functions. Both fan out with bounded concurrency (`resolveAll`, matching `reconcile.RunOnce`/`DetectOrphans`'s own pattern) instead of resolving folders one at a time. On a fetch error, `GCPResolver` serves the last cached membership (logged, not silent) rather than collapsing to "zero projects," which would otherwise make every project previously known to be in that folder vanish for a tick and get flagged as orphaned
- `store` — Postgres schema/migrations (`migrations/*.sql`, embedded via `schema.go`). `applications` also carries `track`/`version`/`repository` (mirroring `manifest.Image`'s identically-named fields, set once per reconcile pass and surfaced by `internal/api` for the dashboard — see `imageupdater` above) — legitimately empty for most units, so unlike `desired_image` an empty value here is never itself treated as "the fetch failed," that's still keyed off `desired_image`'s own emptiness. Applied on boot via `goose` (github.com/pressly/goose/v3): goose tracks applied versions in its own `goose_db_version` table, so each migration runs exactly once, and its Postgres session locker (`pg_try_advisory_lock` in a retry loop, not a blocking call) serializes concurrent replicas booting at once without the deadlock risk a blocking lock would have against a `CREATE INDEX CONCURRENTLY` migration (see `migrations/00004_metrics_index.sql`'s `NO TRANSACTION` annotation) — no hand-rolled locking/migration-tracking code needed
- `leader` — Postgres-backed leader election (conditional UPDATE lease claim). `holderID` is a random ID generated once at process startup, not `$HOSTNAME`/`os.Hostname()` — both resolve to the literal string `"localhost"` on Cloud Run (unlike Kubernetes, where `HOSTNAME` is the pod name), which made two genuinely different replicas indistinguishable to the lease claim once the service ever scaled past one instance, causing real leadership flapping in production. `Lease` gets its own small, dedicated `*sql.DB` connection pool in `cmd/controller/main.go`, separate from the pool reconcile/API traffic shares — leader election is latency-sensitive (one `UPDATE` every `RenewInterval`), and sharing a pool meant heavy reconcile load could starve `Claim()` of a connection until it timed out, which itself triggered more flapping (every flap cancels every in-flight reconcile pass, which then retries, adding more load)
- `githubapp` — GitHub App auth (RS256 JWT, stdlib `crypto/rsa`) + Contents API file fetches (`GetFile`/`GetFileWithSHA`) and writes (`PutFile`, used only by `imageupdater`) — no local git clone, keeps the distroless runtime image git-free. The GitHub App needs Contents:write granted (beyond the read-only permission every other use of this client requires) for `PutFile` to actually succeed — until then it just fails per-manifest, logged, same fail-safe posture as any other unconfigured add-on
- `gitsource` — `reconcile.ManifestSource` on top of `githubapp`, with a short-TTL cache + singleflight coalescing (many sync units often share one manifest). The cache map is keyed on a `(repo, path)` struct, not a concatenated string — repo values are SSH URLs that already contain `@` (`git@github.com:org/repo.git`), so a naive `repo+"@"+path` join is ambiguous; singleflight's own string-key requirement joins with `"\x00"` instead (not a valid byte in either a repo URL or a file path)
- `imageupdater` — optional add-on, the git-write-back half of an `argocd-image-updater` equivalent: resolves `image.track`/`image.version` against Artifact Registry (`Resolver` interface + `GCPResolver`, same interface+fake pattern as `cloudrun`/`precondition`) and, when the resolved digest differs from what's committed, rewrites *only* the `digest:` line in `service.yaml` and commits straight to the manifest repo's default branch (`githubapp.PutFile`, guarded by the blob SHA `GetFileWithSHA` returned, so a concurrent edit 409s instead of silently clobbering). Deliberately never unmarshals-then-remarshals the manifest — `yaml.Marshal` would reorder keys/drop comments/restyle quoting, turning a one-line digest bump into a whole-file diff — and re-parses the rewritten bytes as a guard that the regex hit the right line. `version` constraints (`"1"`, `"1.2"`, `"1.2.3"`) are resolved via `golang.org/x/mod/semver` by picking the highest matching tag; tags that aren't valid semver (`latest`, `main-abc123`) are silently skipped rather than erroring, since a real image repo mixes both kinds. Fully inert with no env var to flip: a manifest with only `digest` set is untouched, and nothing calls this at all unless some app's `service.yaml` sets `track`/`version` (which itself requires `image.repository`, enforced by `manifest.Parse`). Runs leader-gated inside `cmd/controller`'s `reconcileLoop` tick, deduped to one resolve+commit per distinct `(repo, path)` manifest (every environment an app targets shares one `service.yaml`) — any commit it makes is picked up by the *next* tick's normal fetch/diff/deploy pass, same as a hand-authored digest change; it does not itself deploy anything. One environment-agnostic manifest, one resolved digest — there's no per-environment opt-in (a `runcd.yaml`-level concern, out of scope here). The image-events Eventarc trigger (`internal/api`'s `handleImageEvent`) nudges the same `tick()` this runs inside, so an Artifact Registry push shortens the latency for both the updater and the reconcile pass through the identical `nudgeCh` path — no second trigger/handler needed
- `cloudrun` — `AdminClient` interface; `GCPAdminClient` is the real Cloud Run Admin API v2 implementation (client construction coalesced via `singleflight`, using `context.WithoutCancel` so one caller's cancelled context can't spuriously fail concurrent callers)
- `precondition` — `Checker` interface; `GCPChecker` is the real Pub/Sub-backed implementation
- `diff` — computes Synced/OutOfSync status from desired vs. live state. `env` is skipped for `resourceType: job` (see `manifest`'s note); `nil` desired `EnvVars`/`SecretRefs` means "env not managed for this unit," the same nil-means-unmanaged convention `TrafficLatestRevisionPercent` already uses
- `health` — assesses Healthy/Degraded per resource type (service/job/workerPool). A job's health is really "did the most recent execution succeed," not a service's continuous up/down state — a real, noisier distinction, since a job runs to completion and stops, and its executions can be triggered by something outside RunCD entirely (an external scheduler, another CI pipeline). `reconcile.Result`/`applications` persist `ResourceType` alongside Status/Health specifically so `internal/api`/the dashboard can tell the two apart; the dashboard shows a plain "Job" badge instead of a Health status for job units, rather than dress up an execution outcome as if it were an ongoing health signal
- `reconcile` — the core loop: fetch → diff → precondition-gate (respecting each app's `ignorePreconditions`) → sync-window check (auto-sync only) → deploy → re-fetch → persist → notify. Writes `sync_events` as an audit trail. A per-unit TTL lock (`sync_locks` table) serializes a manual sync against the auto-reconcile loop (or two concurrent manual syncs) deploying the same unit at once — a losing attempt gets `ErrSyncInProgress`. `DryRun` runs the same fetch/diff/health path with deploy short-circuited, no lock taken, nothing persisted. `DetectOrphans` lists live Cloud Run services per project/region a unit still targets and flags any not declared by a current unit there (prune, read-only). `SyncPolicy.Observe` (shadow mode) puts a unit's fetch/diff/health path on the same footing as always — Status/Health are tracked every tick as normal — but blocks deploy outright, overriding both auto-sync and a manual sync's `force`; a manual sync attempt against an observing unit gets `ErrObserveMode` back explicitly (surfaced as 409) instead of a silent no-op, and `DryRun` surfaces the same `Observing` flag so a preview doesn't look identical to a normal non-auto unit's. Meant for onboarding a project/environment onto runcd gradually before granting it any authority to actually change anything. A precondition failure's error is never overwritten by an unrelated later validation (e.g. the job+managed-env rejection below) — both set `res.Err`, but the second is guarded on the first not already having fired.
- `rbac` — role/scope matching (`env:x`, `app:x@project`, `folder:x`) for who may trigger a manual sync. `Store` holds both the parsed `*Config` and a separately-swapped folder-membership map (`SetFolderMembership`/`FolderMembership`) — the two are hot-reloaded independently, same looseness `cmd/controller/main.go`'s reconcileLoop already has between config/notify/RBAC reloads. `HasAnyGrant` (used by orphan detection, which has no single unit to scope a check against) requires a rule with a **non-empty** `Scope`, not just a matching `Subject` — a `scope: []` row grants nothing under `CanSync` either, so it must not count here
- `auth` — identity verification. `IAPAuthenticator` (default, wired in `main.go`) verifies Identity-Aware Proxy's signed assertion header, defense-in-depth on top of IAP/IAM actually gating access; `GoogleAuthenticator` (direct Google OAuth token) is kept as an option for non-IAP deployments but not wired by default.
- `notify` — Slack notifications on sync-failed / health-degraded / stuck-out-of-sync, debounced via `notification_debounce`. Claim, send, and confirm are three separate statements, not one transaction held open across the Slack HTTP call — holding a pooled connection for however long a webhook takes starved every other consumer of the same pool under load, including `internal/leader`'s own `Claim()`, in a real production incident. The claim itself (`claim_expires_at`, a short TTL, same idea as `sync_locks`) is deliberately separate from the debounce marker (`last_notified_at`, only ever set once Send actually succeeds) — a crash or a lost connection mid-send self-heals within that short TTL instead of leaving the row stuck claimed for the rest of the debounce window
- `api` — HTTP handlers: `GET /api/units` (list), `GET /api/units/{project}/{app}` (detail/diff), `GET /api/units/{project}/{app}/history` (sync_events), `GET /api/units/{project}/{app}/dry-run` (preview a sync without deploying), `GET /api/rbac` (configured roles), `GET /api/config` (runtime config), `GET /api/orphans` (prune/orphan detection), `POST /api/sync/{project}/{app}` (manual sync), `POST /api/sync` (bulk sync — ArgoCD's "Sync All": every unit the caller's RBAC covers, optionally narrowed by `?project=` and/or `?filter=outOfSync`; a unit outside the caller's RBAC scope is reported per-unit as `skipped: "forbidden"` rather than failing the whole batch, same one-bad-unit posture `reconcile.RunOnce` already has), `POST /api/events/image` (optional Eventarc image-events add-on, see below), `GET /metrics` (OTel-backed Prometheus exposition). Most read endpoints are open to any authenticated caller; sync, dry-run, and history are RBAC-gated (dry-run makes the same real Cloud Run/Pub-Sub calls a sync does; history's `sync_events.error` column carries raw deploy/DB error text — the same class of detail the sync response itself never echoes, so viewing it needs the same grant as triggering a sync, not just being logged in), same for orphans (`HasAnyGrant`, since it fans out live calls with no single unit to scope a check against). `/metrics` is the one unauthenticated route, matching the controller's no-IAP posture. Go 1.22+ pattern routing. `Handler.Reconciler` is an `*atomic.Pointer[reconcile.Reconciler]`, not a plain pointer — swappable so a config hot-reload doesn't race a concurrent manual sync.
- **Image-events add-on** — `POST /api/events/image` is always registered but does nothing (`http.NotFound`) unless `RUNCD_IMAGE_EVENTS_AUDIENCE`/`RUNCD_IMAGE_EVENTS_SERVICE_ACCOUNT` are both set on the controller, the same "configured or inert" shape `notify.slackWebhookUrl` already has — see `terraform/image-events/` for the Eventarc trigger (Cloud Audit Logs on Artifact Registry pushes; Artifact Registry has no native Eventarc event type) that would call it. `RUNCD_IMAGE_EVENTS_AUDIENCE` must be the full destination URL *including the `/api/events/image` path*, not the bare Cloud Run service URL — confirmed with a real end-to-end test push that Eventarc's Pub/Sub push subscription signs the OIDC token's audience that way; the bare-service-URL version passes `terraform validate` and looks reasonable but gets every real event rejected by the controller's own audience check (`terraform/image-events`'s `expected_audience` output already accounts for this). `auth.EventarcAuthenticator` verifies the push OIDC token like `GoogleAuthenticator` does, but additionally requires the token's email match exactly one configured trigger service account — an authorization check, not just authentication, since this endpoint has no RBAC lookup of its own. The handler deliberately ignores the event body entirely (no per-unit targeting — `reconcile.RunOnce` already only redeploys `OutOfSync` units, so an extra early pass over the whole fleet is harmless) and only nudges `cmd/controller`'s reconcile loop (via a buffered, non-blocking `nudgeCh` the loop's `select` also listens on alongside its ticker) if the receiving replica is currently leader — a non-leader (or the feature being unconfigured) just 202s, falling back to the same `RECONCILE_INTERVAL` polling that's always been the baseline, deliberately not any cross-replica messaging.

`web/` — the Next.js dashboard (Phase 4). App Router, TypeScript, Tailwind
CSS v4, shadcn/ui + lucide-react. Calls the Go API same-origin
(`credentials: "include"`), relying on both sitting behind the same
IAP-protected perimeter — no auth code of its own in the frontend.

`terraform/controller-sa/` — Terraform module provisioning the shared
controller service account (§5.5). Not invoked directly — see its
`examples/minimal/`, which is what CI actually validates.

`terraform/image-events/` — sibling module for the optional Eventarc
image-events add-on (see `internal/api`'s note above); same
"module shape only, validated via its own `examples/minimal/`" status. Reads
the controller's already-deployed Cloud Run state (a `google_cloud_run_v2_service`
data source) rather than managing that service itself, since it's deployed
via `gcloud run deploy`, not Terraform.

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
