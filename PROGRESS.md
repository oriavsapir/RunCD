# argorun — build checklist

Tracks what's built against the design spec's phased plan (§10). Every item
below is either done-and-tested or explicitly not-yet-started — nothing
half-wired.

## Phase 0 — Foundations

- [x] **Postgres schema** (§5.2) — `internal/store/migrations/0001_init.sql`
  `applications`, `sync_events`, `leader_lease` tables, verbatim from spec.
  Lease row pre-seeded already-expired so the first replica claims it.
  - Test: `go test ./internal/leader/...` (applies this schema to a real
    throwaway Postgres container on every run — see "How tests run" below).

- [x] **Leader-election lease claim/renew/crash-takeover** (§5.3) —
  `internal/leader/lease.go`
  Conditional `UPDATE ... WHERE id=1 AND (holder_id=$1 OR expires_at<now())`.
  `Run()` renews every 10s inside a 30s TTL, reports leadership transitions.
  - Test: `go test ./internal/leader/... -v` — 6 cases: first-claim wins,
    second replica blocked while lease is live, renew extends expiry,
    crash-and-takeover (force-expire the row, confirm a new replica claims it
    and the old holder is locked out), `Run`'s claim/renew/cancel loop, and
    losing leadership when renewal stops.

- [x] **Service-definition parsing + digest-pin validation** (§5.1) —
  `internal/manifest/service.go`
  Rejects: missing digest, bare/floating tags, malformed digests, `track`+
  `version` both set. Accepts: digest alone, digest+track, digest+version.
  - Test: `go test ./internal/manifest/... -v` — 10 table-style cases.

