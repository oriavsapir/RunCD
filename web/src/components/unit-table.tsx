import { useState } from "react";
import Link from "next/link";
import { ChevronRight, FolderGit2 } from "lucide-react";
import { StatusBadge } from "@/components/status-badge";
import { SyncButton } from "@/components/sync-button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { Unit } from "@/lib/types";

function groupByEnv(units: Unit[]): Map<string, Unit[]> {
  const groups = new Map<string, Unit[]>();
  for (const u of units) {
    const list = groups.get(u.env) ?? [];
    list.push(u);
    groups.set(u.env, list);
  }
  for (const list of groups.values()) {
    // "\x00"-joined, not a bare concatenation — project/app names could
    // otherwise collide across the boundary (e.g. "acme"+"api" ==
    // "acmea"+"pi"), same convention as gitsource's cache key.
    list.sort((a, b) =>
      `${a.project}\x00${a.app}`.localeCompare(`${b.project}\x00${b.app}`),
    );
  }
  return groups;
}

function groupByProject(units: Unit[]): Map<string, Unit[]> {
  const groups = new Map<string, Unit[]>();
  for (const u of units) {
    const list = groups.get(u.project) ?? [];
    list.push(u);
    groups.set(u.project, list);
  }
  for (const list of groups.values()) {
    list.sort((a, b) => a.app.localeCompare(b.app));
  }
  return groups;
}

interface UnitTableProps {
  units: Unit[];
  onSynced?: () => void;
}

// Sync-unit list grouped by environment/customer project, per §5.11.
export function UnitTable({ units, onSynced }: UnitTableProps) {
  if (units.length === 0) {
    return (
      <p className="text-muted-foreground text-sm">No sync units match.</p>
    );
  }

  const groups = groupByEnv(units);
  const envNames = [...groups.keys()].sort();

  return (
    <div className="flex flex-col gap-8">
      {envNames.map((env) => (
        <section key={env}>
          <div className="mb-2 flex items-center gap-2">
            <h2 className="text-muted-foreground text-sm font-semibold tracking-wide uppercase">
              {env}
            </h2>
            <Badge variant="secondary" className="text-xs font-normal">
              {groups.get(env)!.length}
            </Badge>
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>App</TableHead>
                <TableHead>Project</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Health</TableHead>
                <TableHead className="text-right">Sync</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {groups.get(env)!.map((u) => (
                <TableRow key={`${u.app}/${u.project}`}>
                  <TableCell className="font-medium">
                    <Link
                      href={`/units/${encodeURIComponent(u.project)}/${encodeURIComponent(u.app)}`}
                      className="hover:text-primary hover:underline"
                    >
                      {u.app}
                    </Link>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {u.project}
                  </TableCell>
                  <TableCell>
                    <StatusBadge value={u.status} />
                  </TableCell>
                  <TableCell>
                    <StatusBadge value={u.health} />
                  </TableCell>
                  <TableCell className="text-right">
                    <SyncButton unit={u} onSynced={onSynced} size="sm" />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </section>
      ))}
    </div>
  );
}

// Same data as UnitTable, as a collapsible env → project → app hierarchy
// (native <details>/<summary> — no state or JS tree library needed).
//
// `open` is state-backed, not hardcoded — a bare `open` attribute is
// reapplied by React on every re-render (any unrelated data refresh, e.g.
// the periodic poll), silently re-expanding a node the user had manually
// collapsed a moment before. collapsed tracks the exceptions (default:
// everything expanded) so a manual toggle actually sticks.
export function UnitTree({ units, onSynced }: UnitTableProps) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());

  if (units.length === 0) {
    return (
      <p className="text-muted-foreground text-sm">No sync units match.</p>
    );
  }

  function toggle(key: string, open: boolean) {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (open) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  const envGroups = groupByEnv(units);
  const envNames = [...envGroups.keys()].sort();

  return (
    <div className="flex flex-col gap-2">
      {envNames.map((env) => {
        const envUnits = envGroups.get(env)!;
        const projectGroups = groupByProject(envUnits);
        const projectNames = [...projectGroups.keys()].sort();
        return (
          <details
            key={env}
            open={!collapsed.has(env)}
            onToggle={(e) => toggle(env, e.currentTarget.open)}
            className="group rounded-lg border"
          >
            <summary className="hover:bg-accent/50 flex cursor-pointer list-none items-center gap-2 rounded-lg px-3 py-2 select-none [&::-webkit-details-marker]:hidden">
              <ChevronRight className="text-muted-foreground size-4 shrink-0 transition-transform group-open:rotate-90" />
              <span className="text-sm font-semibold tracking-wide uppercase">
                {env}
              </span>
              <Badge variant="secondary" className="text-xs font-normal">
                {envUnits.length}
              </Badge>
            </summary>
            <div className="flex flex-col gap-1 px-3 pb-3 pl-9">
              {projectNames.map((project) => {
                const key = `${env}/${project}`;
                return (
                  <details
                    key={project}
                    open={!collapsed.has(key)}
                    onToggle={(e) => toggle(key, e.currentTarget.open)}
                    className="group"
                  >
                    <summary className="text-muted-foreground hover:text-foreground flex cursor-pointer list-none items-center gap-2 rounded-md py-1.5 text-sm select-none [&::-webkit-details-marker]:hidden">
                      <ChevronRight className="size-3.5 shrink-0 transition-transform group-open:rotate-90" />
                      <FolderGit2 className="size-3.5 shrink-0" />
                      {project}
                    </summary>
                    <div className="flex flex-col gap-1 py-1 pl-9">
                      {projectGroups.get(project)!.map((u) => (
                        <div
                          key={u.app}
                          className="hover:bg-accent/40 flex flex-wrap items-center justify-between gap-2 rounded-md px-2 py-1.5"
                        >
                          <Link
                            href={`/units/${encodeURIComponent(u.project)}/${encodeURIComponent(u.app)}`}
                            className="hover:text-primary text-sm font-medium hover:underline"
                          >
                            {u.app}
                          </Link>
                          <div className="flex items-center gap-2">
                            <StatusBadge value={u.status} />
                            <StatusBadge value={u.health} />
                            <SyncButton
                              unit={u}
                              onSynced={onSynced}
                              size="sm"
                            />
                          </div>
                        </div>
                      ))}
                    </div>
                  </details>
                );
              })}
            </div>
          </details>
        );
      })}
    </div>
  );
}
