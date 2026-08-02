output "trigger_service_account_email" {
  description = "Set the controller's RUNCD_IMAGE_EVENTS_SERVICE_ACCOUNT to exactly this value."
  value       = google_service_account.image_events.email
}

output "expected_audience" {
  description = "Set the controller's RUNCD_IMAGE_EVENTS_AUDIENCE to exactly this value — Eventarc signs the push OIDC token's audience as the destination Cloud Run service's own URL."
  value       = data.google_cloud_run_v2_service.target.uri
}
