variable "management_project_id" {
  description = "GCP project that owns the controller's shared service account."
  type        = string
}

variable "service_account_id" {
  description = "Account ID (local part) for the controller's shared service account."
  type        = string
  default     = "argorun-controller"
}

variable "target_projects" {
  description = <<-EOT
    GCP project IDs the controller may deploy to — one entry per project
    listed under any environments[env].projects in argorun.yaml (§5.1, §5.5).
    Adding a project here is the "one IAM binding" provisioning step in §5.5.
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
