# image-events

Optional add-on: an Eventarc trigger on Artifact Registry's Cloud Audit Logs
(Artifact Registry has no native Eventarc event type of its own) that calls
the already-deployed controller's `POST /api/events/image` on every
completed image push (audit log method `Docker-PutManifest`), nudging the
auto-reconcile loop to run within seconds instead of waiting out the rest of
`RECONCILE_INTERVAL`.

**Status:** module shape only, not yet invoked against a real project — see
`examples/minimal`.

Also enables Data Access (`DATA_WRITE`) audit logging for
`artifactregistry.googleapis.com` in the project — Data Access logs are off
by default for every service except some BigQuery ones, and
`Docker-PutManifest` is one, so without this the trigger exists but
Artifact Registry never actually emits the log entries it's listening for.

Purely additive: the controller's `POST /api/events/image` route is always
registered and does nothing unless both `RUNCD_IMAGE_EVENTS_AUDIENCE` and
`RUNCD_IMAGE_EVENTS_SERVICE_ACCOUNT` are set on it — deleting this module's
resources (or never applying it) leaves the controller exactly as it was
before, polling on `RECONCILE_INTERVAL` alone. This module does not manage
the controller Cloud Run service itself (that's deployed via
`gcloud run deploy`, outside Terraform) — it only reads that service's
existing state (via a `google_cloud_run_v2_service` data source) to route
the trigger and surface the audience URL below.

## Usage

```hcl
module "image_events" {
  source = "./terraform/image-events"

  project_id = "example-shared-resources"
  region     = "us-central1"
  # Optional — defaults shown:
  # cloud_run_service_name = "runcd"
  # trigger_name           = "runcd-image-events"
  # service_account_id     = "runcd-image-events"
}
```

After `apply`, set on the controller (and redeploy for it to take effect):

```
RUNCD_IMAGE_EVENTS_AUDIENCE=<module.image_events.expected_audience>
RUNCD_IMAGE_EVENTS_SERVICE_ACCOUNT=<module.image_events.trigger_service_account_email>
```

Both must be set together — either one left unset (or wrong) means the
route rejects every event, which fails safe: the controller just keeps
polling on `RECONCILE_INTERVAL` as if this module had never been applied.

See [`examples/minimal`](examples/minimal) for a complete, `terraform
validate`-able example (also what CI runs).

## What this doesn't do

The handler this triggers (`internal/api`'s `handleImageEvent`) deliberately
ignores the event body entirely — it doesn't parse which specific image or
sync unit changed, it just makes the leader's next reconcile pass happen
sooner. `reconcile.RunOnce` already only redeploys units that are actually
`OutOfSync`, so an extra early pass over the whole fleet is harmless, just
slightly wasteful at very large scale. This is not a git-write-back
image-updater (à la `argocd-image-updater`) — RunCD manifests stay
digest-pinned, and nothing here writes a new digest to `service.yaml`; it
only makes existing digest drift (from a manifest change already in git)
visible sooner.

## Inputs

| Name | Description | Type | Default |
|------|-------------|------|---------|
| `project_id` | GCP project running both the controller and the Artifact Registry repositories to react to. | `string` | n/a |
| `region` | Region of the controller Cloud Run service and the Eventarc trigger. | `string` | n/a |
| `cloud_run_service_name` | Name of the already-deployed controller Cloud Run service. | `string` | `"runcd"` |
| `trigger_name` | Name for the Eventarc trigger resource. | `string` | `"runcd-image-events"` |
| `service_account_id` | Account ID (local part) for the trigger's dedicated service account. | `string` | `"runcd-image-events"` |
| `manage_audit_config` | Manage the `DATA_WRITE` audit log config for `artifactregistry.googleapis.com` in this project. Set `false` if that config is already managed elsewhere — this resource is authoritative for whatever it's scoped to and will otherwise fight over/overwrite an existing config. | `bool` | `true` |
| `enable_apis` | Enable `eventarc.googleapis.com` in `project_id`. Turn off if API enablement is centrally managed elsewhere. | `bool` | `true` |

## Outputs

| Name | Description |
|------|-------------|
| `trigger_service_account_email` | Set the controller's `RUNCD_IMAGE_EVENTS_SERVICE_ACCOUNT` to exactly this. |
| `expected_audience` | Set the controller's `RUNCD_IMAGE_EVENTS_AUDIENCE` to exactly this. |
