# controller

Provisions everything runcd's controller (and, optionally, its dashboard)
needs to run against real GCP infra: the shared controller service account
(§5.5) — one identity, granted `roles/run.developer` in every target project
it manages, not a dedicated per-project runner SA — the IAM/API-enablement
pieces around it a real deployment also needs (folder access, Pub/Sub
precondition checks, Secret Manager access, Cloud SQL IAM database auth,
Artifact Registry read access), and, if you want it, the Cloud Run v2
services themselves for the controller and the dashboard.

This is the one module a new deployment imports. Adding a project to
`environments[env].projects` in `runcd.yaml` corresponds to adding it to
`target_projects` here; adding a folder to `environments[env].folders`
corresponds to `target_folders`.

Every grant below except the base service account and API enablement is
independently opt-in — leave a variable at its default (`[]`/`{}`/`false`/
`null`) to skip that IAM binding entirely rather than granting access to a
feature you don't use. `deploy_controller`/`deploy_dashboard` follow the
same rule: both default to `false`, since plenty of setups deploy the
services themselves via `gcloud run deploy`/their own CI and only want this
module for IAM.

**Two-phase story with a sibling module, not a submodule relationship:**
apply this module first — it's everything the controller needs *before* it
can run. The optional Eventarc image-events add-on is a separate, sibling
module — [`terraform/image-events`](../image-events) — kept out of this one
on purpose: it inherently depends on the controller already being deployed
and serving traffic (it reads the live Cloud Run service to build its
trigger destination), so it can only be applied as a deliberate second
step, not nested as a component of this one. See
[`terraform/README.md`](../README.md) for the two-module apply order.

## Usage — IAM only (deploy the services yourself)

```hcl
module "controller" {
  source = "./terraform/controller"

  management_project_id = "example-shared-resources"
  target_projects        = ["example-sandbox"]

  # Optional — only if runcd.yaml uses environments[env].folders:
  target_folders = ["123456789012"]

  # Optional — only if some app declares a pubsubTopic/pubsubSubscription
  # precondition (off by default — this grant is project-wide, not scoped
  # to the specific topic/subscription a manifest declares):
  enable_pubsub_preconditions = true

  # Optional — only if a Cloud Run revision runs as a specific runtime SA:
  runtime_service_account_emails = {
    "example-sandbox" = "runtime-sa@example-sandbox.iam.gserviceaccount.com"
  }

  # Optional — only for whichever Secret Manager secrets you actually use:
  secret_accessor_ids = {
    database_url = "projects/example-shared-resources/secrets/runcd-database-url"
  }

  # Optional — only if DATABASE_URL uses the Cloud SQL IAM auth path:
  cloudsql_instance_name = "runcd-db"

  # Optional — only if the dashboard needs to invoke the controller directly
  # (no IAP in front of it) and you're deploying it outside this module:
  controller_cloud_run_service_name = "runcd"
  controller_cloud_run_region       = "us-central1"
  dashboard_invoker_members = [
    "serviceAccount:dashboard@example-shared-resources.iam.gserviceaccount.com",
  ]
}

# Second, separate apply, once the controller above is deployed and serving:
module "image_events" {
  source = "./terraform/image-events"

  project_id             = "example-shared-resources"
  region                 = "us-central1"
  cloud_run_service_name = "runcd"
}
```

## Usage — IAM *and* the Cloud Run services

Set `deploy_controller`/`deploy_dashboard` to also create the actual Cloud
Run v2 services. The initial container image is only used at first create —
`lifecycle.ignore_changes` deliberately excludes it, since redeploying a new
tag via `gcloud run deploy`/CI is the expected, ongoing way the image
changes; without that, the next `terraform apply` would silently revert the
running service back to whatever image was set here. The dashboard's own
service account is created automatically and wired into the controller's
`run.invoker` grant — no manual `dashboard_invoker_members` entry needed.

```hcl
module "controller" {
  source = "./terraform/controller"

  management_project_id = "example-shared-resources"
  target_projects        = ["example-sandbox"]

  deploy_controller                 = true
  controller_cloud_run_service_name = "runcd"
  controller_cloud_run_region       = "us-central1"
  controller_image                  = "us-central1-docker.pkg.dev/example-shared-resources/runcd/controller:v1"
  controller_env = {
    RUNCD_CONFIG_REPO   = "example-org/example-deployment-repo"
    RUNCD_CONFIG_BRANCH = "main"
  }
  controller_secret_env = {
    DATABASE_URL = { secret = "database-url" } # -> projects/<mgmt>/secrets/database-url
  }

  deploy_dashboard = true
  dashboard_image  = "us-central1-docker.pkg.dev/example-shared-resources/runcd/dashboard:v1"
  dashboard_env = {
    RUNCD_API_URL = "https://runcd.example.internal" # set once you know the controller's URL
  }
}
```

See [`examples/minimal`](examples/minimal) for the smallest `terraform
validate`-able call (also what CI runs — this module isn't invoked directly,
only through a caller), and [`examples/complete`](examples/complete) for one
exercising every optional variable, including both deploy toggles.

