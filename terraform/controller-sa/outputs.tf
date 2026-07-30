output "service_account_email" {
  description = "The controller's shared service account — bind this identity in every target project."
  value       = google_service_account.controller.email
}
