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
  status: UnitStatus;
  health: UnitHealth;
  lastReconciledAt?: string;
  canSync: boolean;
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
