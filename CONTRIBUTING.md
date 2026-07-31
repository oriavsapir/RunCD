# Contributing to RunCD

Thanks for considering a contribution. This project stays small and
readable on purpose — see the conventions below before opening a PR.

## Before you start

For anything non-trivial, open an issue first describing what you want to
change and why. It saves everyone time if the approach gets aligned before
code is written.

## Development setup

```bash
git clone https://github.com/oriavsapir/RunCD.git
cd RunCD
go build ./...
cd web && npm install && cd ..
```

Backend tests need Docker (real, ephemeral Postgres via
[testcontainers-go](https://github.com/testcontainers/testcontainers-go) —
no mocks for DB logic, per project convention).

## Running the checks

Backend (repo root):

```bash
go build ./...
go test ./... -race -shuffle=on
gofmt -l .          # must print nothing
go vet ./...
golangci-lint run ./...
govulncheck ./...
nilaway -exclude-errors-in-files internal/notify/slack.go,internal/api/api_test.go,internal/cloudrun/gcp.go,internal/githubapp/githubapp.go,internal/leader/lease.go,web/node_modules ./...
```

Dashboard (`web/`):

```bash
npm run build
npm run lint
npm test
```

All of the above (minus `nilaway`/`govulncheck`, which run separately) are
what CI (`.github/workflows/ci.yml`) checks on every PR.

## Conventions

- **Test-driven.** Write the failing test first for any new behavior or
  bug fix.
- **No comments unless they explain a non-obvious WHY** (a hidden
  constraint, a workaround, a subtle invariant). Don't restate what the
  code already says.
- **Interface + fake for external services** (`cloudrun.AdminClient`,
  `precondition.Checker`, `auth.Authenticator`) so tests don't need live
  GCP/Google API calls. Real Postgres via testcontainers for anything
  DB-backed — not mocks.
- **No unrequested abstractions.** A bug fix doesn't need surrounding
  cleanup; a one-shot operation doesn't need a helper. Don't design for
  hypothetical future requirements.
- **Dashboard UI:** no emoji, icon library only (lucide-react), prefer
  established libraries over custom components.

`CLAUDE.md` has the full architecture and convention notes this project's
own AI-assisted development follows — worth a skim regardless of what
you're using to write code.

## Commit messages / PRs

Explain the *why*, not just the *what* — the diff already shows what
changed. Keep PRs scoped to one change; separate refactors from behavior
changes.

## Reporting bugs / security issues

Open a GitHub issue for ordinary bugs. For anything security-sensitive
(auth bypass, RBAC bypass, credential handling), please don't open a public
issue — contact the maintainer directly instead.
