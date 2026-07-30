# controller-sa

Provisions argorun's shared controller service account (§5.5): one identity,
granted `roles/run.developer` directly in every target project it manages —
not a dedicated per-project runner SA. Adding a project to
`environments[env].projects` in `argorun.yaml` corresponds to adding it to
`target_projects` here.

**Status:** module shape only. Not yet invoked against a real management or
target project — see `examples/minimal` for how it would be called.

## Usage

```hcl
module "controller_sa" {
  source = "./terraform/controller-sa"

  management_project_id = "example-shared-resources"
  target_projects       = ["example-sandbox"]
}
```

See [`examples/minimal`](examples/minimal) for a complete, `terraform
validate`-able example (also what CI runs — this module isn't invoked
directly, only through a caller).

## Inputs

| Name | Description | Type | Default |
|------|-------------|------|---------|
| `management_project_id` | GCP project that owns the controller's shared service account. | `string` | n/a |
| `service_account_id` | Account ID (local part) for the controller's shared service account. | `string` | `"argorun-controller"` |
| `target_projects` | Project IDs the controller may deploy to — one entry per project listed under any `environments[env].projects`. | `set(string)` | `[]` |
| `runtime_service_account_emails` | Per-project runtime service account the deployed revision runs as, keyed by target project ID, if `iam.serviceAccounts.actAs` is required (§5.5 point 2 — verify against current GCP docs). | `map(string)` | `{}` |

## Outputs

| Name | Description |
|------|-------------|
| `service_account_email` | The controller's shared service account — bind this identity in every target project. |

## Known limitation (§5.5 point 4)

One identity with standing access to every target project means a
compromised controller credential reaches every project it's bound to, not
just one. Accepted tradeoff for v1 (see the design spec); a per-project-SA
design is the documented fallback if this blast radius becomes unacceptable.
