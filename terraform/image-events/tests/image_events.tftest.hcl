# Native `terraform test` (1.6+) unit suite, run with a mocked provider
# (1.7+) — zero-cost, no real GCP project/credentials or an actually
# deployed controller service needed.

mock_provider "google" {}

variables {
  project_id             = "example-shared-resources"
  region                 = "us-central1"
  cloud_run_service_name = "runcd"
}

run "trigger_destination_path_is_not_the_default_root" {
  command = plan

  assert {
    # Regression guard for the real bug this module shipped with once:
    # an unset destination path defaults to "/", not the handler's actual
    # route — a trigger that looks healthy but silently never reaches
    # POST /api/events/image.
    condition     = one(google_eventarc_trigger.image_push.destination).cloud_run_service[0].path == "/api/events/image"
    error_message = "The Eventarc trigger must route to /api/events/image, not Cloud Run's default \"/\"."
  }
}

run "audit_config_managed_by_default" {
  command = plan

  assert {
    condition     = length(google_project_iam_audit_config.artifact_registry_data_write) == 1
    error_message = "manage_audit_config must default to true — without it, Docker-PutManifest audit log entries are never emitted and the trigger silently never fires."
  }
}

run "audit_config_skipped_when_disabled" {
  command = plan

  variables {
    manage_audit_config = false
  }

  assert {
    condition     = length(google_project_iam_audit_config.artifact_registry_data_write) == 0
    error_message = "manage_audit_config = false must skip the audit config resource entirely, so it doesn't fight over one already managed elsewhere."
  }
}

run "eventarc_api_enabled_by_default" {
  command = plan

  assert {
    condition     = length(google_project_service.eventarc_api) == 1
    error_message = "enable_apis must default to true — eventarc.googleapis.com must be enabled for the trigger to exist at all."
  }
}

run "eventarc_api_skipped_when_disabled" {
  command = plan

  variables {
    enable_apis = false
  }

  assert {
    condition     = length(google_project_service.eventarc_api) == 0
    error_message = "enable_apis = false must skip API enablement, for centrally-managed setups."
  }
}

run "rejects_malformed_project_id" {
  command = plan

  variables {
    project_id = "Not_A_Valid_Project!"
  }

  expect_failures = [var.project_id]
}

run "rejects_malformed_region" {
  command = plan

  variables {
    region = "not-a-region"
  }

  expect_failures = [var.region]
}

run "rejects_malformed_trigger_name" {
  command = plan

  variables {
    trigger_name = "Not A Valid Name"
  }

  expect_failures = [var.trigger_name]
}

run "rejects_malformed_service_account_id" {
  command = plan

  variables {
    service_account_id = "x"
  }

  expect_failures = [var.service_account_id]
}
