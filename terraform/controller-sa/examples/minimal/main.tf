# Minimal usage example — also what CI actually `init`/`validate`s, since a
# reusable module isn't run directly; this is its root caller.

module "controller_sa" {
  source = "../.."

  management_project_id = "example-mgmt-project"
  target_projects       = ["example-target-project-a", "example-target-project-b"]
}

output "controller_service_account_email" {
  value = module.controller_sa.service_account_email
}
