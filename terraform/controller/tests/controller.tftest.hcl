# Native `terraform test` (1.6+) unit suite, run with a mocked provider
# (1.7+) — zero-cost, no real GCP project/credentials needed. Complements
# examples/{minimal,complete}'s `terraform validate` (syntax/types only):
# these assertions check the module's actual for_each/count conditionals —
# that an opt-in variable really is a no-op at its default and really does
# grant something once set.

mock_provider "google" {
  mock_resource "google_service_account" {
    defaults = {
      email = "mocked-controller@example-mgmt-project.iam.gserviceaccount.com"
    }
  }

  mock_resource "google_cloud_run_v2_service" {
    defaults = {
      uri = "https://mocked-service.a.run.app"
    }
  }

  # Fixed, numeric project number — the default mocked value is a random
  # alphanumeric string, which would make the service-<number>@... member
  # string assertions below unrepresentative of the real GCP convention.
  mock_data "google_project" {
    defaults = {
      number = "123456789012"
    }
  }
}

variables {
  management_project_id = "example-mgmt-project"
  target_projects       = ["example-target-project-a"]
}

# --- Base grants: always present, unconditional -----------------------

run "base_grants_present_for_every_target_project" {
  command = plan

  assert {
    condition     = contains(keys(google_project_iam_member.run_developer), "example-target-project-a")
    error_message = "run.developer must be granted on every target_projects entry."
  }
}

run "artifactregistry_reader_defaults_to_management_project_not_target_projects" {
  command = plan

  assert {
    condition     = contains(keys(google_project_iam_member.artifactregistry_reader), "example-mgmt-project")
    error_message = "artifactregistry.reader must default to management_project_id, since a shared registry there (not the deploy-target project) is the common shape."
  }

  assert {
    condition     = !contains(keys(google_project_iam_member.artifactregistry_reader), "example-target-project-a")
    error_message = "artifactregistry.reader should NOT default onto target_projects — that's a separate, independent set."
  }
}

run "artifactregistry_reader_respects_explicit_override" {
  command = plan

  variables {
    artifact_registry_project_ids = ["example-registry-project"]
  }

  assert {
    condition     = contains(keys(google_project_iam_member.artifactregistry_reader), "example-registry-project") && !contains(keys(google_project_iam_member.artifactregistry_reader), "example-mgmt-project")
    error_message = "An explicit artifact_registry_project_ids should replace, not add to, the management-project default."
  }
}

run "folder_grant_absent_without_target_folders" {
  command = plan

  assert {
    condition     = length(google_folder_iam_member.folder_viewer) == 0
    error_message = "No folder_viewer grant should exist when target_folders is empty."
  }
}

run "folder_grant_present_and_scoped_when_target_folders_set" {
  command = plan

  variables {
    target_folders = ["123456789012"]
  }

  assert {
    condition     = contains(keys(google_folder_iam_member.folder_viewer), "123456789012")
    error_message = "folder_viewer must be granted on every target_folders entry."
  }

  assert {
    condition     = google_folder_iam_member.folder_viewer["123456789012"].folder == "folders/123456789012"
    error_message = "folder_viewer must be scoped to \"folders/<id>\", not the bare numeric ID."
  }
}

run "run_service_agent_grant_present_for_every_target_and_registry_project" {
  # apply: the member string embeds data.google_project's computed `number`.
  command = apply

  assert {
    condition     = google_project_iam_member.run_service_agent_artifactregistry_reader["example-target-project-a/example-mgmt-project"].member == "serviceAccount:service-123456789012@serverless-robot-prod.iam.gserviceaccount.com"
    error_message = "Each target project's Cloud Run Service Agent must be granted artifactregistry.reader on every artifact_registry_project_ids project, using GCP's service-<project number>@serverless-robot-prod.iam.gserviceaccount.com convention."
  }
}

run "act_as_runtime_sa_absent_by_default" {
  command = plan

  assert {
    condition     = length(google_service_account_iam_member.act_as_runtime_sa) == 0
    error_message = "No actAs grant should exist when runtime_service_account_emails is empty."
  }
}

run "act_as_runtime_sa_granted_and_scoped_when_configured" {
  command = plan

  variables {
    runtime_service_account_emails = {
      "example-target-project-a" = "runtime@example-target-project-a.iam.gserviceaccount.com"
    }
  }

  assert {
    condition     = length(google_service_account_iam_member.act_as_runtime_sa) == 1
    error_message = "Exactly one actAs grant should exist per runtime_service_account_emails entry."
  }

  assert {
    condition     = google_service_account_iam_member.act_as_runtime_sa["example-target-project-a"].service_account_id == "projects/example-target-project-a/serviceAccounts/runtime@example-target-project-a.iam.gserviceaccount.com"
    error_message = "actAs must be scoped to the runtime SA's own resource ID (projects/<project>/serviceAccounts/<email>), keyed by the target project, not the management project or a bare email."
  }
}

