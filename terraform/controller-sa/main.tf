# §5.5: one shared controller service account, granted roles/run.developer
# directly in every target project — not a dedicated per-project runner SA.
# Module shape only for Phase 0; not yet invoked against a real project.

resource "google_service_account" "controller" {
  project      = var.management_project_id
  account_id   = var.service_account_id
  display_name = "runcd controller (shared, all target projects)"
}

# One IAM binding per project add (§5.5 point 1) — deploy-only, never project
# admin (§5.5 point 4's stated mitigation for the shared-SA blast radius).
resource "google_project_iam_member" "run_developer" {
  for_each = var.target_projects

  project = each.value
  role    = "roles/run.developer"
  member  = "serviceAccount:${google_service_account.controller.email}"
}

# §5.5 point 2: deploying a revision that runs as a specific runtime SA
# typically also requires actAs on that SA. Granted only where a runtime SA
# is declared for that project.
resource "google_service_account_iam_member" "act_as_runtime_sa" {
  for_each = var.runtime_service_account_emails

  service_account_id = "projects/${each.key}/serviceAccounts/${each.value}"
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.controller.email}"
}
