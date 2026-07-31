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
client.

`internal/`, roughly in dependency order:
- `config` — parses `runcd.yaml` (environments, apps, notify rules), validates references
- `manifest` — per-app manifest format (§5.1), digest-pin validation
- `expander` — expands config into `SyncUnit`s (one per app × environment)
- `store` — Postgres schema/migrations (`migrations/*.sql`, embedded via `schema.go`)
- `leader` — Postgres-backed leader election (conditional UPDATE lease claim)
- `githubapp` — GitHub App auth (RS256 JWT, stdlib `crypto/rsa`) + Contents API file fetches — no local git clone, keeps the distroless runtime image git-free
- `gitsource` — `reconcile.ManifestSource` on top of `githubapp`, with a short-TTL cache + singleflight coalescing (many sync units often share one manifest)
- `cloudrun` — `AdminClient` interface; `GCPAdminClient` is the real Cloud Run Admin API v2 implementation (client construction coalesced via `singleflight`, using `context.WithoutCancel` so one caller's cancelled context can't spuriously fail concurrent callers)
- `precondition` — `Checker` interface; `GCPChecker` is the real Pub/Sub-backed implementation
- `diff` — computes Synced/OutOfSync status from desired vs. live state
- `health` — assesses Healthy/Degraded per resource type (service/job/workerPool)
- `reconcile` — the core loop: fetch → diff → precondition-gate → deploy → re-fetch → persist → notify. Writes `sync_events` as an audit trail.
- `rbac` — role/scope matching (`env:x`, `app:x@project`) for who may trigger a manual sync
- `auth` — identity verification. `IAPAuthenticator` (default, wired in `main.go`) verifies Identity-Aware Proxy's signed assertion header, defense-in-depth on top of IAP/IAM actually gating access; `GoogleAuthenticator` (direct Google OAuth token) is kept as an option for non-IAP deployments but not wired by default.
- `notify` — Slack notifications on sync-failed / health-degraded / stuck-out-of-sync, debounced via a Postgres transaction (commits the debounce claim only after the send succeeds)
- `api` — HTTP handlers: `GET /api/units` (list), `GET /api/units/{project}/{app}` (detail/diff), `GET /api/units/{project}/{app}/history` (sync_events), `POST /api/sync/{project}/{app}` (manual sync). Read endpoints are open to any authenticated caller; only sync is RBAC-gated. Go 1.22+ pattern routing.

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
wiring, IAP auth, Next.js dashboard). See `PROGRESS.md` for what's still
explicitly deferred (schema migrations aren't applied by `main.go`, no
config/RBAC hot-reload, no per-unit deploy lock between manual sync and the
auto-reconcile loop, and a few other flagged-not-fixed items from the
review passes).
