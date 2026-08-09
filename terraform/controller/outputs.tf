output "service_account_email" {
  description = "The controller's shared service account — bind this identity in every target project."
  value       = google_service_account.controller.email
}

output "target_projects" {
  description = "Every project the controller SA was actually granted run.developer on — target_projects plus everything resolved from target_folders at this apply."
  value       = local.all_target_projects
}

output "artifact_registry_project_ids" {
  description = "Every project the controller SA was actually granted artifactregistry.reader on."
  value       = local.artifact_registry_project_ids
}

output "cloudsql_iam_user" {
  description = "The Cloud SQL IAM database username to set as CLOUDSQL_IAM_DB_USER, if cloudsql_instance_name was set. Null otherwise."
  value       = var.cloudsql_instance_name != null ? google_sql_user.controller_iam[0].name : null
}

output "controller_uri" {
  description = "The controller Cloud Run service's URL, if deploy_controller is true. Set RUNCD_IMAGE_EVENTS_AUDIENCE (plus the /api/events/image path) or RUNCD_API_URL for the dashboard from this. Null otherwise."
  value       = var.deploy_controller ? google_cloud_run_v2_service.controller[0].uri : null
}

output "dashboard_uri" {
  description = "The dashboard Cloud Run service's URL, if deploy_dashboard is true. Null otherwise."
  value       = var.deploy_dashboard ? google_cloud_run_v2_service.dashboard[0].uri : null
}

output "dashboard_service_account_email" {
  description = "The dashboard's own service account, if deploy_dashboard is true — already granted run.invoker on the controller. Null otherwise."
  value       = var.deploy_dashboard ? google_service_account.dashboard[0].email : null
}