## Inputs

| Name | Description | Type | Default |
|------|-------------|------|---------|
| `management_project_id` | GCP project that owns the controller's shared service account (and, if deployed here, the Cloud Run services). | `string` | n/a |
| `service_account_id` | Account ID (local part) for the controller's shared service account. | `string` | `"runcd-controller"` |
| `target_projects` | Project IDs the controller may deploy to — one entry per project listed under any `environments[env].projects`. | `set(string)` | `[]` |
| `target_folders` | GCP folder IDs (numeric) listed under any `environments[env].folders`. Grants `resourcemanager.folderViewer` on the folder and resolves its current `ACTIVE` direct child projects at `apply` time for the same `run.developer` grant `target_projects` gets — a plan-time snapshot, not continuous reconciliation. | `set(string)` | `[]` |
| `artifact_registry_project_ids` | Projects hosting the Artifact Registry repos manifests pull from. Independent of `target_projects` — a shared registry (often in `management_project_id`) backing many deploy targets is the common shape. Defaults to `[management_project_id]`. | `set(string)` | `null` |
| `runtime_service_account_emails` | Per-project runtime service account the deployed revision runs as, keyed by target project ID, if `iam.serviceAccounts.actAs` is required (§5.5 point 2 — verify against current GCP docs). | `map(string)` | `{}` |
| `enable_pubsub_preconditions` | Grant `roles/pubsub.viewer` on every target project (including folder-resolved ones) so `pubsubTopic`/`pubsubSubscription` preconditions can be checked. Off by default — this grant is project-wide, not scoped to a specific topic/subscription. | `bool` | `false` |
| `secret_accessor_ids` | Secret Manager secrets the controller reads at boot, keyed by an arbitrary label, valued by the secret's full resource ID. Nothing is granted unless listed. | `map(string)` | `{}` |
| `cloudsql_instance_name` | Cloud SQL instance ID (in `management_project_id`) if `DATABASE_URL` is resolved via IAM database auth rather than a password. Grants `cloudsql.client`/`cloudsql.instanceUser` and creates the matching IAM database user. | `string` | `null` |
| `controller_cloud_run_service_name` / `controller_cloud_run_region` | The controller Cloud Run service's name/region — used either to deploy it (`deploy_controller`) or just to reference an already-deployed one for `dashboard_invoker_members`. | `string` | `null` |
| `dashboard_invoker_members` | IAM members granted `roles/run.invoker` scoped to just the controller Cloud Run service, on top of whatever `deploy_dashboard` wires up automatically. | `set(string)` | `[]` |
| `enable_apis` | Enable the GCP APIs this module's grants depend on (`run`, `artifactregistry`, and conditionally `pubsub`/`secretmanager`/`sqladmin`). Turn off if API enablement is centrally managed elsewhere. | `bool` | `true` |
| `deploy_controller` | Also create the controller's own Cloud Run v2 service. Requires `controller_image` plus the service name/region above. | `bool` | `false` |
| `controller_image` / `controller_env` / `controller_secret_env` / `controller_cpu` / `controller_memory` / `controller_port` / `controller_min_instances` / `controller_max_instances` / `controller_allow_unauthenticated` | Controller container/service settings. Only used if `deploy_controller` is true. | various | see `variables.tf` |
| `deploy_dashboard` | Also create the dashboard's own Cloud Run v2 service and dedicated service account (auto-granted invoker on the controller). Requires `dashboard_image`. | `bool` | `false` |
| `dashboard_cloud_run_service_name` / `dashboard_cloud_run_region` / `dashboard_service_account_id` / `dashboard_image` / `dashboard_env` / `dashboard_cpu` / `dashboard_memory` / `dashboard_port` / `dashboard_min_instances` / `dashboard_max_instances` / `dashboard_allow_unauthenticated` | Dashboard container/service settings. Only used if `deploy_dashboard` is true. | various | see `variables.tf` |

## Outputs

| Name | Description |
|------|-------------|
| `service_account_email` | The controller's shared service account — bind this identity in every target project. |
| `target_projects` | Every project actually granted access at this apply — `target_projects` plus everything resolved from `target_folders`. |
| `artifact_registry_project_ids` | Every project actually granted `artifactregistry.reader`. |
| `cloudsql_iam_user` | The Cloud SQL IAM database username to set as `CLOUDSQL_IAM_DB_USER`, if `cloudsql_instance_name` was set. |
| `controller_uri` / `dashboard_uri` | The deployed service's URL, if `deploy_controller`/`deploy_dashboard` is true. |
| `dashboard_service_account_email` | The dashboard's own service account, if `deploy_dashboard` is true. |

## Known limitation (§5.5 point 4)

One identity with standing access to every target project means a
compromised controller credential reaches every project it's bound to, not
just one. Accepted tradeoff for v1 (see the design spec); a per-project-SA
design is the documented fallback if this blast radius becomes unacceptable.
