variable "management_project_id" {
  description = "GCP project that owns the controller's shared service account."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.management_project_id))
    error_message = "management_project_id must be a valid GCP project ID (6-30 chars, lowercase letters/digits/hyphens, starting with a letter)."
  }
}

variable "service_account_id" {
  description = "Account ID (local part) for the controller's shared service account."
  type        = string
  default     = "runcd-controller"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.service_account_id))
    error_message = "service_account_id must be 6-30 chars, lowercase letters/digits/hyphens, starting with a letter (GCP service account ID rules)."
  }
}

variable "target_projects" {
  description = <<-EOT
    GCP project IDs the controller may deploy to — one entry per project
    listed under any environments[env].projects in runcd.yaml (§5.1, §5.5).
    Adding a project here is the "one IAM binding" provisioning step in §5.5.
  EOT
  type        = set(string)
  default     = []

  validation {
    condition     = alltrue([for p in var.target_projects : can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", p))])
    error_message = "Every target_projects entry must be a valid GCP project ID."
  }
}

variable "target_folders" {
  description = <<-EOT
    GCP folder IDs (numeric, e.g. "123456789012") listed under any
    environments[env].folders in runcd.yaml. Grants the SA folderViewer on
    each folder and resolves its current direct child projects at `apply`
    time for the same run.developer grant target_projects gets — a
    plan-time snapshot, not continuous reconciliation.
  EOT
  type        = set(string)
  default     = []

  validation {
    condition     = alltrue([for f in var.target_folders : can(regex("^[0-9]+$", f))])
    error_message = "Every target_folders entry must be a bare numeric folder ID (e.g. \"123456789012\"), not \"folders/123456789012\"."
  }
}

variable "artifact_registry_project_ids" {
  description = <<-EOT
    Projects hosting the Artifact Registry repositories referenced by
    manifests' image.repository (track/version resolution) or by a live
    resource deployed by tag rather than digest (cloudrun's live tag
    resolution). Independent of target_projects/target_folders — a shared
    registry in one project (often management_project_id) backing many
    deploy-target projects is the common shape, not a registry per target
    project. Defaults to [management_project_id] when left null.
  EOT
  type        = set(string)
  default     = null
  nullable    = true

  validation {
    condition     = var.artifact_registry_project_ids == null || alltrue([for p in var.artifact_registry_project_ids : can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", p))])
    error_message = "Every artifact_registry_project_ids entry must be a valid GCP project ID."
  }
}

variable "runtime_service_account_emails" {
  description = <<-EOT
    Per-project runtime service account the deployed Cloud Run revision runs
    as, if the controller must be granted iam.serviceAccounts.actAs on it
    (§5.5 point 2 — confirm this requirement against current GCP docs before
    Phase 0 finalizes; not all target projects necessarily need this).
    Keyed by target project ID; a project with no entry gets no actAs grant.
  EOT
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for e in values(var.runtime_service_account_emails) : can(regex("^[^@\\s]+@[^@\\s]+\\.gserviceaccount\\.com$", e))])
    error_message = "Every runtime_service_account_emails value must be a service account email ending in .gserviceaccount.com."
  }
}

variable "enable_pubsub_preconditions" {
  description = <<-EOT
    Grant roles/pubsub.viewer on every target project (target_projects plus
    every project resolved from target_folders) so
    internal/precondition.GCPChecker can evaluate pubsubTopic/
    pubsubSubscription preconditions (§5.10) there. Turn off if nothing in
    runcd.yaml declares a pubsub precondition and you'd rather not grant
    fleet-wide Pub/Sub read for a feature you're not using.

    Defaults to false: this grant is project-wide (pubsub.viewer), not
    scoped to the specific topics/subscriptions a manifest actually
    declares, so an importer should opt in deliberately rather than
    inherit fleet-wide Pub/Sub read by default.
  EOT
  type        = bool
  default     = false
}

variable "secret_accessor_ids" {
  description = <<-EOT
    Secret Manager secrets the controller reads at boot (GitHub App private
    key, DATABASE_URL, Slack webhook, etc.) if you store them there instead
    of plain env vars — this module has no way to know which ones you use,
    so nothing is granted unless listed here. Keyed by an arbitrary label
    (used only for the resource address); each value is the secret's full
    resource ID, "projects/<project>/secrets/<secret_id>".
  EOT
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for s in values(var.secret_accessor_ids) : can(regex("^projects/[^/]+/secrets/[^/]+$", s))])
    error_message = "Every secret_accessor_ids value must be a full secret resource ID: projects/<project>/secrets/<secret_id>."
  }
}

