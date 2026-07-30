import Link from "next/link";
import { StatusBadge } from "@/components/status-badge";
import { SyncButton } from "@/components/sync-button";
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
    list.sort((a, b) => (a.project + a.app).localeCompare(b.project + b.app));
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
      <p className="text-muted-foreground text-sm">
        No sync units configured.
      </p>
    );
  }

  const groups = groupByEnv(units);
  const envNames = [...groups.keys()].sort();

  return (
    <div className="flex flex-col gap-8">
      {envNames.map((env) => (
        <section key={env}>
          <h2 className="text-muted-foreground mb-2 text-sm font-semibold tracking-wide uppercase">
            {env}
          </h2>
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
                      className="hover:underline"
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