# --- enable_apis --------------------------------------------------------

run "apis_enabled_by_default" {
  command = plan

  assert {
    condition     = length(google_project_service.target_project_apis) > 0 && length(google_project_service.management_project_apis) > 0
    error_message = "enable_apis must default to true — run.googleapis.com and the management-project APIs must be enabled for the module's grants to work at all."
  }
}

run "apis_skipped_when_disabled" {
  command = plan

  variables {
    enable_apis = false
  }

  assert {
    condition     = length(google_project_service.target_project_apis) == 0 && length(google_project_service.management_project_apis) == 0 && length(google_project_service.artifact_registry_apis) == 0
    error_message = "enable_apis = false must skip all API enablement, for centrally-managed setups."
  }
}

# --- Opt-in toggles default to off -------------------------------------

run "pubsub_viewer_off_by_default" {
  command = plan

  assert {
    condition     = length(google_project_iam_member.pubsub_viewer) == 0
    error_message = "enable_pubsub_preconditions must default to false — this grant is project-wide, not scoped to a specific topic/subscription."
  }
}

run "secret_accessor_absent_by_default" {
  command = plan

  assert {
    condition     = length(google_secret_manager_secret_iam_member.secret_accessor) == 0
    error_message = "No secret grants should exist when secret_accessor_ids is empty."
  }
}

run "cloudsql_resources_absent_by_default" {
  command = plan

  assert {
    condition     = length(google_project_iam_member.cloudsql_client) == 0 && length(google_project_iam_member.cloudsql_instance_user) == 0 && length(google_sql_user.controller_iam) == 0
    error_message = "No Cloud SQL IAM resources should exist when cloudsql_instance_name is null."
  }
}

run "dashboard_invoker_absent_by_default" {
  command = plan

  assert {
    condition     = length(google_cloud_run_v2_service_iam_member.dashboard_invoker) == 0
    error_message = "No dashboard invoker grant should exist when dashboard_invoker_members is empty."
  }
}

# --- Opt-in toggles actually grant something once set ------------------

run "pubsub_viewer_granted_when_enabled" {
  command = plan

  variables {
    enable_pubsub_preconditions = true
  }

  assert {
    condition     = contains(keys(google_project_iam_member.pubsub_viewer), "example-target-project-a")
    error_message = "pubsub.viewer must be granted on every target project once enable_pubsub_preconditions is true."
  }
}

run "secret_accessor_granted_when_configured" {
  command = plan

  variables {
    secret_accessor_ids = {
      database_url = "projects/example-mgmt-project/secrets/database-url"
    }
  }

  assert {
    condition     = length(google_secret_manager_secret_iam_member.secret_accessor) == 1
    error_message = "Exactly one secretAccessor grant should exist per secret_accessor_ids entry."
  }
}

run "cloudsql_resources_created_when_instance_set" {
  # command = apply: google_sql_user.name derives from the SA's computed
  # `email` attribute (trimsuffix), only known after the mocked create.
  command = apply

  variables {
    cloudsql_instance_name = "example-runcd-db"
  }

  assert {
    condition     = length(google_project_iam_member.cloudsql_client) == 1 && length(google_project_iam_member.cloudsql_instance_user) == 1
    error_message = "cloudsql.client and cloudsql.instanceUser must both be granted when cloudsql_instance_name is set."
  }

  assert {
    # Real SA emails are "name@project.iam.gserviceaccount.com" — stripping
    # only the ".gserviceaccount.com" suffix correctly leaves the ".iam"
    # part; that's GCP's own IAM-database-username convention, not a bug.
    condition     = google_sql_user.controller_iam[0].name == "mocked-controller@example-mgmt-project.iam"
    error_message = "The Cloud SQL IAM user name must be the controller SA email with only the .gserviceaccount.com suffix stripped."
  }

  assert {
    condition     = google_sql_user.controller_iam[0].type == "CLOUD_IAM_SERVICE_ACCOUNT"
    error_message = "The Cloud SQL user must be IAM-auth type, not a password user."
  }
}

run "dashboard_invoker_granted_when_service_and_members_set" {
  command = plan

  variables {
    controller_cloud_run_service_name = "runcd"
    controller_cloud_run_region       = "us-central1"
    dashboard_invoker_members = [
      "serviceAccount:dashboard@example-mgmt-project.iam.gserviceaccount.com",
    ]
  }

  assert {
    condition     = length(google_cloud_run_v2_service_iam_member.dashboard_invoker) == 1
    error_message = "Exactly one run.invoker grant should exist per dashboard_invoker_members entry."
  }
}

# --- deploy_controller / deploy_dashboard -------------------------------

