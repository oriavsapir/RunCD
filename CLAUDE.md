# argorun

ArgoCD-equivalent for Google Cloud Run: reconciles declared manifests against
live Cloud Run services/jobs/worker-pools, gated by preconditions and RBAC.
Go, stdlib-first (no web framework), Postgres via `pgx`.

## Commands

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

CI (`.github/workflows/ci.yml`) runs all of the above plus a Docker build.
See `PROGRESS.md` for the authoritative "what's done / what's tested / what's
deliberately deferred" checklist — check it before assuming something is or
isn't built.

## Architecture

`cmd/controller/main.go` — entrypoint (currently only drives leader election;
not yet wired to run the reconcile loop or serve the API).

`internal/`, roughly in dependency order:
- `config` — parses `argorun.yaml` (environments, apps, notify rules), validates references
- `manifest` — per-app manifest format (§5.1), digest-pin validation
- `expander` — expands config into `SyncUnit`s (one per app × environment)
- `store` — Postgres schema/migrations (`migrations/*.sql`, embedded via `schema.go`)
- `leader` — Postgres-backed leader election (conditional UPDATE lease claim)
- `cloudrun` — `AdminClient` interface for Cloud Run reads/writes (no real impl yet, interface only)
- `precondition` — `Checker` interface gating whether a sync unit may deploy
- `diff` — computes Synced/OutOfSync status from desired vs. live state
- `health` — assesses Healthy/Degraded per resource type (service/job/workerPool)
- `reconcile` — the core loop: fetch → diff → precondition-gate → deploy → re-fetch → persist → notify. Writes `sync_events` as an audit trail.
- `rbac` — role/scope matching (`env:x`, `app:x@project`) for who may trigger a manual sync
- `auth` — Google ID token verification
- `notify` — Slack notifications on sync-failed / health-degraded / stuck-out-of-sync, debounced via Postgres
- `api` — HTTP handler for manual sync (`POST /api/sync/{project}/{app}`), Go 1.22+ pattern routing

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
- UI (future Phase 4 dashboard): no emoji, icon libraries only, prefer
  established libraries over custom code, professional/non-AI-looking polish.

## Phased build plan

Phases 0–3 are done (foundations, reconcile loop, job/worker-pool support,
sync+audit trail, gated sync/RBAC/notifications). Phase 4 (Next.js web
dashboard) is scoped but not started — separate tech stack, do as its own
piece of work rather than folding into Go-side changes.
