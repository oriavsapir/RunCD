# RunCD

[![CI](https://github.com/oriavsapir/RunCD/actions/workflows/ci.yml/badge.svg)](https://github.com/oriavsapir/RunCD/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

An ArgoCD-equivalent for [Google Cloud Run](https://cloud.google.com/run): it
reconciles a declared Git manifest against your live Cloud Run
services/jobs/worker-pools, gates deploys behind preconditions and RBAC, and
gives you a dashboard to see drift and trigger syncs.

If you already run ArgoCD for Kubernetes and have workloads on Cloud Run
instead (or alongside), RunCD gives you the same GitOps model — desired
state in Git, continuous reconciliation, manual sync with audit trail —
without needing a cluster.

## Why

Cloud Run doesn't have a first-class GitOps controller. RunCD fills that
gap:

- **Declarative** — one `runcd.yaml` describes every environment, project,
  and app; each app's own manifest (in your app's repo) pins the desired
  image by digest.
- **Continuously reconciled** — a background loop polls Git and live Cloud
  Run state, detects drift (`OutOfSync`), and either auto-deploys or waits
  for a human, depending on policy.
- **Gated** — Pub/Sub topic/subscription preconditions can block a deploy
  until its dependencies exist; RBAC controls who can trigger a manual sync,
  scoped per environment, project, app, or GCP folder.
- **Auditable** — every sync attempt (auto or manual) is a durable
  `sync_events` row: who, when, what image, what happened.
- **Observable** — a Next.js dashboard shows every unit's sync/health
  status, a desired-vs-live diff, sync history, and lets an authorized user
  trigger a sync — plus Slack notifications on failure/degradation.

## How it works

```
┌─────────────┐     poll      ┌──────────────┐    diff & deploy    ┌───────────┐
│ runcd.yaml   │ ───────────▶ │  controller  │ ──────────────────▶ │ Cloud Run │
│ + app        │   (GitHub)   │ (leader-     │                     │ services/ │
│ manifests    │               │  elected)    │ ◀────────────────── │ jobs/pools│
└─────────────┘               └──────┬───────┘   live state check  └───────────┘
                                      │
                              read/write state
                                      │
                                ┌─────▼─────┐        ┌──────────────┐
                                │ Postgres  │        │  Dashboard   │
                                │(applications,       │  (Next.js)   │
                                │ sync_events,        │  same-origin │
                                │ leader_lease)        │  API calls   │
                                └───────────┘        └──────────────┘
```

Every replica serves the read API and the manual-sync endpoint; only the
elected leader runs the auto-reconcile loop, so it's safe to run more than
one instance. See [`CLAUDE.md`](CLAUDE.md) for the full architecture
breakdown and [`PROGRESS.md`](PROGRESS.md) for exactly what's built, tested,
and deliberately deferred.

## Quickstart

**Requirements:** Go 1.26+, Node 24+, Docker (for the test suite's ephemeral
Postgres), a GCP project, a GitHub App (for config + manifest fetches).

```bash
git clone https://github.com/oriavsapir/RunCD.git
cd RunCD

# Backend
go build ./...
go test ./... -race -shuffle=on   # spins up real, throwaway Postgres containers

# Dashboard
cd web
npm install
npm run build
npm test
```

### Running the controller

The controller needs a Postgres database, a GitHub App (to fetch
`runcd.yaml`/`rbac.yaml` and app manifests), and Identity-Aware Proxy in
front of it (or the `GoogleAuthenticator` fallback, for non-IAP setups).

```bash
export DATABASE_URL="postgres://user:pass@host:5432/runcd"
export IAP_AUDIENCE="/projects/<PROJECT_NUMBER>/global/backendServices/<ID>"
export RUNCD_CONFIG_REPO="your-org/your-deployment-repo"
export RUNCD_CONFIG_BRANCH="main"
export RUNCD_CONFIG_PATH="runcd.yaml"
export GITHUB_APP_ID="..."
export GITHUB_APP_PEM="$(cat your-github-app-private-key.pem)"

go run ./cmd/controller
```

`DATABASE_URL` can be swapped for `CLOUDSQL_INSTANCE_CONNECTION_NAME` +
`CLOUDSQL_IAM_DB_USER` + `CLOUDSQL_DB_NAME` to connect via the Cloud SQL Go
Connector with IAM database auth instead of a password. Other env vars,
all optional: `RBAC_PATH` (default `rbac.yaml`), `HTTP_ADDR` (default
`:8080`), `RECONCILE_INTERVAL` (default `30s`), `DB_MAX_OPEN_CONNS`
(default `25`), and `RUNCD_IMAGE_EVENTS_AUDIENCE` +
`RUNCD_IMAGE_EVENTS_SERVICE_ACCOUNT` (both required together to enable the
Eventarc image-events add-on — see [`terraform/image-events`](terraform/image-events)).

Schema migrations apply automatically on boot (idempotent — safe on every
restart, including a fresh database). See [`internal/config`](internal/config)
for the full `runcd.yaml` shape and [`examples/`](examples/) for one folder
per feature (folders, per-project overrides, sync windows, observe mode,
notify, ...), each with its own README and a runnable example, plus
[`examples/full/`](examples/full/) for a complete repo layout (`runcd.yaml`
+ [`rbac.yaml`](examples/rbac/) + service manifests) with everything
composed together.

### Running the dashboard

```bash
cd web
RUNCD_API_URL="https://your-runcd-controller-url" npm run dev
```

The dashboard calls the controller's API same-origin (`credentials:
"include"`) on the assumption both sit behind the same IAP-protected
perimeter — see [`web/src/app/api/proxy`](web/src/app/api/proxy) for the
server-to-server proxy this relies on.

### CLI

`cmd/runcd` is a small terminal client for the same API the dashboard
uses — list units, inspect a diff, check sync history, trigger a sync,
or see who's allowed to sync what, without a browser.

```bash
go build -o runcd ./cmd/runcd

export RUNCD_API_URL="https://your-dashboard-url/api/proxy"  # see below
runcd units
runcd get <project> <app>
runcd sync <project> <app>
```

`RUNCD_API_URL` should point at the dashboard's own `/api/proxy` (its
server-to-server auth to the controller already exists — see above), not
the bare controller service: the controller typically has no IAP of its
own, only Cloud Run IAM invoker scoped to the dashboard's service account,
so a human generally isn't an authorized caller of it directly. If
`RUNCD_API_URL` is itself IAP-protected, set `RUNCD_IAP_AUDIENCE` to the
same audience value the controller's own `IAP_AUDIENCE` env var uses —
`runcd` shells out to `gcloud auth print-identity-token` for a token,
reusing whichever account is already active in `gcloud`. Run `runcd help`
for the full command list.

## Tech stack

- **Backend:** Go, stdlib-first (no web framework — `net/http`'s
  `ServeMux` pattern routing), [`pgx`](https://github.com/jackc/pgx) for
  Postgres.
- **Dashboard:** Next.js (App Router), TypeScript, Tailwind CSS v4,
  [shadcn/ui](https://ui.shadcn.com) + [lucide-react](https://lucide.dev).
- **Infra:** Terraform modules for the shared controller service account
  (`terraform/controller-sa`) and the optional image-events Eventarc add-on
  (`terraform/image-events`), Docker (distroless, non-root), GitHub
  Actions CI.

## Project layout

```
cmd/controller/     entrypoint — leader election, auto-reconcile loop, HTTP API
cmd/runcd/          CLI client for the HTTP API
internal/
  config/           runcd.yaml parsing
  manifest/         per-app manifest format, digest-pin validation
  expander/         config -> sync units
  folders/          GCP folder -> child project resolution (config + RBAC)
  store/            Postgres schema (embedded migrations) + boot-time apply
  leader/           Postgres-backed leader election
  githubapp/        GitHub App auth + Contents API fetches/writes
  gitsource/        manifest source on top of githubapp, cached/coalesced
  imageupdater/     optional add-on: resolves+commits digests back to Git
  registry/         Artifact Registry tag listing/resolution, cached
  cloudrun/         Cloud Run Admin API v2 client
  precondition/     Pub/Sub-backed precondition checks
  diff/             desired vs. live -> Synced/OutOfSync
  health/           per-resource-type health assessment
  reconcile/        the core loop: fetch -> diff -> gate -> deploy -> persist
  rbac/             role/scope matching for manual sync
  auth/             IAP (default) / direct Google OAuth identity verification
  notify/           Slack notifications, debounced
  api/              HTTP handlers (dashboard reads + gated sync)
web/                Next.js dashboard
terraform/          controller service-account + image-events modules
examples/           reference rbac.yaml
```

## Contributing

Contributions are welcome — see [`CONTRIBUTING.md`](CONTRIBUTING.md) for
conventions (test-driven, real Postgres via testcontainers rather than
mocks for anything DB-backed, no unrequested abstractions) and how to run
the full check suite locally before opening a PR.

## License

[Apache License 2.0](LICENSE).
