variable "project_id" {
  description = "GCP project running both the controller Cloud Run service and the Artifact Registry repositories it should react to pushes in."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.project_id))
    error_message = "project_id must be a valid GCP project ID."
  }
}

variable "region" {
  description = "Region of the controller Cloud Run service and the Eventarc trigger."
  type        = string

  validation {
    condition     = can(regex("^[a-z]+-[a-z]+[0-9]$", var.region))
    error_message = "region must be a GCP region, e.g. \"us-central1\"."
  }
}

variable "cloud_run_service_name" {
  description = "Name of the already-deployed controller Cloud Run service (deployed via `gcloud run deploy`, not managed by this module)."
  type        = string
  default     = "runcd"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,61}[a-z0-9]$", var.cloud_run_service_name))
    error_message = "cloud_run_service_name must be a valid Cloud Run service name."
  }
}

variable "trigger_name" {
  description = "Name for the Eventarc trigger resource."
  type        = string
  default     = "runcd-image-events"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,61}[a-z0-9]$", var.trigger_name))
    error_message = "trigger_name must be a valid Eventarc trigger name."
  }
}

variable "service_account_id" {
  description = "Account ID (local part) for the trigger's dedicated service account."
  type        = string
  default     = "runcd-image-events"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.service_account_id))
    error_message = "service_account_id must be 6-30 chars, lowercase letters/digits/hyphens, starting with a letter (GCP service account ID rules)."
  }
}

variable "manage_audit_config" {
  description = <<-EOT
    Manage the DATA_WRITE audit log config for artifactregistry.googleapis.com
    in this project — required for the trigger to ever fire (Docker-PutManifest
    is a Data Access log entry, off by default). This resource is
    authoritative for whatever it's scoped to: if the project already has its
    own DATA_READ/DATA_WRITE audit config for this service managed elsewhere,
    applying this module's copy will fight over/overwrite it. Set to false
    and manage that audit config yourself (with DATA_WRITE included) if that's
    your situation — the trigger will silently never fire without it either
    way, this only changes who owns the config resource.
  EOT
  type        = bool
  default     = true
}

variable "enable_apis" {
  description = "Enable eventarc.googleapis.com in project_id. Turn off if API enablement is centrally managed elsewhere."
  type        = bool
  default     = true
}
