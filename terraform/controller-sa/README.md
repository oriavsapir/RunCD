# controller-sa

Provisions runcd's shared controller service account (§5.5): one identity,
granted `roles/run.developer` directly in every target project it manages —
not a dedicated per-project runner SA. Adding a project to
`environments[env].projects` in `runcd.yaml` corresponds to adding it to
`target_projects` here; adding a folder to `environments[env].folders`
corresponds to `target_folders`.

**Status:** module shape only. Not yet invoked against a real management or
target project — see `examples/minimal` for how it would be called.

Every grant below except the base service account is independently
opt-in — leave a variable at its default (`[]`/`{}`/`false`) to skip that
IAM binding entirely rather than granting access to a feature you don't use.

## Usage

```hcl
module "controller_sa" {
  source = "./terraform/controller-sa"

  management_project_id = "example-shared-resources"
  target_projects       = ["example-sandbox"]

  # Optional — only if runcd.yaml uses environments[env].folders:
  target_folders = ["123456789012"]

  # Optional — only if some app declares a pubsubTopic/pubsubSubscription
  # precondition (on by default; set false to skip the grant entirely):
  enable_pubsub_preconditions = true

  # Optional — only if a Cloud Run revision runs as a specific runtime SA:
  runtime_service_account_emails = {
    "example-sandbox" = "runtime-sa@example-sandbox.iam.gserviceaccount.com"
  }

  # Optional — only for whichever Secret Manager secrets you actually use:
  secret_accessor_ids = {
    database_url = "projects/example-shared-resources/secrets/runcd-database-url"
  }
}
```

See [`examples/minimal`](examples/minimal) for a complete, `terraform
validate`-able example (also what CI runs — this module isn't invoked
directly, only through a caller).

## Inputs

| Name | Description | Type | Default |
|------|-------------|------|---------|
| `management_project_id` | GCP project that owns the controller's shared service account. | `string` | n/a |
| `service_account_id` | Account ID (local part) for the controller's shared service account. | `string` | `"runcd-controller"` |
| `target_projects` | Project IDs the controller may deploy to — one entry per project listed under any `environments[env].projects`. | `set(string)` | `[]` |
| `target_folders` | GCP folder IDs (numeric) listed under any `environments[env].folders`. Grants `resourcemanager.folderViewer` on the folder and resolves its current `ACTIVE` direct child projects at `apply` time for the same `run.developer` grant `target_projects` gets — a plan-time snapshot, not continuous reconciliation. | `set(string)` | `[]` |
| `runtime_service_account_emails` | Per-project runtime service account the deployed revision runs as, keyed by target project ID, if `iam.serviceAccounts.actAs` is required (§5.5 point 2 — verify against current GCP docs). | `map(string)` | `{}` |
| `enable_pubsub_preconditions` | Grant `roles/pubsub.viewer` on every target project (including folder-resolved ones) so `pubsubTopic`/`pubsubSubscription` preconditions can be checked. Turn off if unused. | `bool` | `true` |
| `secret_accessor_ids` | Secret Manager secrets the controller reads at boot, keyed by an arbitrary label, valued by the secret's full resource ID. Nothing is granted unless listed. | `map(string)` | `{}` |

## Outputs

| Name | Description |
|------|-------------|
| `service_account_email` | The controller's shared service account — bind this identity in every target project. |

## Known limitation (§5.5 point 4)

One identity with standing access to every target project means a
compromised controller credential reaches every project it's bound to, not
just one. Accepted tradeoff for v1 (see the design spec); a per-project-SA
design is the documented fallback if this blast radius becomes unacceptable.
