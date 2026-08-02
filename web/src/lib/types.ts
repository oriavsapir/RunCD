// Mirrors the JSON shapes served by internal/api/units.go.

export type UnitStatus =
  | "Pending"
  | "Synced"
  | "OutOfSync"
  | "Progressing"
  | "Degraded"
  | "Missing"
  | "Invalid";

export type UnitHealth =
  | "Pending"
  | "Healthy"
  | "Progressing"
  | "Degraded"
  | "Missing"
  | "Invalid";

export interface Unit {
  app: string;
  project: string;
  env: string;
  region: string;
  auto: boolean;
  desiredImage?: string;
  liveImage?: string;
  // Mirror the manifest's image.track/image.version/image.repository
  // (internal/imageupdater's resolver input) — empty for a unit that only
  // sets image.digest.
  track?: string;
  version?: string;
  repository?: string;
  // "service" | "job" | "workerPool" (manifest.ResourceType) — a job runs
  // to completion and stops, so its `health` really means "did the most
  // recent execution succeed," not an ongoing state the way a service's
  // does. Empty only for a unit pending its first reconcile pass.
  resourceType?: string;
  status: UnitStatus;
  health: UnitHealth;
  lastReconciledAt?: string;
  canSync: boolean;
  // Mirrors this unit's effective SyncPolicy.observe — sync is disabled
  // server-side regardless of canSync, so the Sync button should say why
  // rather than let a click round-trip into a 409.
  observing: boolean;
  // This app's resource exclusions (config.App.ignoreFields/
  // ignorePreconditions) — a unit's Status can reflect a diff on a field
  // excluded here, which the desired/live image comparison alone can't
  // explain.
  ignoreFields?: string[];
  ignorePreconditions?: string[];
}

export type SyncTrigger = "auto" | "manual";
export type SyncResult = "in_progress" | "succeeded" | "failed";

export interface SyncEvent {
  id: number;
  trigger: SyncTrigger;
  actor?: string;
  fromImage?: string;
  toImage: string;
  startedAt: string;
  finishedAt?: string;
  result: SyncResult;
  error?: string;
}

export interface SyncResponse {
  app: string;
  project: string;
  status: UnitStatus;
  health: UnitHealth;
}

export type BatchSyncSkipReason =
  | "forbidden"
  | "observing"
  | "inProgress"
  | "error";

// One unit's outcome from a bulk sync (POST /api/sync). skipped is empty
// when the sync was actually attempted — status/health then reflect its
// outcome the same way SyncResponse does for a single unit.
export interface BatchSyncResult {
  app: string;
  project: string;
  status?: UnitStatus;
  health?: UnitHealth;
  skipped?: BatchSyncSkipReason;
}

export type RbacRole = "admin" | "syncer";

export interface RbacRule {
  subject: string;
  role: RbacRole;
  scope: string[];
}

export interface RuntimeConfig {
  configRepo: string;
  configBranch: string;
  configPath: string;
  rbacPath: string;
  reconcileIntervalSeconds: number;
  managedFields: string[];
  notificationsEnabled: boolean;
}
