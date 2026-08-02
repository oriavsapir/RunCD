# Optional add-on (§internal/api's handleImageEvent doc comment): an
# Eventarc trigger on Artifact Registry's Cloud Audit Logs, nudging the
# already-running controller's auto-reconcile loop to run sooner than its
# next RECONCILE_INTERVAL tick after an image push. Artifact Registry has no
# native Eventarc event type of its own — only Cloud Audit Logs triggers,
# matched on serviceName/methodName (confirmed against current GCP docs:
# Docker-PutManifest is the audit log entry for a completed image push,
# after the preceding Docker-StartUpload/Docker-FinishUpload blob uploads).
#
# The controller service itself is deployed via `gcloud run deploy`, not
# managed by this module (or any Terraform in this repo) — this data source
# only reads its already-deployed state, to surface the exact audience URL
# the controller's RUNCD_IMAGE_EVENTS_AUDIENCE must be set to.
data "google_cloud_run_v2_service" "target" {
  project  = var.project_id
  location = var.region
  name     = var.cloud_run_service_name
}

# A dedicated identity, not the shared controller-sa module's SA — this one
# only ever needs to invoke the controller's HTTP endpoint and receive
# Eventarc events, nothing the controller SA's own broader deploy grants
# (roles/run.developer across every target project) should be conflated
# with.
resource "google_service_account" "image_events" {
  project      = var.project_id
  account_id   = var.service_account_id
  display_name = "runcd image-events Eventarc trigger"
}

# Required for any Eventarc trigger's own service account, regardless of
# destination type.
resource "google_project_iam_member" "event_receiver" {
  project = var.project_id
  role    = "roles/eventarc.eventReceiver"
  member  = "serviceAccount:${google_service_account.image_events.email}"
}

# Scoped to just the controller service, not project-wide roles/run.invoker
# — this identity's only job is calling POST /api/events/image on this one
# service.
resource "google_cloud_run_v2_service_iam_member" "run_invoker" {
  project  = var.project_id
  location = var.region
  name     = var.cloud_run_service_name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.image_events.email}"
}

# One-time project setup some older projects still need — see
# https://cloud.google.com/eventarc/docs/creating-triggers-terraform:
# "If the Pub/Sub service agent was created on or before April 8, 2021, and
# the service account does not have the Cloud Pub/Sub Service Agent role,
# grant the Service Account Token Creator role." Cloud Audit Logs triggers
# route through Pub/Sub internally even though this module's own trigger
# destination is a direct Cloud Run push, not a Pub/Sub subscription — this
# grant is what lets that internal routing mint the OIDC token Eventarc
# attaches to the push. Harmless to grant unconditionally: idempotent, and
# a no-op on any project where the Pub/Sub service agent already has it via
# the (newer, default) Cloud Pub/Sub Service Agent role instead.
resource "google_project_iam_member" "pubsub_token_creator" {
  project = var.project_id
  role    = "roles/iam.serviceAccountTokenCreator"
  member  = "serviceAccount:service-${data.google_project.this.number}@gcp-sa-pubsub.iam.gserviceaccount.com"
}

data "google_project" "this" {
  project_id = var.project_id
}

# Docker-PutManifest is a Data Access ("Data Write") audit log entry, and
# Data Access logs are OFF by default for every service except some
# BigQuery ones — without this, the trigger below is created and looks
# healthy, but Artifact Registry never actually emits the log entries it's
# listening for, so it silently never fires. Scoped to just this one
# service, not "allServices" — this resource is authoritative for whatever
# it's scoped to, so keeping it per-service means it can't clobber some
# unrelated service's own audit config.
resource "google_project_iam_audit_config" "artifact_registry_data_write" {
  project = var.project_id
  service = "artifactregistry.googleapis.com"
  audit_log_config {
    log_type = "DATA_WRITE"
  }
}

resource "google_eventarc_trigger" "image_push" {
  name     = var.trigger_name
  project  = var.project_id
  location = var.region

  matching_criteria {
    attribute = "type"
    value     = "google.cloud.audit.log.v1.written"
  }
  matching_criteria {
    attribute = "serviceName"
    value     = "artifactregistry.googleapis.com"
  }
  matching_criteria {
    attribute = "methodName"
    value     = "Docker-PutManifest"
  }

  destination {
    cloud_run_service {
      service = data.google_cloud_run_v2_service.target.name
      region  = var.region
    }
  }

  service_account = google_service_account.image_events.email

  depends_on = [
    google_project_iam_member.event_receiver,
    google_cloud_run_v2_service_iam_member.run_invoker,
    google_project_iam_member.pubsub_token_creator,
    google_project_iam_audit_config.artifact_registry_data_write,
  ]
}
