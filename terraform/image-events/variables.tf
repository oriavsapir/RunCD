variable "project_id" {
  description = "GCP project running both the controller Cloud Run service and the Artifact Registry repositories it should react to pushes in."
  type        = string
}

variable "region" {
  description = "Region of the controller Cloud Run service and the Eventarc trigger."
  type        = string
}

variable "cloud_run_service_name" {
  description = "Name of the already-deployed controller Cloud Run service (deployed via `gcloud run deploy`, not managed by this module)."
  type        = string
  default     = "runcd"
}

variable "trigger_name" {
  description = "Name for the Eventarc trigger resource."
  type        = string
  default     = "runcd-image-events"
}

variable "service_account_id" {
  description = "Account ID (local part) for the trigger's dedicated service account."
  type        = string
  default     = "runcd-image-events"
}
