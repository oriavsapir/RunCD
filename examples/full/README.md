# Full example repo

Everything in the other `examples/*/` folders composed into one realistic
repo layout — what a real `RUNCD_CONFIG_REPO` actually looks like, not just
one feature in isolation.

```
full/
├── runcd.yaml               # environments, apps, notify — see below
├── rbac.yaml                # who may trigger a manual sync
└── services/
    ├── checkout-service/service.yaml
    ├── nightly-batch-job/service.yaml   (resourceType: job)
    └── legacy-service/service.yaml
```

What's combined here:

- **`dev`** — explicit `projects` plus a `folders` entry, narrowed to
  `syncFailed`-only notifications.
- **`staging`** — a business-hours `syncWindows` allow rule.
- **`prod`** — a Friday `syncWindows` deny rule, a region override
  (`acme-prod-eu`) and an image-track override (`acme-prod-us`) via
  `apps[].overrides`, `traffic` excluded from managed fields via
  `ignoreFields`, and notify routed to a dedicated `prod-incidents` Slack
  sink with every rule enabled.
- **`legacy-onboarding`** — `sync.observe` shadow mode, plus
  `ignorePreconditions` skipping a `requires` entry the manifest declares
  but this environment can't satisfy.
- **`nightly-batch-job`** — a `resourceType: job` manifest, excluded from
  `acme-prod-eu` via `apps[].exclude`.
- **`notify`** — named Slack sinks, a 15-minute early warning plus a
  60-minute escalation `healthDegraded` pair (disambiguated by `name`), and
  `healthRecovered`.

Every file here is checked against the real parsers/expander in
`internal/config`, `internal/rbac`, and `internal/manifest` (see each
package's `TestExamplesParse`, plus `internal/config`'s
`TestExamplesExpand`) — none of it is aspirational, all of it actually
loads and expands into real sync units. The image digests, project IDs, and
webhook URLs are fake placeholders, though — this repo isn't meant to be
deployed anywhere.

If you want to see one concept without everything else around it, start
with the other `examples/*/` folders instead.
