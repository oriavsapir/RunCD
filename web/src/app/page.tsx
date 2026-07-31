"use client";

import { useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  Layers,
  ListTree,
  Loader2,
  RefreshCw,
  Search,
  Table2,
  XCircle,
} from "lucide-react";
import Link from "next/link";
import { UnitTable, UnitTree } from "@/components/unit-table";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { listUnits } from "@/lib/api";
import type { Unit } from "@/lib/types";

type ViewMode = "table" | "tree";

const STAT_TILES: Array<{
  label: string;
  match: (u: Unit) => boolean;
  icon: typeof Layers;
  className: string;
}> = [
  {
    label: "Synced",
    match: (u) => u.status === "Synced",
    icon: CheckCircle2,
    className: "text-emerald-600 dark:text-emerald-400",
  },
  {
    label: "Out of sync",
    match: (u) => u.status === "OutOfSync",
    icon: AlertTriangle,
    className: "text-amber-600 dark:text-amber-400",
  },
  {
    label: "Progressing",
    match: (u) => u.status === "Progressing" || u.health === "Progressing",
    icon: Loader2,
    className: "text-blue-600 dark:text-blue-400",
  },
  {
    label: "Degraded",
    match: (u) => u.status === "Degraded" || u.health === "Degraded",
    icon: XCircle,
    className: "text-red-600 dark:text-red-400",
  },
];

export default function Home() {
  const [units, setUnits] = useState<Unit[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [query, setQuery] = useState("");
  const [view, setView] = useState<ViewMode>("table");

  useEffect(() => {
    let cancelled = false;
    listUnits()
      .then((data) => {
        if (!cancelled) {
          setUnits(data);
          setError(null);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(
            err instanceof Error ? err.message : "Failed to load sync units",
          );
        }
      });
    return () => {
      cancelled = true;
    };
  }, [refreshKey]);

  const attention = useMemo(
    () =>
      (units ?? []).filter(
        (u) =>
          u.status === "Degraded" ||
          u.status === "Invalid" ||
          u.health === "Degraded" ||
          u.health === "Invalid",
      ),
    [units],
  );

  const filtered = useMemo(() => {
    if (!units) return units;
    const q = query.trim().toLowerCase();
    if (!q) return units;
    return units.filter(
      (u) =>
        u.app.toLowerCase().includes(q) ||
        u.project.toLowerCase().includes(q) ||
        u.env.toLowerCase().includes(q),
    );
  }, [units, query]);

  return (
    <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-6 p-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Sync Units</h1>
          <p className="text-muted-foreground text-sm">
            Every configured sync unit and its last-known status.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => setRefreshKey((k) => k + 1)}
        >
          <RefreshCw className="size-3.5" />
          Refresh
        </Button>
      </div>

      {attention.length > 0 && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>
            {attention.length === 1
              ? "1 unit needs attention"
              : `${attention.length} units need attention`}
          </AlertTitle>
          <AlertDescription>
            <ul className="flex flex-col gap-0.5">
              {attention.map((u) => (
                <li key={`${u.project}/${u.app}`}>
                  <Link
                    href={`/units/${encodeURIComponent(u.project)}/${encodeURIComponent(u.app)}`}
                    className="font-medium hover:underline"
                  >
                    {u.app}
                  </Link>{" "}
                  <span className="opacity-80">
                    ({u.project}) — status: {u.status}, health: {u.health}
                  </span>
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      {units && units.length > 0 && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <div className="bg-card flex items-center gap-3 rounded-lg border p-3">
            <Layers className="text-primary size-5 shrink-0" />
            <div>
              <p className="text-lg leading-none font-semibold">
                {units.length}
              </p>
              <p className="text-muted-foreground text-xs">Total</p>
            </div>
          </div>
          {STAT_TILES.map(({ label, match, icon: Icon, className }) => (
            <div
              key={label}
              className="bg-card flex items-center gap-3 rounded-lg border p-3"
            >
              <Icon className={`size-5 shrink-0 ${className}`} />
              <div>
                <p className="text-lg leading-none font-semibold">
                  {units.filter(match).length}
                </p>
                <p className="text-muted-foreground text-xs">{label}</p>
              </div>
            </div>
          ))}
        </div>
      )}

      {error && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>Failed to load sync units</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {!units && !error ? (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : (
        units && (
          <>
            <div className="flex items-center gap-2">
              <div className="relative flex-1">
                <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Filter by app, project, or environment…"
                  aria-label="Filter by app, project, or environment"
                  className="h-9 pl-8"
                />
              </div>
              <ToggleGroup
                variant="outline"
                value={[view]}
                onValueChange={(v) => {
                  const next = v.find((x) => x !== view) ?? v[0];
                  if (next) setView(next as ViewMode);
                }}
              >
                <ToggleGroupItem value="table" aria-label="Table view">
                  <Table2 className="size-4" />
                </ToggleGroupItem>
                <ToggleGroupItem value="tree" aria-label="Tree view">
                  <ListTree className="size-4" />
                </ToggleGroupItem>
              </ToggleGroup>
            </div>
            {view === "table" ? (
              <UnitTable
                units={filtered ?? []}
                onSynced={() => setRefreshKey((k) => k + 1)}
              />
            ) : (
              <UnitTree
                units={filtered ?? []}
                onSynced={() => setRefreshKey((k) => k + 1)}
              />
            )}
          </>
        )
      )}
    </main>
  );
}
