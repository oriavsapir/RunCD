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
  `terraform/controller-sa/` — shared `google_service_account` +
  `for_each` over `target_projects` granting `roles/run.developer`, optional
  per-project `actAs` grant. **Not wired to a real project** — no
  `management_project_id` or `target_projects` values are set anywhere yet.
  - Test: `cd terraform/controller-sa && terraform init -backend=false &&
    terraform validate` (module shape only, doesn't touch real GCP).

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

- [ ] **Not built yet, on purpose:**
  - Git polling / a real `ManifestSource` — `reconcile.ManifestSource` is an
    interface today; nothing fetches `argorun.yaml` or service definitions
    from `acme-org/deployment` yet.
  - A real `cloudrun.AdminClient` — `internal/cloudrun/client.go` is an
    interface only; no Cloud Run Admin API calls exist anywhere in the repo.
  - A real `precondition.Checker` — same story, Pub/Sub calls not wired.
  - Anything that deploys, writes `sync_events`, or reads real GCP state.

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