variable "cloudsql_instance_name" {
  description = <<-EOT
    Cloud SQL instance ID (in management_project_id) backing the
    controller's Postgres database, if DATABASE_URL is resolved via the
    Cloud SQL Go Connector with IAM database auth (see README's
    CLOUDSQL_INSTANCE_CONNECTION_NAME/CLOUDSQL_IAM_DB_USER env vars) rather
    than a plain password. When set, grants the controller SA
    roles/cloudsql.client + roles/cloudsql.instanceUser on
    management_project_id and creates the matching IAM database user.
    Leave null if the controller connects with a plain password instead —
    nothing Cloud-SQL-specific is granted in that case.
  EOT
  type        = string
  default     = null

  validation {
    condition     = var.cloudsql_instance_name == null || can(regex("^[a-z][a-z0-9-]{0,97}[a-z0-9]$", var.cloudsql_instance_name))
    error_message = "cloudsql_instance_name must be a valid Cloud SQL instance ID."
  }
}

variable "controller_cloud_run_service_name" {
  description = <<-EOT
    Name of the controller Cloud Run service, in management_project_id.
    Required whenever this module needs to *reference* that service by
    name — either because it's deploying it (deploy_controller = true) or
    because dashboard_invoker_members is non-empty and needs to know which
    service to scope the grant to. If deploy_controller is false, the
    service is assumed already deployed elsewhere (e.g. `gcloud run
    deploy`, same relationship the terraform/image-events sibling module
    has to it) — this module only reads/references it in that case.
  EOT
  type        = string
  default     = null
}

variable "controller_cloud_run_region" {
  description = "Region of the controller Cloud Run service. Required under the same conditions as controller_cloud_run_service_name."
  type        = string
  default     = null
}

variable "deploy_controller" {
  description = <<-EOT
    Also create the controller's own Cloud Run v2 service, not just its IAM
    identity. False by default — plenty of setups deploy the service via
    `gcloud run deploy`/CI instead. Requires controller_image plus the
    service name/region below. The image is excluded from drift detection
    (see the resource's lifecycle.ignore_changes) since CI redeploying a new
    tag is the expected ongoing path.
  EOT
  type        = bool
  default     = false

  validation {
    condition     = !var.deploy_controller || (var.controller_image != null && var.controller_cloud_run_service_name != null && var.controller_cloud_run_region != null)
    error_message = "controller_image, controller_cloud_run_service_name, and controller_cloud_run_region must all be set when deploy_controller is true."
  }
}

variable "controller_image" {
  description = <<-EOT
    Initial container image for the controller Cloud Run service, e.g.
    "us-central1-docker.pkg.dev/<project>/<repo>/controller:vN". Required
    if deploy_controller is true. Only used at first create — see
    deploy_controller's note on why later image changes are ignored.
  EOT
  type        = string
  default     = null
}

variable "controller_env" {
  description = "Plain (non-secret) env vars for the controller container, e.g. RUNCD_CONFIG_REPO, RUNCD_CONFIG_BRANCH, IAP_AUDIENCE. Only used if deploy_controller is true."
  type        = map(string)
  default     = {}
}

variable "controller_secret_env" {
  description = <<-EOT
    Secret-Manager-backed env vars for the controller container (DATABASE_URL,
    GITHUB_APP_PEM, etc.), keyed by env var name. Only used if
    deploy_controller is true. Each secret must already exist — this
    module doesn't create secrets, only wires up the reference (and, via
    secret_accessor_ids, the read grant).
  EOT
  type = map(object({
    secret  = string
    version = optional(string, "latest")
  }))
  default = {}
}

variable "controller_cpu" {
  description = "CPU limit for the controller container. Only used if deploy_controller is true."
  type        = string
  default     = "1"
}

variable "controller_memory" {
  description = "Memory limit for the controller container. Only used if deploy_controller is true."
  type        = string
  default     = "512Mi"
}

variable "controller_port" {
  description = "Container port the controller's HTTP server listens on (HTTP_ADDR's port). Only used if deploy_controller is true."
  type        = number
  default     = 8080
}

variable "controller_min_instances" {
  description = "Minimum Cloud Run instance count. Defaults to 1, not 0 — the reconcile loop and leader renewal need to keep running with no HTTP traffic. Only used if deploy_controller is true."
  type        = number
  default     = 1
}

variable "controller_max_instances" {
  description = "Maximum Cloud Run instance count for the controller — more than 1 replica is meaningful here (leader election gates who actually reconciles). Only used if deploy_controller is true."
  type        = number
  default     = 3
}

