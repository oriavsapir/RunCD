variable "management_project_id" {
  description = "GCP project that owns the controller's shared service account."
  type        = string
}

variable "service_account_id" {
  description = "Account ID (local part) for the controller's shared service account."
  type        = string
  default     = "runcd-controller"
}

variable "target_projects" {
  description = <<-EOT
    GCP project IDs the controller may deploy to — one entry per project
    listed under any environments[env].projects in runcd.yaml (§5.1, §5.5).
    Adding a project here is the "one IAM binding" provisioning step in §5.5.
  EOT
  type        = set(string)
  default     = []
}

variable "target_folders" {
  description = <<-EOT
    GCP folder IDs (numeric, e.g. "123456789012" — not "folders/123456789012")
    listed under any environments[env].folders in runcd.yaml. The module
    grants the controller SA read access to each folder (so
    internal/folders' runtime resolution can list its contents) AND
    resolves its *current* direct child projects at `terraform apply` time
    (via the google_projects data source) to grant the same
    roles/run.developer every target_projects entry gets.

    This is a plan-time snapshot, not continuous reconciliation: a project
    added to the folder after the last `apply` shows up in RunCD's own
    sync-unit list within one reconcile tick (internal/folders resolves
    fresh every hot-reload), but the controller SA has no run.developer on
    it — and every deploy to it fails — until `terraform apply` runs again
    against this module.
  EOT
  type        = set(string)
  default     = []
}

variable "runtime_service_account_emails" {
  description = <<-EOT
    Per-project runtime service account the deployed Cloud Run revision runs
    as, if the controller must be granted iam.serviceAccounts.actAs on it
    (§5.5 point 2 — confirm this requirement against current GCP docs before
    Phase 0 finalizes; not all target projects necessarily need this).
    Keyed by target project ID; a project with no entry gets no actAs grant.
  EOT
  type        = map(string)
  default     = {}
}
