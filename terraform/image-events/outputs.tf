output "trigger_service_account_email" {
  description = "Set the controller's RUNCD_IMAGE_EVENTS_SERVICE_ACCOUNT to exactly this value."
  value       = google_service_account.image_events.email
}

output "expected_audience" {
  description = <<-EOT
    Set the controller's RUNCD_IMAGE_EVENTS_AUDIENCE to exactly this value.

    NOT just the bare Cloud Run service URL — confirmed against a real
    end-to-end test push that Eventarc's Pub/Sub push subscription signs
    the OIDC token's audience as the *full destination URL, including the
    path* the trigger delivers to (local.destination_path). Setting
    RUNCD_IMAGE_EVENTS_AUDIENCE to the bare service URL passes
    `terraform validate` and looks reasonable, but every real event gets
    rejected by the controller's own idtoken.Validate audience check —
    this was caught by an actual live push, not by inspection.
  EOT
  value       = "${data.google_cloud_run_v2_service.target.uri}${local.destination_path}"
}
