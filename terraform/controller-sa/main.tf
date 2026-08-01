# §5.5: one shared controller service account, granted roles/run.developer
# directly in every target project — not a dedicated per-project runner SA.
# Module shape only for Phase 0; not yet invoked against a real project.

resource "google_service_account" "controller" {
  project      = var.management_project_id
  account_id   = var.service_account_id
  display_name = "runcd controller (shared, all target projects)"
}

# The current direct child projects of each target_folders entry, resolved
# at `terraform apply` time — see target_folders' doc comment on why this
# is a snapshot, not continuous reconciliation.
data "google_projects" "folder_members" {
  for_each = var.target_folders

  filter = "parent.id:${each.value} parent.type:folder"
}

locals {
  # Every project ID resolved from any target_folders entry, flattened and
  # deduped against target_projects itself (for_each over a set can't have
  # duplicate keys).
  folder_member_project_ids = toset(flatten([
    for result in data.google_projects.folder_members : [
      for p in result.projects : p.project_id
    ]
  ]))
  all_target_projects = setunion(var.target_projects, local.folder_member_project_ids)
}

# One IAM binding per project — deploy-only, never project admin (§5.5
# point 4's stated mitigation for the shared-SA blast radius). Covers both
# target_projects and every project currently resolved from target_folders.
resource "google_project_iam_member" "run_developer" {
  for_each = local.all_target_projects

  project = each.value
  role    = "roles/run.developer"
  member  = "serviceAccount:${google_service_account.controller.email}"
}

# Read access on each target folder itself — internal/folders' runtime
# resolution (ListProjects) needs this independent of the run.developer
# grants above, which are on the folder's *contents*, not the folder
# resource. roles/resourcemanager.folderViewer is Google's documented
# minimal predefined role for "list/get a folder and what's under it" —
# confirm against current GCP docs before relying on this in a real
# deployment, same caveat as runtime_service_account_emails above.
resource "google_folder_iam_member" "folder_viewer" {
  for_each = var.target_folders

  folder = "folders/${each.value}"
  role   = "roles/resourcemanager.folderViewer"
  member = "serviceAccount:${google_service_account.controller.email}"
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
