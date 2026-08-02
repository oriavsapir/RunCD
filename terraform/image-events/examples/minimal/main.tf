# Minimal usage example — also what CI actually `init`/`validate`s, since a
# reusable module isn't run directly; this is its root caller.

module "image_events" {
  source = "../.."

  project_id = "example-shared-resources"
  region     = "us-central1"
}

output "trigger_service_account_email" {
  value = module.image_events.trigger_service_account_email
}

output "expected_audience" {
  value = module.image_events.expected_audience
}
