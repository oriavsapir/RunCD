# §5.5: one shared controller service account, granted roles/run.developer
# directly in every target project — not a dedicated per-project runner SA.

# Resolved at apply time — a plan-time snapshot, not continuous reconciliation.
data "google_projects" "folder_members" {
  for_each = var.target_folders

  # ACTIVE only, matching internal/folders' own runtime resolution.
  filter = "parent.id:${each.value} parent.type:folder lifecycleState:ACTIVE"
}

locals {
  folder_member_project_ids = toset(flatten([
    for result in data.google_projects.folder_members : [
      for p in result.projects : p.project_id
    ]
  ]))
  all_target_projects = setunion(var.target_projects, local.folder_member_project_ids)

  # A shared registry (often the management project) backing many deploy
  # targets is the common shape, so this is independent of all_target_projects.
  artifact_registry_project_ids = coalesce(var.artifact_registry_project_ids, toset([var.management_project_id]))

  # artifactregistry.googleapis.com is enabled separately below, on
  # artifact_registry_project_ids, not here.
  target_project_services = toset(concat(
    ["run.googleapis.com"],
    var.enable_pubsub_preconditions ? ["pubsub.googleapis.com"] : []
  ))
  target_project_api_pairs = var.enable_apis ? {
    for pair in setproduct(local.all_target_projects, local.target_project_services) :
    "${pair[0]}/${pair[1]}" => { project = pair[0], service = pair[1] }
  } : {}

  management_project_services = toset(concat(
    ["iam.googleapis.com", "cloudresourcemanager.googleapis.com"],
    length(var.secret_accessor_ids) > 0 ? ["secretmanager.googleapis.com"] : [],
    var.cloudsql_instance_name != null ? ["sqladmin.googleapis.com"] : [],
  ))
}

resource "google_project_service" "target_project_apis" {
  for_each = local.target_project_api_pairs

  project = each.value.project
  service = each.value.service
  # Never disable on destroy: other workloads in the same project may
  # depend on the same API having been enabled by someone else first.
  disable_on_destroy = false
}

resource "google_project_service" "management_project_apis" {
  for_each = var.enable_apis ? local.management_project_services : []

  project            = var.management_project_id
  service            = each.value
  disable_on_destroy = false
}

resource "google_project_service" "artifact_registry_apis" {
  for_each = var.enable_apis ? local.artifact_registry_project_ids : []

  project            = each.value
  service            = "artifactregistry.googleapis.com"
  disable_on_destroy = false
}

resource "google_service_account" "controller" {
  project      = var.management_project_id
  account_id   = var.service_account_id
  display_name = "runcd controller (shared, all target projects)"

  depends_on = [google_project_service.management_project_apis]
}

# One IAM binding per project — deploy-only, never project admin (§5.5
# point 4's stated mitigation for the shared-SA blast radius). Covers both
# target_projects and every project currently resolved from target_folders.
resource "google_project_iam_member" "run_developer" {
  for_each = local.all_target_projects

  project = each.value
  role    = "roles/run.developer"
  member  = "serviceAccount:${google_service_account.controller.email}"

  depends_on = [google_project_service.target_project_apis]
}

# Backs internal/registry.Client (imageupdater + cloudrun's live tag
# resolution) — granted unconditionally, not add-on-only. On
# artifact_registry_project_ids, not all_target_projects.
resource "google_project_iam_member" "artifactregistry_reader" {
  for_each = local.artifact_registry_project_ids

  project = each.value
  role    = "roles/artifactregistry.reader"
  member  = "serviceAccount:${google_service_account.controller.email}"

  depends_on = [google_project_service.artifact_registry_apis]
}

# The actual container pull at deploy time is done by each target project's
# Cloud Run Service Agent, not the controller SA above. GCP auto-grants this
# for same-project images but not cross-project — without it, every deploy
# into a target project fails to pull an image from a different registry
# project (the default shape, since artifact_registry_project_ids defaults
# to management_project_id).
data "google_project" "target_project_details" {
  for_each   = local.all_target_projects
  project_id = each.value
}

locals {
  run_service_agent_registry_grants = {
    for pair in setproduct(local.all_target_projects, local.artifact_registry_project_ids) :
    "${pair[0]}/${pair[1]}" => {
      registry_project = pair[1]
      member           = "serviceAccount:service-${data.google_project.target_project_details[pair[0]].number}@serverless-robot-prod.iam.gserviceaccount.com"
    }
  }
}

resource "google_project_iam_member" "run_service_agent_artifactregistry_reader" {
  for_each = local.run_service_agent_registry_grants

  project = each.value.registry_project
  role    = "roles/artifactregistry.reader"
  member  = each.value.member

  depends_on = [google_project_service.target_project_apis, google_project_service.artifact_registry_apis]
}

# Read access on the folder itself (its contents get run_developer above,
# not the folder resource) — internal/folders' ListProjects needs this.
resource "google_folder_iam_member" "folder_viewer" {
  for_each = var.target_folders

  folder = "folders/${each.value}"
  role   = "roles/resourcemanager.folderViewer"
  member = "serviceAccount:${google_service_account.controller.email}"
}

# §5.5 point 2: deploying a revision as a specific runtime SA also requires
# actAs on that SA.
resource "google_service_account_iam_member" "act_as_runtime_sa" {
  for_each = var.runtime_service_account_emails

  service_account_id = "projects/${each.key}/serviceAccounts/${each.value}"
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.controller.email}"
}

# internal/precondition.GCPChecker's Topic/Subscription.Exists() checks
# (§5.10) need pubsub.viewer or every precondition 403s.
resource "google_project_iam_member" "pubsub_viewer" {
  for_each = var.enable_pubsub_preconditions ? local.all_target_projects : []

  project = each.value
  role    = "roles/pubsub.viewer"
  member  = "serviceAccount:${google_service_account.controller.email}"

  depends_on = [google_project_service.target_project_apis]
}