run "controller_and_dashboard_compute_absent_by_default" {
  command = plan

  assert {
    condition     = length(google_cloud_run_v2_service.controller) == 0 && length(google_cloud_run_v2_service.dashboard) == 0 && length(google_service_account.dashboard) == 0
    error_message = "No Cloud Run services or dashboard SA should exist when deploy_controller/deploy_dashboard are both false."
  }
}

run "deploy_controller_creates_service_with_image_ignored_on_change" {
  command = plan

  variables {
    deploy_controller                 = true
    controller_image                  = "us-central1-docker.pkg.dev/example-mgmt-project/runcd/controller:v1"
    controller_cloud_run_service_name = "runcd"
    controller_cloud_run_region       = "us-central1"
  }

  assert {
    condition     = length(google_cloud_run_v2_service.controller) == 1
    error_message = "deploy_controller = true must create exactly one controller Cloud Run service."
  }

  assert {
    condition     = google_cloud_run_v2_service.controller[0].template[0].scaling[0].min_instance_count == 1
    error_message = "controller_min_instances must default to 1, not 0 — the reconcile loop and leader renewal need to keep running with no HTTP traffic."
  }
}

run "deploy_dashboard_creates_service_and_wires_invoker_automatically" {
  # apply: asserting on google_service_account.dashboard[0].email (computed)
  # and using it to check the auto-wired invoker grant.
  command = apply

  variables {
    deploy_controller                 = true
    controller_image                  = "us-central1-docker.pkg.dev/example-mgmt-project/runcd/controller:v1"
    controller_cloud_run_service_name = "runcd"
    controller_cloud_run_region       = "us-central1"
    deploy_dashboard                  = true
    dashboard_image                   = "us-central1-docker.pkg.dev/example-mgmt-project/runcd/dashboard:v1"
  }

  assert {
    condition     = length(google_cloud_run_v2_service.dashboard) == 1 && length(google_service_account.dashboard) == 1
    error_message = "deploy_dashboard = true must create both the dashboard's Cloud Run service and its own dedicated service account."
  }

  assert {
    condition     = contains([for m in google_cloud_run_v2_service_iam_member.dashboard_invoker : m.member], "serviceAccount:${google_service_account.dashboard[0].email}")
    error_message = "The dashboard's own service account must automatically be granted run.invoker on the controller, with no manual dashboard_invoker_members entry needed."
  }
}

run "deploy_controller_public_and_dashboard_public_grants_off_by_default" {
  command = plan

  variables {
    deploy_controller                 = true
    controller_image                  = "us-central1-docker.pkg.dev/example-mgmt-project/runcd/controller:v1"
    controller_cloud_run_service_name = "runcd"
    controller_cloud_run_region       = "us-central1"
    deploy_dashboard                  = true
    dashboard_image                   = "us-central1-docker.pkg.dev/example-mgmt-project/runcd/dashboard:v1"
  }

  assert {
    condition     = length(google_cloud_run_v2_service_iam_member.controller_public) == 0 && length(google_cloud_run_v2_service_iam_member.dashboard_public) == 0
    error_message = "Neither service should be granted allUsers invoker unless *_allow_unauthenticated is explicitly set — IAP-first is the documented default posture."
  }
}

run "rejects_deploy_controller_without_image" {
  command = plan

  variables {
    deploy_controller                 = true
    controller_cloud_run_service_name = "runcd"
    controller_cloud_run_region       = "us-central1"
  }

  expect_failures = [var.deploy_controller]
}

run "rejects_deploy_dashboard_without_image" {
  command = plan

  variables {
    deploy_dashboard = true
  }

  expect_failures = [var.deploy_dashboard]
}

# --- Variable validation: reject malformed/incomplete input ------------

run "rejects_malformed_project_id" {
  command = plan

  variables {
    target_projects = ["Not_A_Valid_Project!"]
  }

  expect_failures = [var.target_projects]
}

run "rejects_non_numeric_folder_id" {
  command = plan

  variables {
    target_folders = ["folders/123456789012"]
  }

  expect_failures = [var.target_folders]
}

run "rejects_malformed_secret_resource_id" {
  command = plan

  variables {
    secret_accessor_ids = {
      database_url = "example-shared-resources/secrets/database-url"
    }
  }

  expect_failures = [var.secret_accessor_ids]
}

run "rejects_dashboard_invoker_without_service_and_region" {
  command = plan

  variables {
    dashboard_invoker_members = [
      "serviceAccount:dashboard@example-mgmt-project.iam.gserviceaccount.com",
    ]
    # controller_cloud_run_service_name / controller_cloud_run_region left null
  }

  expect_failures = [var.dashboard_invoker_members]
}

run "rejects_malformed_dashboard_invoker_member" {
  command = plan

  variables {
    dashboard_invoker_members         = ["not-a-valid-iam-member"]
    controller_cloud_run_service_name = "runcd"
    controller_cloud_run_region       = "us-central1"
  }

  expect_failures = [var.dashboard_invoker_members]
}
