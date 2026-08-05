import { AlertTriangle, CheckCircle2, EyeOff, FolderGit2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { Unit } from "@/lib/types";

interface ProjectSummary {
  project: string;
  envs: Set<string>;
  regions: Set<string>;
  total: number;
  synced: number;
  outOfSync: number;
  needsAttention: number;
  observing: number;
}

// Grouped by project, not environment — at real scale a flat list of every
// sync unit stops being scannable; this is the "which project needs my
// attention" overview.
export function summarizeByProject(units: Unit[]): ProjectSummary[] {
  const byProject = new Map<string, ProjectSummary>();
  for (const u of units) {
    const s = byProject.get(u.project) ?? {
      project: u.project,
      envs: new Set<string>(),
      regions: new Set<string>(),
      total: 0,
      synced: 0,
      outOfSync: 0,
      needsAttention: 0,
      observing: 0,
    };
    s.envs.add(u.env);
    s.regions.add(u.region);
    s.total += 1;
    if (u.status === "Synced") s.synced += 1;
    if (u.status === "OutOfSync") s.outOfSync += 1;
    if (
      u.status === "Degraded" ||
      u.status === "Invalid" ||
      u.health === "Degraded" ||
      u.health === "Invalid"
    ) {
      s.needsAttention += 1;
    }
    if (u.observing) s.observing += 1;
    byProject.set(u.project, s);
  }
  return [...byProject.values()].sort((a, b) =>
    a.project.localeCompare(b.project),
  );
}

export function ProjectGrid({
  units,
  onSelectProject,
}: {
  units: Unit[];
  onSelectProject: (project: string) => void;
}) {
  const summaries = summarizeByProject(units);

  if (summaries.length === 0) {
    return (
      <p className="text-muted-foreground text-sm">No sync units match.</p>
    );
  }

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {summaries.map((s) => (
        <button
          key={s.project}
          type="button"
          onClick={() => onSelectProject(s.project)}
          className={`bg-card hover:border-primary/50 flex flex-col gap-3 rounded-lg border p-4 text-left outline-none transition-colors focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 ${
            s.needsAttention > 0 ? "border-destructive/50" : ""
          }`}
        >
          <div className="flex items-start justify-between gap-2">
            <div className="flex items-center gap-2">
              <FolderGit2 className="text-primary size-4 shrink-0" />
              <span className="font-semibold">{s.project}</span>
            </div>
            {s.needsAttention > 0 && (
              <span className="text-destructive flex items-center gap-1 text-xs font-medium">
                <AlertTriangle className="size-3.5" />
                {s.needsAttention} needs attention
              </span>
            )}
          </div>

          <p className="text-muted-foreground truncate text-xs">
            {[...s.envs].sort().join(", ")} · {[...s.regions].sort().join(", ")}
          </p>

          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant="secondary" className="text-xs font-normal">
              {s.total} app{s.total === 1 ? "" : "s"}
            </Badge>
            {s.synced > 0 && (
              <Badge
                variant="outline"
                className="border-emerald-200 text-xs font-normal text-emerald-700 dark:border-emerald-800 dark:text-emerald-400"
              >
                <CheckCircle2 className="size-3" />
                {s.synced} synced
              </Badge>
            )}
            {s.outOfSync > 0 && (
              <Badge
                variant="outline"
                className="border-amber-200 text-xs font-normal text-amber-700 dark:border-amber-800 dark:text-amber-400"
              >
                {s.outOfSync} out of sync
              </Badge>
            )}
            {s.observing > 0 && (
              <Badge variant="outline" className="text-xs font-normal">
                <EyeOff className="size-3" />
                {s.observing} observing
              </Badge>
            )}
          </div>
        </button>
      ))}
    </div>
  );
}