- [x] **Terraform module shape for the controller SA** (§5.5) —
  `terraform/controller-sa/` — a proper reusable module: `main.tf`,
  `variables.tf`, `outputs.tf`, `versions.tf` (split out separately, per
  convention), `README.md` (usage/inputs/outputs), and
  `examples/minimal/` — the module itself carries no `.terraform.lock.hcl`
  (a module isn't `init`'d directly; that belongs to whatever calls it),
  the example does. Shared `google_service_account` + `for_each` over
  `target_projects` granting `roles/run.developer`, optional per-project
  `actAs` grant. **Not wired to a real project** — the example uses
  placeholder project IDs, nothing points at `example-sandbox` or any real
  management project yet.
  - Test: `cd terraform/controller-sa/examples/minimal && terraform init
    -backend=false && terraform validate` (this is what CI runs — module
    shape only, doesn't touch real GCP).

## Phase 1 — Core reconcile loop (read-only, no deploy)

- [x] **Root config parsing** (`argorun.yaml`, §5.1) — `internal/config/`
  `environments`/`defaults`/`apps[]`, sync-policy merge (env overrides
  auto/interval, defaults fills in retry/selfHeal). Rejects an app
  referencing an unknown environment.
  - Test: `go test ./internal/config/... -v`

- [x] **Expander** (apps[] × environments[env].projects → sync units, §5.1) —
  `internal/expander/expander.go`
  Applies `overrides`/`exclude`, resolves region fallback
  (override → env → defaults), rejects an `overrides`/`exclude` entry naming
  a project not in that app's environment (fails the whole config load).
  - Test: `go test ./internal/expander/... -v`

- [x] **Diff engine** (compare only `defaults.managedFields`, §5.7) —
  `internal/diff/diff.go` — image digest + optional traffic-percent
  comparison; traffic ignored for `job`/`workerPool` even if listed.
  - Test: `go test ./internal/diff/... -v`

- [x] **Health assessment, all three resourceTypes** (§5.7) —
  `internal/health/health.go` — `AssessService`/`AssessWorkerPool` are
  revision-based (workerPool just has no traffic concept); `AssessJob` is
  execution-based (Succeeded/Running/Failed against the desired digest).
  - Test: `go test ./internal/health/... -v`

- [x] **Precondition checks** (`pubsubTopic`/`pubsubSubscription`, §5.10) —
  `internal/precondition/precondition.go` — fails loudly naming the missing
  resource; unknown precondition types rejected.
  - Test: `go test ./internal/precondition/... -v`

- [x] **Bounded worker-pool reconcile pass, writes `applications`** (§5.4) —
  `internal/reconcile/reconcile.go` — fans out over sync units via
  `errgroup.SetLimit` (default 16 workers), upserts one row per unit. No
  deploy call anywhere in this path. Dispatches per `resourceType` to the
  right Cloud Run client method (`GetService`/`GetJob`) and health assessor.
  - Test: `go test ./internal/reconcile/... -v` — synced, out-of-sync,
    invalid manifest, missing precondition (status=Invalid but health still
    assessed from live state), unprovisioned resource (status=Missing), a
    20-unit concurrent run asserting all 20 rows land in Postgres, workerPool
    (traffic ignored even if managed), job (Healthy on succeeded execution,
    Missing when never executed), and a precondition-failure-plus-unprovisioned
    regression case (Status must stay Invalid, not flip to Missing).

## Phase 1b — Job & worker-pool support

- [x] **Diff engine, job/workerPool** (§5.7) — already generic in Phase 1
  (`resourceType != "service"` skips traffic comparison); added explicit
  tests for `job` to lock in the existing behavior.
  - Test: `go test ./internal/diff/... -v`

- [x] **`cloudrun.AdminClient.GetJob`** — `internal/cloudrun/client.go` —
  new `LiveJob`/`ExecutionStatus` types (execution-based, distinct from
  `LiveService`'s revision-based shape). Interface only, no real Cloud Run
  Jobs API call wired up.

- [x] **`reconcile.go` dispatches on `resourceType`** — service/workerPool
  call `GetService`, job calls `GetJob`; each routes to its own health
  assessor, sharing the same precondition-check → diff → upsert flow via a
  common `applyLiveState` helper (so the "precondition failure takes
  precedence over not-provisioned" fix applies to all three resourceTypes).

## Phase 2 — Sync + audit trail

- [x] **Deploy path** (§6 steps 5-6) — `internal/reconcile/reconcile.go`'s
  `deploySyncUnit`: for a sync unit that's `OutOfSync`, has no prior
  `Invalid`/precondition failure, and whose environment auto-syncs
  (`unit.Sync.Auto == true`) — upserts the `applications` row first (needed
  so `sync_events`' FK has something to reference on a brand-new unit's
  first-ever pass), writes a `sync_events` row (`result=in_progress`)
  *before* calling `CloudRun.DeployService`/`DeployJob`, then re-checks live
  state + health after the deploy call returns and updates the row to
  `succeeded`/`failed`. A failed deploy leaves `Status=OutOfSync` (retried
  next poll, no special-casing, per §7) and records the error on the row.
  A precondition failure or manual (`auto=false`) sync unit never reaches
  the deploy call at all — verified by asserting the fake Cloud Run's state
  is untouched in both cases.
  - Test: `go test ./internal/reconcile/... -v` —
    `TestRunOnce_DeploysOutOfSyncAutoUnit`,
    `TestRunOnce_DeployFailureRecordedAndStatusStaysOutOfSync`,
    `TestRunOnce_ManualSyncNeverDeploys`,
    `TestRunOnce_FailedPreconditionNeverDeploysEvenIfAuto`.

- [x] **`cloudrun.AdminClient.DeployService`/`DeployJob`** —
  `internal/cloudrun/client.go` — interface only, no real Cloud Run Admin
  API calls yet; the test fake replaces (not mutates) its in-memory state on
  deploy, matching a real Cloud Run call, to keep the fix below honest.

### Bug found and fixed after initial Phase 2 implementation

The first version of the post-deploy re-check reused the *same* pre-deploy
`assess()` closure, which closed over a `live` pointer fetched once, before
the deploy — so "re-checking after deploy" was actually re-running the
health function against stale, pre-deploy data. It only passed tests
because the fake's `DeployService` happened to mutate that same shared
struct in place; a real Cloud Run client never would. Fixed by making the
per-resourceType closure a genuine `fetch(ctx)` that calls
`GetService`/`GetJob` fresh every time it's invoked, called once before
diffing and again (a real second network round-trip) after a successful
deploy. Verified two ways: the fake now *replaces* its state on deploy
(so a stale-pointer bug would show up as a test failure, not an
accidental pass), and `TestRunOnce_DeploysOutOfSyncAutoUnit` asserts
`GetService` was actually called twice.

This also means a deploy that hasn't converged by the time of the
post-deploy re-check (still creating, or eventually-consistent propagation
lag) is now handled honestly: `sync_events` still records the deploy call
itself as `succeeded`, but `Status` reflects whatever's actually live, not
an assumption. A subsequent poll that still sees `OutOfSync` may issue
another deploy call before the first one's finished converging — accepted
per NFR6/§5.3 ("deploying an already-deployed digest is a no-op"), which is
a hard requirement on whatever real `DeployService`/`DeployJob`
implementation eventually lands: it must itself be idempotent for an
unchanged desired digest.

- [x] **Crash-mid-sync recovery** (§5.3/§8, explicitly required test) —
  the new leader never reads or trusts a stale `in_progress` `sync_events`
  row; it always re-derives truth by re-fetching live Cloud Run state via
  the same `GetService`/`GetJob` call every pass already makes. Two
  scenarios both covered, matching the two ways a crash can land:
  - `TestRunOnce_CrashMidSync_DeployAlreadyTookEffect` — Cloud Run had
    already accepted the deploy before the crash; the new leader sees
    `Synced`, doesn't redeploy, and the orphaned `in_progress` row is left
    untouched (never updated, never re-read).
  - `TestRunOnce_CrashMidSync_DeployNeverTookEffect` — Cloud Run never
    received the deploy; the new leader safely retries it (NFR6 idempotent
    retry), writing a fresh `sync_events` row while the orphaned one from
    the crashed leader stays as-is.

### Bugs found and fixed while building Phase 2

A background adversarial review (before Phase 2 work started) found one
blocking concurrency bug and two low-severity gaps in the Phase 1/1b code;
all three are fixed now, before Phase 2 built more on top of the same path:

- [x] **HIGH — `RunOnce` used `errgroup.WithContext`**, which cancels every
  other in-flight unit's context the instant *any* single unit's `upsert`
  fails — silently discarding correctly-computed results for the rest of
  the fleet over one transient write error. Fixed by using a plain
  `errgroup.Group` and passing the original (non-cancelling) `ctx` through.
  Verified with a new regression test forcing one specific unit's write to
  fail via a wrapping `db` interface (`Reconciler.DB` is now an interface,
  not `*sql.DB` directly, specifically to make this kind of fault injection
  possible): `TestRunOnce_OneUnitWriteFailureDoesNotDiscardSiblingResults` —
  19 of 20 units still persist correctly despite the 20th's write failing.
- [x] **LOW — `config.Parse` didn't validate `defaults.managedFields`** —
  a typo (`managedFields: [immage]`) parsed fine and the diff engine just
  silently never compared that field. Now rejected at parse time against a
  known set (`image`, `traffic`), matching every other "fail loudly" check
  in this package.
- [x] **LOW — `expander.Expand` trusted an unenforced invariant** ("Parse()
  already guarantees this exists") instead of checking. Now returns an
  explicit error if called with an app referencing an unknown environment,
  rather than silently producing zero sync units for that app.
- [x] **Informational — schema hardening** — added `CHECK` constraints on
  `applications.status`/`.health` and `sync_events.trigger`/`.result`
  (previously enum values were comment-only), and an index on
  `sync_events(application, target_gcp_project, started_at DESC)` since
  Phase 2 is the point this table actually starts getting written to.

- **A sweep for orphaned `in_progress` sync_events rows.** A row left
  `in_progress` by a crashed leader is never revisited by design (the next
  pass re-derives truth from live Cloud Run, never from this row) — which
  is correct for controller state but means the audit trail can
  accumulate permanently-stuck rows with no `finished_at`, which is bad
  for the compliance-evidence use case (FR6/NFR5). Flagged, not yet
  implemented — pending a decision on when a row should be considered
  "definitely orphaned" (e.g. some multiple of the lease TTL) and what to
  mark it as (there's no `unknown`/`timed_out` value in the `result`
  CHECK constraint yet, only `in_progress`/`succeeded`/`failed`). Still open.

## Phase 3 — Gated sync + RBAC + notifications

- [x] **`internal/rbac`** (§5.9) — flat `subject -> role -> scope` model.
  `CanSync` matches `"*"` (everything), `"env:<name>"` (via the new
  `expander.SyncUnit.Env` field), or `"app:<name>@<project>"` (exact match).
  Rejects an unknown role at parse time. **Documented limitation**: a
  `subject` that's a Google Workspace group only matches if passed in
  literally — no real Google Workspace Admin SDK call resolves group
  membership (consistent with this repo's "no real GCP calls" posture).
  - Test: `go test ./internal/rbac/... -v`

- [x] **`internal/auth`** — `Authenticator` interface + `GoogleAuthenticator`
  (a real implementation using `google.golang.org/api/idtoken`, verifying
  signature, audience, and `email_verified`). Only the offline rejection
  path is tested (malformed/empty tokens fail before any network call) —
  there's no way to test the happy path without a live Google-issued token.

- [x] **Manual sync path** (§5.9/FR4, §6 steps 5-6 via a human) —
  `Reconciler.ManualSync(ctx, unit, actor)`: same precondition-check → diff
  → deploy-and-audit machinery as the auto loop, but always attempts the
  deploy (trigger=`manual`, actor=the verified email) regardless of the
  unit's `auto` flag — unless the unit is `Invalid`/`Missing`, in which
  case a failed precondition still blocks deploy exactly like the auto
  path (§5.10: a precondition failure blocks deploy no matter who triggers
  it). Required refactoring `reconcileOne` into a shared `reconcile(ctx,
  unit, syncOptions{trigger, actor, force})` so both entry points share one
  code path — no duplicated deploy logic.
  - Test: `go test ./internal/reconcile/... -run ManualSync -v`

- [x] **`internal/api`** — a stdlib-only (`net/http`, Go 1.22+ pattern
  routing, no framework) HTTP handler: `POST /api/sync/{project}/{app}`.
  Bearer token → `auth.Authenticator.Verify` → `rbac.CanSync` →
  `Reconciler.ManualSync` → JSON response. 401 (missing/invalid token), 404
  (unknown app/project), 403 (authenticated but out of RBAC scope), 200
  (synced, with `status`/`health`/`error` in the body).
  - Test: `go test ./internal/api/... -v` — full request/response cycle via
    `httptest`, covering all four outcomes above plus a successful sync
    that actually mutates the fake Cloud Run state.

- [x] **`internal/notify`** (§5.8) — `Evaluator` implements
  `reconcile.Notifier`, invoked once per reconcile pass per sync unit
  (both from `RunOnce` and `ManualSync`), best-effort (a notification
  failure never fails the reconcile pass itself):
  - `syncFailed` — fires immediately when `sync_events` resolves to
    `failed` (no duration check).
  - `healthDegraded` — fires once `Health="Degraded"` has held for
    `forMinutes`, using the new `applications.health_since` column.
  - `outOfSyncGated` — fires once a **gated** (`auto=false`) unit has sat
    `OutOfSync` for `forHours`, using `applications.status_since`; never
    fires for an auto-sync unit (that's just normal poll-cycle drift, not
    "stuck waiting on a human").
  - Debounced per (sync unit, rule) via a new `notification_debounce`
    table — a single atomic `INSERT ... ON CONFLICT DO UPDATE ... WHERE
    last_notified_at < now() - interval RETURNING` statement, so concurrent
    callers can't double-send and a sustained failure doesn't spam Slack
    more than once per `DebounceInterval` (default 1 hour, per §5.8).
  - `SlackSink` — v1's one sink, posts `{"text": message}` to a webhook
    URL. Sink-agnostic engine, so adding a second sink later is additive.
  - Test: `go test ./internal/notify/... -v` — 13 cases: each rule firing
    correctly, each rule's debounce (fires once, stays silent within the
    window, fires again after), rules debounced independently of each
    other, an auto-sync unit never triggering `outOfSyncGated`, and the
    Slack sink's payload shape / non-2xx handling via `httptest`.

- [x] **Schema additions for Phase 3** — new migration
  `internal/store/migrations/0002_notify.sql` (Phase 0/2's `0001_init.sql`
  is treated as already-merged, not edited): `applications.status_since`/
  `.health_since` (reset only when the value actually changes — a `CASE
  WHEN` in the same `upsert` statement, verified by
  `TestUpsert_StatusSinceResetsOnlyWhenStatusChanges`), and the
  `notification_debounce` table.

- [x] **`config.Root.Notify`** — `notify.slackWebhookUrl`/`notify.rules`
  parsed as part of root `argorun.yaml` (§5.1), not a separate file.
  Rejects an unknown `on` value or a `healthDegraded`/`outOfSyncGated` rule
  missing its required `forMinutes`/`forHours` at parse time.

## Live wiring — `main.go` now drives the real controller

- [x] **`internal/cloudrun.GCPAdminClient`** (`internal/cloudrun/gcp.go`) —
  real Cloud Run Admin API v2 implementation of `AdminClient`, one regional
  client cached per region (Cloud Run has no global endpoint). `GetService`
  covers both `service` and `workerPool` resource types (the interface
  doesn't carry resourceType) by trying the Services API first and falling
  back to WorkerPools on `NotFound` — one extra round-trip for workerPool
  units. `GetJob` reads health off `Job.LatestCreatedExecution`'s completion
  status rather than fetching the full `Execution` — one fewer round-trip.
  No tests (would require live GCP credentials — same posture as every
  other real-GCP adapter in this repo); the fakes remain what's tested.

- [x] **`internal/precondition.GCPChecker`** (`internal/precondition/gcp.go`)
  — real Pub/Sub Admin API implementation of `Checker`, one client cached
  per target project.

- [x] **`internal/githubapp`** — GitHub App authentication + the Contents
  API, no local git clone (the Dockerfile is distroless — no `git` binary
  in the runtime image). Mints and caches short-lived installation tokens
  (RS256 JWT signed with stdlib `crypto/rsa`, no JWT library dependency).
  `Client.GetFile(ctx, repo, ref, path)` fetches one file's raw bytes via
  `Accept: application/vnd.github.raw`.
  - Test: `go test ./internal/githubapp/... -v` — `parseOwnerRepo` against
    all three repo-URL shapes, and the JWT's claims/structure (generated
    against a throwaway RSA key, no network).

- [x] **`internal/gitsource.Source`** — implements
  `reconcile.ManifestSource` on top of `githubapp.Client`: fetches each sync
  unit's manifest from its `SourceRepo`'s default branch (`SyncUnit` carries
  no ref).

- [x] **`main.go`** now actually runs the controller: loads `argorun.yaml`
  and `rbac.yaml` from `ARGORUN_CONFIG_REPO` via the GitHub App client,
  leader-gates the auto-reconcile loop (only the leader deploys; every
  replica serves the API — no per-unit lock, so a manual sync on a
  non-leader replica can race the leader's auto pass; fine for a
  single-replica sandbox, add locking if that matters at scale), and
  refreshes the sync-unit list every `RECONCILE_INTERVAL` tick so a new app
  is reachable by the manual-sync API without a restart. RBAC config,
  managedFields, and notify rules are only read once at startup — restart
  to pick up changes there.
  - Required env: `DATABASE_URL`, `AUTH_AUDIENCE` (OAuth client ID —
    fails startup if unset, it's a trust boundary), `ARGORUN_CONFIG_REPO`,
    `ARGORUN_CONFIG_BRANCH`, `ARGORUN_CONFIG_PATH`, `GITHUB_APP_ID`,
    `GITHUB_APP_PEM`.
  - Optional: `RBAC_PATH` (default `rbac.yaml`, same repo/branch as config),
    `HTTP_ADDR` (default `:8080`), `RECONCILE_INTERVAL` (default `30s`).
  - **Not built yet, on purpose:** schema migrations aren't applied by
    `main.go` — `internal/store.Schema` still has to be run against the
    target Postgres out of band before first boot. Config/RBAC/notify
    hot-reload (see above). A real per-unit deploy lock.

## Bugs found and fixed in a post-wiring review

An external-style review of the live-GCP wiring above found 11 issues; all
were verified against the actual source before fixing. 9 were fixed
directly (each with its own regression test); 2 were left as documented
tradeoffs rather than guessed at.

- [x] **Split-brain leadership** (`leader/lease.go`) — a transient DB error
  during renewal killed the lease-renewal goroutine without ever calling
  `leading(false)`; a caller tracking leadership via that callback (like
  `main.go`'s `isLeader atomic.Bool`) could keep believing it was leader
  forever, even as another replica correctly claimed the now-unrenewed
  lease. Fixed: `attempt()` now calls `leading(false)` before returning the
  error if it was leader. `Lease.db` is now an interface (was `*sql.DB`) so
  the fix could be regression-tested with a wrapper that fails renewal on
  demand.
  - Test: `TestRun_ErrorDuringRenewalSignalsLeadershipLoss`

- [x] **Permanent traffic redeploy loop** (`diff/diff.go`) — a real Cloud
  Run client always returns a non-nil live traffic percent, but `desired`
  stays `nil` whenever a manifest manages traffic without setting a
  `traffic:` block — `trafficEqual(nil, non-nil)` never matched, so the
  unit was OutOfSync (and redeployed) on every poll, forever. Fixed:
  `Compute` now skips the traffic comparison entirely when desired has no
  opinion, rather than treating nil-vs-non-nil as a mismatch.
  - Test: `TestCompute_TrafficManagedButManifestOmitsItIsSynced`

- [x] **`GetJob` template/execution conflation** (`cloudrun/gcp.go`) — right
  after `DeployJob`'s `UpdateJob` succeeds (job spec now shows the new
  digest) but before the `RunJob`-triggered execution finishes,
  `LatestCreatedExecution` can still reference the *previous* execution —
  reporting that execution's outcome for a digest that hasn't actually run
  yet. Fixed: `GetJob` now fetches the actual `Execution` via a new
  `ExecutionsClient` and reads its own container image + completion state,
  instead of trusting the job's spec template or the `ExecutionReference`'s
  own (laggy) completion status.
  - No live-GCP test (same posture as the rest of this file); pure-function
    helpers (`executionStatus`, `conditionState`, `latestRevisionPercent`,
    `containerImage`) are unit-tested in the new `cloudrun/gcp_test.go`.

- [x] **Duplicate `projects` entries** (`config/config.go`) — an
  environment listing the same project twice produced duplicate
  `SyncUnit`s, reconciled concurrently and racing writes to the same
  `applications`/`sync_events` rows. Fixed: `Parse` now rejects a repeated
  project within one environment.
  - Test: `TestParse_DuplicateProjectInEnvironmentRejected`

- [x] **Unclamped `traffic.latestRevisionPercent`** (`manifest/service.go`)
  — a manifest could set an out-of-range percent (negative or >100), which
  the deploy path silently clamped while the diff engine compared against
  the raw unclamped value — the same permanent-redeploy pattern as above.
  Fixed: `Parse` now rejects `latestRevisionPercent` outside `[0, 100]`.
  - Test: `TestParse_TrafficPercentOutOfRangeRejected`,
    `TestParse_TrafficPercentInRangeIsValid`

- [x] **`DeployService` traffic overwrite** (`cloudrun/gcp.go`) — always
  replaced the entire `Traffic` list with one `{LATEST, percent}` target,
  which for any percent other than 0 or 100 produces a Cloud Run traffic
  spec that doesn't sum to 100 (an invalid spec, previously only caught
  deep inside the API call). Fixed: `validatedPercent` now rejects anything
  but a full cutover (0 or 100) with a clear error naming the limitation —
  argorun's traffic model has no way to say where the remaining percent
  should go.
  - Test: `TestValidatedPercent_FullCutoverAccepted`,
    `TestValidatedPercent_PartialRejected`

- [x] **Region can resolve to empty** (`expander/expander.go`) — neither
  `defaults.region` nor `environments[env].region` was required, so an
  all-empty region only surfaced as an opaque Cloud Run API error deep in a
  malformed resource name. Fixed: `Expand` now rejects a unit whose region
  resolves to `""`, naming exactly which config keys could set it.
  - Test: `TestExpand_NoResolvedRegionIsRejected`

- [x] **Mutex held across client construction** (`cloudrun/gcp.go`,
  `precondition/gcp.go`) — the per-region/per-project client cache held its
  mutex across `run.NewServicesClient`/`pubsub.NewClient`, which can block
  on ADC credential resolution (a real metadata-server round-trip),
  collapsing the whole reconcile worker pool to serial execution on cold
  start. Fixed: construction is now coalesced per key via
  `golang.org/x/sync/singleflight` (already a dependency, no new import);
  the map mutex itself is only ever held for a plain lookup/insert.

- [x] **GitHub token cache stampede** (`githubapp/githubapp.go`) —
  concurrent cache misses for the same repo each independently minted a
  fresh JWT + installation token. Fixed: `installationToken` now coalesces
  concurrent misses per owner/repo via `singleflight.Group`.
  - Test: `TestInstallationToken_ConcurrentMissesCoalesce` (a fake
    `http.RoundTripper` counts real calls to prove 20 concurrent misses
    produce exactly one installation lookup and one token mint).

## Bugs found and fixed in a second review pass

- [x] **Notify debounce key collision across thresholds**
  (`notify/notify.go`) — `maybeNotify` keyed the debounce row only on the
  rule *type* string (e.g. `"healthDegraded"`), ignoring the actual
  `config.NotifyRule`'s threshold. Two `healthDegraded` rules with
  different `forMinutes` (a normal early-warning + escalation config)
  collided on one row — whichever fired first silently blocked the other
  for the full debounce interval. Fixed: the debounce key now folds in the
  threshold (`"healthDegraded:5"` vs `"healthDegraded:60"`). Required
  relaxing `notification_debounce.rule`'s CHECK constraint in
  `0002_notify.sql` from a fixed enum to a regex match — safe to edit in
  place since that migration hasn't been applied to any live database yet.
  - Test: `TestEvaluate_SameRuleTypeDifferentThresholdsDebounceIndependently`

- [x] **API 500 path leaked internal error detail** (`api/api.go`) — a
  `ManualSync` infra failure (DB error text, potentially GCP error detail)
  was echoed straight into the HTTP response body for any RBAC-authorized
  caller. Fixed: the real error is logged server-side (with `%q` on the
  request-controlled `app`/`project`/`email` fields to neutralize log
  injection, since gosec's taint check can't tell `%q` already defeats
  that); the client gets a generic `"sync failed"` message.
  - Test: `TestHandleSync_InfraErrorReturns500WithoutLeakingDetail`

- [x] **`githubapp.Client`'s HTTP client had no timeout** — falling back to
  `http.DefaultClient` (which has `Timeout: 0`) meant a hung connection
  could block a goroutine indefinitely if its context carried no deadline
  (e.g. the reconcile loop's ticker-driven ctx). Fixed: the fallback is now
  a client with a 30s timeout.

- [x] **`parseOwnerRepo` didn't validate repo shape** — a malformed
  `source.repo` with an extra path segment (e.g. `owner/repo/extra`) was
  silently accepted, producing a confusing 404 at fetch time instead of a
  clear error at config-load time. Fixed: rejects any extra segment.
  - Test: `TestParseOwnerRepo_ExtraSegmentRejected`

- [ ] **Left as-is, deliberately:**
  - A precondition-failing unit still makes a live Cloud Run fetch every
    poll purely to keep `Health` visible for display — real API cost, but
    an intentional Phase-1 decision (tested by
    `TestRunOnce_...PreconditionFailureSurvivesUnprovisionedResource`), not
    an oversight. Revisit only if the API-call volume becomes a real cost
    concern.
  - `rbac.Role` (admin/syncer) is validated at parse time but never
    actually consulted in `CanSync` — the two roles currently grant
    identical access. Not fixed because doing so means inventing what
    "admin" should additionally grant, which isn't specified anywhere yet
    — a product decision, not a mechanical bug.
  - `internal/api/api_test.go`'s nilaway exclusion couldn't be narrowed to
    just the `postSync` helper — verified by trying it: nilaway reports at
    the dereference site (every call site's `.StatusCode`/`.Body` access
    across the test functions), not at `postSync`'s own `http.Client.Do`
    call, so isolating the helper into its own file doesn't shrink what
    has to be excluded.

## Infra / delivery

- [x] **Dockerfile** — multi-stage (`golang:1.26-alpine` build →
  `gcr.io/distroless/static-debian12:nonroot`), non-root, 18.9MB.
  Packages `cmd/controller`, which today only runs the leader-election loop
  (`DATABASE_URL` → connect → `leader.Run`) — nothing else is wired into
  `main.go` yet.
  - Test: `docker build -t argorun-controller:test .` then
    `docker run --rm -e DATABASE_URL=... argorun-controller:test`

- [x] **CI** (`.github/workflows/ci.yml`) — 7 parallel jobs: `fmt` (gofmt),
  `vet`, `lint` (golangci-lint v2.12.2 via `golangci-lint-action@v9`, config
  in `.golangci.yml`), `vulncheck` (`govulncheck`), `nilaway`, `test`
  (`go test -race -shuffle=on`, runs the real-Postgres suite —
  GitHub-hosted runners have Docker preinstalled), `terraform`
  (fmt/init/validate), `docker` (build-only, no push).

- [x] **`.golangci.yml`** — defaults (errcheck, govet, ineffassign,
  staticcheck, unused) plus bodyclose/sqlclosecheck/rowserrcheck (this repo
  is `database/sql`-heavy), gosec, errorlint, unconvert, unparam, misspell,
  revive (with the godoc-on-every-export rule disabled — deliberately
  terse-comment style), noctx, copyloopvar, gocritic.

- [x] **`govulncheck` / `nilaway`** — run locally and in CI, not part of
  `golangci-lint` (nilaway isn't a golangci-lint linter; govulncheck is a
  separate tool by design). Caught and fixed 6 real transitive
  vulnerabilities (`golang.org/x/text` → v0.39.0, `golang.org/x/crypto` →
  v0.52.0) during setup.

## How the tests actually run

Every non-trivial test in this repo runs against **real dependencies**, not
mocks-all-the-way-down:
- `internal/leader`, `internal/reconcile` spin up a real, throwaway Postgres
  container per test via `testcontainers-go` (`internal/testutil/postgres.go`)
  — needs Docker running locally (`docker ps` to check).
- `internal/reconcile`'s Cloud Run/precondition dependencies are fakes
  (in-memory maps) — deliberately, since no real GCP wiring exists yet.
- Everything else (`config`, `expander`, `diff`, `health`, `manifest`,
  `precondition`) is pure-function unit tests, no external dependencies.

Run everything: `go build ./... && go vet ./... && gofmt -l . && golangci-lint run ./... && go test ./... -race`
