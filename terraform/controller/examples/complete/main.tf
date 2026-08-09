# Exercises every variable the minimal example leaves at its default —
# target_folders, runtime_service_account_emails, enable_pubsub_preconditions,
# secret_accessor_ids, cloudsql_instance_name, artifact_registry_project_ids,
# and both deploy_controller/deploy_dashboard's Cloud Run + IAM wiring — so
# `terraform validate` actually parses these paths instead of only ever
# seeing management_project_id + target_projects (see CI's `terraform` job).

module "controller" {
  source = "../.."

  management_project_id = "example-mgmt-project"
  target_projects       = ["example-target-project-a", "example-target-project-b"]
  target_folders        = ["123456789012"]

  runtime_service_account_emails = {
    "example-target-project-a" = "runtime@example-target-project-a.iam.gserviceaccount.com"
  }

  enable_pubsub_preconditions = true

  secret_accessor_ids = {
    "database_url"   = "projects/example-mgmt-project/secrets/database-url"
    "github_app_pem" = "projects/example-mgmt-project/secrets/github-app-pem"
  }

  artifact_registry_project_ids = ["example-mgmt-project"]

  cloudsql_instance_name = "example-runcd-db"

  controller_cloud_run_service_name = "runcd-controller"
  controller_cloud_run_region       = "us-central1"
  dashboard_invoker_members = [
    "serviceAccount:some-other-caller@example-mgmt-project.iam.gserviceaccount.com",
  ]

  deploy_controller = true
  controller_image  = "us-central1-docker.pkg.dev/example-mgmt-project/runcd/controller:v1"
  controller_env = {
    RUNCD_CONFIG_REPO   = "example-org/example-deployment-repo"
    RUNCD_CONFIG_BRANCH = "main"
  }
  controller_secret_env = {
    DATABASE_URL = { secret = "database-url" }
  }

  deploy_dashboard = true
  dashboard_image  = "us-central1-docker.pkg.dev/example-mgmt-project/runcd/dashboard:v1"
}

output "controller_service_account_email" {
  value = module.controller.service_account_email
}

output "cloudsql_iam_user" {
  value = module.controller.cloudsql_iam_user
}

output "controller_uri" {
  value = module.controller.controller_uri
}

output "dashboard_uri" {
  value = module.controller.dashboard_uri
}