variable "controller_allow_unauthenticated" {
  description = <<-EOT
    Grant roles/run.invoker to allUsers on the controller service. False by
    default — the README's documented posture is IAP in front of the
    controller (or Cloud Run IAM invoker scoped to the dashboard's SA), not
    public access. Only used if deploy_controller is true.
  EOT
  type        = bool
  default     = false
}

variable "deploy_dashboard" {
  description = <<-EOT
    Also create the dashboard's own Cloud Run v2 service, with its own
    dedicated service account. False by default, same reasoning as
    deploy_controller. Requires dashboard_image. The dashboard's SA is
    automatically added to the controller's run.invoker grant — no manual
    dashboard_invoker_members entry needed.
  EOT
  type        = bool
  default     = false

  validation {
    condition     = !var.deploy_dashboard || var.dashboard_image != null
    error_message = "dashboard_image must be set when deploy_dashboard is true."
  }
}

variable "dashboard_cloud_run_service_name" {
  description = "Name of the dashboard Cloud Run service, in management_project_id. Only used if deploy_dashboard is true."
  type        = string
  default     = "runcd-dashboard"
}

variable "dashboard_cloud_run_region" {
  description = "Region of the dashboard Cloud Run service. Falls back to controller_cloud_run_region if unset. Only used if deploy_dashboard is true."
  type        = string
  default     = null
}

variable "dashboard_service_account_id" {
  description = "Account ID (local part) for the dashboard's own service account. Only used if deploy_dashboard is true."
  type        = string
  default     = "runcd-dashboard"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.dashboard_service_account_id))
    error_message = "dashboard_service_account_id must be 6-30 chars, lowercase letters/digits/hyphens, starting with a letter (GCP service account ID rules)."
  }
}

variable "dashboard_image" {
  description = "Initial container image for the dashboard Cloud Run service. Required if deploy_dashboard is true. Only used at first create — same ignore_changes-on-image reasoning as controller_image."
  type        = string
  default     = null
}

variable "dashboard_env" {
  description = "Plain env vars for the dashboard container, e.g. RUNCD_API_URL. Only used if deploy_dashboard is true."
  type        = map(string)
  default     = {}
}

variable "dashboard_cpu" {
  description = "CPU limit for the dashboard container. Only used if deploy_dashboard is true."
  type        = string
  default     = "1"
}

variable "dashboard_memory" {
  description = "Memory limit for the dashboard container. Only used if deploy_dashboard is true."
  type        = string
  default     = "512Mi"
}

variable "dashboard_port" {
  description = "Container port the dashboard's Next.js server listens on. Only used if deploy_dashboard is true."
  type        = number
  default     = 3000
}

variable "dashboard_min_instances" {
  description = "Minimum Cloud Run instance count for the dashboard. Defaults to 0 (request-driven, unlike the controller) — the dashboard has no background loop to keep alive between requests. Only used if deploy_dashboard is true."
  type        = number
  default     = 0
}

variable "dashboard_max_instances" {
  description = "Maximum Cloud Run instance count for the dashboard. Only used if deploy_dashboard is true."
  type        = number
  default     = 3
}

variable "dashboard_allow_unauthenticated" {
  description = "Grant roles/run.invoker to allUsers on the dashboard service. False by default, same IAP-first posture as controller_allow_unauthenticated. Only used if deploy_dashboard is true."
  type        = bool
  default     = false
}

variable "dashboard_invoker_members" {
  description = <<-EOT
    IAM members (e.g. "serviceAccount:dashboard@project.iam.gserviceaccount.com")
    granted roles/run.invoker scoped to just the controller Cloud Run
    service, on top of whatever deploy_dashboard wires up automatically.
    For a dashboard deployed outside this module.
  EOT
  type        = set(string)
  default     = []

  validation {
    condition     = alltrue([for m in var.dashboard_invoker_members : can(regex("^(user|serviceAccount|group|domain):.+$", m))])
    error_message = "Every dashboard_invoker_members entry must be a valid IAM member string, e.g. \"serviceAccount:name@project.iam.gserviceaccount.com\"."
  }

  validation {
    condition     = (length(var.dashboard_invoker_members) == 0 && !var.deploy_dashboard) || (var.controller_cloud_run_service_name != null && var.controller_cloud_run_region != null)
    error_message = "controller_cloud_run_service_name and controller_cloud_run_region must both be set when dashboard_invoker_members is non-empty or deploy_dashboard is true."
  }
}

variable "enable_apis" {
  description = "Enable the GCP APIs this module's grants depend on (run, artifactregistry, conditionally pubsub/secretmanager/sqladmin). Turn off if API enablement is centrally managed elsewhere."
  type        = bool
  default     = true
}
