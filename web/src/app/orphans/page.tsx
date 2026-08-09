"use client";

import { useEffect, useState } from "react";
import { AlertTriangle, RefreshCw, Trash2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { listOrphans } from "@/lib/api";
import type { Orphan } from "@/lib/types";

function groupByProject(orphans: Orphan[]): Map<string, Orphan[]> {
  const groups = new Map<string, Orphan[]>();
  for (const o of orphans) {
    const list = groups.get(o.project) ?? [];
    list.push(o);
    groups.set(o.project, list);
  }
  for (const list of groups.values()) {
    list.sort((a, b) =>
      `${a.region}\x00${a.app}`.localeCompare(`${b.region}\x00${b.app}`),
    );
  }
  return groups;
}

export default function OrphansPage() {
  const [orphans, setOrphans] = useState<Orphan[] | null>(null);
  const [partial, setPartial] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [refreshing, setRefreshing] = useState(false);

  useEffect(() => {
    let cancelled = false;
    listOrphans()
      .then((result) => {
        if (cancelled) return;
        setOrphans(result.orphans);
        setPartial(result.partial);
        setError(null);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to scan for orphans");
      })
      .finally(() => {
        if (!cancelled) setRefreshing(false);
      });
    return () => {
      cancelled = true;
    };
  }, [refreshKey]);

  const projectGroups = orphans ? groupByProject(orphans) : null;
  const projectNames = projectGroups ? [...projectGroups.keys()].sort() : [];

  return (
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-6 p-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Orphans</h1>
          <p className="text-muted-foreground text-sm">
            Live Cloud Run services, jobs, and worker pools found in GCP that no
            current sync unit declares. Read-only — nothing here is deleted
            automatically.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={refreshing}
          onClick={() => {
            setRefreshing(true);
            setRefreshKey((k) => k + 1);
          }}
        >
          <RefreshCw
            className={refreshing ? "size-3.5 animate-spin" : "size-3.5"}
          />
          Rescan
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Trash2 className="text-primary size-4" />
            Detected orphans
          </CardTitle>
          <CardDescription>
            Scanned live, on demand, across every project/region your account
            can sync — not cached from the last reconcile pass.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {error ? (
            <div className="flex items-center justify-between gap-3">
              <p className="text-destructive text-sm">{error}</p>
              <Button
                variant="outline"
                size="sm"
                disabled={refreshing}
                onClick={() => {
                  setRefreshing(true);
                  setRefreshKey((k) => k + 1);
                }}
              >
                Retry
              </Button>
            </div>
          ) : !orphans ? (
            <Skeleton className="h-32 w-full" />
          ) : (
            <>
              {partial && (
                <div className="text-muted-foreground flex items-center gap-2 rounded-md border border-dashed p-2 text-xs">
                  <AlertTriangle className="size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
                  Some project/region scans failed — this list may be
                  incomplete. Rescan to try again.
                </div>
              )}
              {orphans.length === 0 ? (
                <p className="text-muted-foreground text-sm">
                  No orphans found. Every live resource in your scannable
                  projects is declared by a current sync unit.
                </p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Project</TableHead>
                      <TableHead>Region</TableHead>
                      <TableHead>Name</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {projectNames.map((project) =>
                      projectGroups!.get(project)!.map((o, i) => (
                        <TableRow key={`${project}-${o.region}-${o.app}`}>
                          <TableCell className="font-medium">
                            {i === 0 ? project : ""}
                          </TableCell>
                          <TableCell className="text-muted-foreground">
                            {o.region}
                          </TableCell>
                          <TableCell className="font-mono text-xs">
                            {o.app}
                          </TableCell>
                        </TableRow>
                      )),
                    )}
                  </TableBody>
                </Table>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </main>
  );
}