# Per-secret, not project-wide — nothing granted unless listed.
resource "google_secret_manager_secret_iam_member" "secret_accessor" {
  for_each = var.secret_accessor_ids

  secret_id = each.value
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.controller.email}"

  depends_on = [google_project_service.management_project_apis]
}

# Cloud SQL IAM database auth (CLOUDSQL_IAM_DB_USER path) — a plain
# password DATABASE_URL gets none of this.
resource "google_project_iam_member" "cloudsql_client" {
  count = var.cloudsql_instance_name != null ? 1 : 0

  project = var.management_project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${google_service_account.controller.email}"

  depends_on = [google_project_service.management_project_apis]
}

resource "google_project_iam_member" "cloudsql_instance_user" {
  count = var.cloudsql_instance_name != null ? 1 : 0

  project = var.management_project_id
  role    = "roles/cloudsql.instanceUser"
  member  = "serviceAccount:${google_service_account.controller.email}"

  depends_on = [google_project_service.management_project_apis]
}

# GCP convention: the IAM DB username is the SA email with only the
# ".gserviceaccount.com" suffix stripped.
resource "google_sql_user" "controller_iam" {
  count = var.cloudsql_instance_name != null ? 1 : 0

  project  = var.management_project_id
  instance = var.cloudsql_instance_name
  name     = trimsuffix(google_service_account.controller.email, ".gserviceaccount.com")
  type     = "CLOUD_IAM_SERVICE_ACCOUNT"

  depends_on = [google_project_iam_member.cloudsql_client]
}

# --- Optional: deploy the controller's own Cloud Run service -----------
# Image excluded from drift detection — CI redeploys it, not Terraform.
resource "google_cloud_run_v2_service" "controller" {
  count = var.deploy_controller ? 1 : 0

  name     = var.controller_cloud_run_service_name
  project  = var.management_project_id
  location = var.controller_cloud_run_region
  ingress  = "INGRESS_TRAFFIC_ALL"

  template {
    service_account = google_service_account.controller.email

    scaling {
      min_instance_count = var.controller_min_instances
      max_instance_count = var.controller_max_instances
    }

    containers {
      image = var.controller_image

      ports {
        container_port = var.controller_port
      }

      resources {
        limits = {
          cpu    = var.controller_cpu
          memory = var.controller_memory
        }
      }

      dynamic "env" {
        for_each = var.controller_env
        content {
          name  = env.key
          value = env.value
        }
      }

      dynamic "env" {
        for_each = var.controller_secret_env
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = env.value.secret
              version = env.value.version
            }
          }
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [template[0].containers[0].image]
  }

  depends_on = [
    google_project_service.target_project_apis,
    google_project_iam_member.run_developer,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "controller_public" {
  count = var.deploy_controller && var.controller_allow_unauthenticated ? 1 : 0

  project  = var.management_project_id
  location = var.controller_cloud_run_region
  name     = google_cloud_run_v2_service.controller[0].name
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# --- Optional: deploy the dashboard's own Cloud Run service -------------
# Own dedicated SA, distinct from the controller's broader-scoped one —
# auto-wired into the controller's run.invoker grant below.
resource "google_service_account" "dashboard" {
  count = var.deploy_dashboard ? 1 : 0

  project      = var.management_project_id
  account_id   = var.dashboard_service_account_id
  display_name = "runcd dashboard"

  depends_on = [google_project_service.management_project_apis]
}

resource "google_cloud_run_v2_service" "dashboard" {
  count = var.deploy_dashboard ? 1 : 0

  name     = var.dashboard_cloud_run_service_name
  project  = var.management_project_id
  location = coalesce(var.dashboard_cloud_run_region, var.controller_cloud_run_region)
  ingress  = "INGRESS_TRAFFIC_ALL"

  template {
    service_account = google_service_account.dashboard[0].email

    scaling {
      min_instance_count = var.dashboard_min_instances
      max_instance_count = var.dashboard_max_instances
    }

    containers {
      image = var.dashboard_image

      ports {
        container_port = var.dashboard_port
      }

      resources {
        limits = {
          cpu    = var.dashboard_cpu
          memory = var.dashboard_memory
        }
      }

      dynamic "env" {
        for_each = var.dashboard_env
        content {
          name  = env.key
          value = env.value
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [template[0].containers[0].image]
  }
}

resource "google_cloud_run_v2_service_iam_member" "dashboard_public" {
  count = var.deploy_dashboard && var.dashboard_allow_unauthenticated ? 1 : 0

  project  = var.management_project_id
  location = coalesce(var.dashboard_cloud_run_region, var.controller_cloud_run_region)
  name     = google_cloud_run_v2_service.dashboard[0].name
  role     = "roles/run.invoker"
  member   = "allUsers"
}

locals {
  # A map with static keys, not a set: the dashboard SA's email is only
  # known after apply, and for_each requires its keys (not values) known at
  # plan time — a set built from an apply-time value breaks a first apply.
  dashboard_invoker_members_effective = merge(
    { for m in var.dashboard_invoker_members : m => m },
    var.deploy_dashboard ? { "__dashboard_sa" = "serviceAccount:${google_service_account.dashboard[0].email}" } : {},
  )
}

# Scoped to just the controller service, not project-wide run.invoker.
resource "google_cloud_run_v2_service_iam_member" "dashboard_invoker" {
  for_each = local.dashboard_invoker_members_effective

  project  = var.management_project_id
  location = var.controller_cloud_run_region
  name     = var.controller_cloud_run_service_name
  role     = "roles/run.invoker"
  member   = each.value

  depends_on = [google_cloud_run_v2_service.controller]
}
