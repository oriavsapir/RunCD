"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  FolderGit2,
  HelpCircle,
  Layers,
  ListTree,
  Loader2,
  RefreshCw,
  Search,
  Table2,
  X,
  XCircle,
} from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { ProjectGrid } from "@/components/project-grid";
import { UnitTable, UnitTree } from "@/components/unit-table";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { listUnits } from "@/lib/api";
import type { Unit } from "@/lib/types";
import { usePolling } from "@/lib/use-polling";

type ViewMode = "projects" | "table" | "tree";
const VIEW_MODES: ViewMode[] = ["projects", "table", "tree"];

function deriveViewState(searchParams: URLSearchParams): {
  view: ViewMode;
  selectedProject: string | null;
} {
  const project = searchParams.get("project");
  const rawView = searchParams.get("view");
  if (project) {
    // A project filter paired with the "projects" grid (or no view at all)
    // is a self-perpetuating inconsistent state — the "Project: X" chip
    // shows, but the grid still lists every project. Table is the natural
    // fallback (the same view clicking a project card lands on), while an
    // explicit tree stays respected.
    return {
      view: rawView === "tree" ? "tree" : "table",
      selectedProject: project,
    };
  }
  return {
    view: VIEW_MODES.includes(rawView as ViewMode)
      ? (rawView as ViewMode)
      : "projects",
    selectedProject: null,
  };
}

// Without this, a live rollout (or another operator's sync) looks frozen
// until someone happens to click Refresh — this is a silent background
// poll, not tied to `refreshing`, so it doesn't spin the Refresh button or
// otherwise announce itself.
const POLL_INTERVAL_MS = 15000;

const STAT_TILES: Array<{
  label: string;
  match: (u: Unit) => boolean;
  icon: typeof Layers;
  className: string;
  // Only the Progressing tile's icon spins, and only while it's actually
  // counting something — a spinner next to "0" reads as "something's
  // happening" when nothing is.
  spinWhenNonZero?: boolean;
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
    spinWhenNonZero: true,
  },
  {
    label: "Degraded",
    match: (u) => u.status === "Degraded" || u.health === "Degraded",
    icon: XCircle,
    className: "text-red-600 dark:text-red-400",
  },
];

export default function Home() {
  return (
    <Suspense fallback={null}>
      <SyncUnitsPage />
    </Suspense>
  );
}

function SyncUnitsPage() {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  const [units, setUnits] = useState<Unit[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  // Absolute, not "Xs ago" — a relative time would need its own ticking
  // timer to stay accurate; an absolute timestamp is trivially always
  // correct and just as useful for "is this stale" during an incident.
  const [lastUpdatedAt, setLastUpdatedAt] = useState<Date | null>(null);
  const [query, setQuery] = useState("");
  // view/selectedProject live only in the URL, not in local state — a
  // separate useState copy synced back and forth via an effect either goes
  // stale (browser Back/Forward wouldn't touch it) or fights the effect
  // that writes it. Deriving fresh from searchParams every render means
  // there's exactly one source of truth, and Back/Forward "just work"
  // since Next re-renders this page with updated searchParams on its own.
  const { view, selectedProject } = deriveViewState(searchParams);

  function updateViewState(patch: {
    view?: ViewMode;
    selectedProject?: string | null;
  }) {
    const nextView = patch.view ?? view;
    const nextProject =
      "selectedProject" in patch ? patch.selectedProject : selectedProject;
    const params = new URLSearchParams();
    if (nextProject) {
      params.set("project", nextProject);
      // Table is deriveViewState's default for a project filter; only tree
      // needs to be spelled out explicitly to round-trip correctly.
      if (nextView === "tree") params.set("view", "tree");
    } else if (nextView !== "projects") {
      params.set("view", nextView);
    }
    const qs = params.toString();
    router.replace(qs ? `${pathname}?${qs}` : pathname, { scroll: false });
  }

  useEffect(() => {
    let cancelled = false;
    listUnits()
      .then((data) => {
        if (!cancelled) {
          setUnits(data);
          setError(null);
          setLastUpdatedAt(new Date());
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(
            err instanceof Error ? err.message : "Failed to load sync units",
          );
        }
      })
      .finally(() => {
        if (!cancelled) setRefreshing(false);
      });
    return () => {
      cancelled = true;
    };
  }, [refreshKey]);

  usePolling(POLL_INTERVAL_MS, () => setRefreshKey((k) => k + 1));

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
    if (selectedProject) {
      return units.filter((u) => u.project === selectedProject);
    }
    const q = query.trim().toLowerCase();
    if (!q) return units;
    return units.filter(
      (u) =>
        u.app.toLowerCase().includes(q) ||
        u.project.toLowerCase().includes(q) ||
        u.env.toLowerCase().includes(q),
    );
  }, [units, query, selectedProject]);

  return (
    <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-6 p-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Sync Units</h1>
          <p className="text-muted-foreground text-sm">
            Every configured sync unit and its last-known status.
          </p>
        </div>
        <div className="flex flex-col items-end gap-1">
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
            Refresh
          </Button>
          {lastUpdatedAt && (
            <p className="text-muted-foreground text-xs">
              Updated {lastUpdatedAt.toLocaleTimeString()}
            </p>
          )}
        </div>
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
          {STAT_TILES.map(
            ({ label, match, icon: Icon, className, spinWhenNonZero }) => {
              const count = units.filter(match).length;
              const spin = spinWhenNonZero && count > 0;
              return (
                <div
                  key={label}
                  className="bg-card flex items-center gap-3 rounded-lg border p-3"
                >
                  <Icon
                    className={`size-5 shrink-0 ${className} ${spin ? "animate-spin" : ""}`}
                  />
                  <div>
                    <p className="text-lg leading-none font-semibold">
                      {count}
                    </p>
                    <p className="text-muted-foreground text-xs">{label}</p>
                  </div>
                </div>
              );
            },
          )}
          {(() => {
            // The tiles above aren't a mutually-exclusive partition (a unit
            // can match none of them, e.g. status=Invalid/health=Invalid —
            // that combination is never Synced/OutOfSync and isn't
            // Progressing or Degraded either) — without this, Total could
            // silently exceed the visible tiles' sum with no explanation.
            const accounted = new Set(
              STAT_TILES.flatMap((t) => units.filter(t.match)),
            );
            const other = units.length - accounted.size;
            return (
              other > 0 && (
                <div className="bg-card flex items-center gap-3 rounded-lg border p-3">
                  <HelpCircle className="text-muted-foreground size-5 shrink-0" />
                  <div>
                    <p className="text-lg leading-none font-semibold">
                      {other}
                    </p>
                    <p className="text-muted-foreground text-xs">
                      Other (pending/missing/invalid)
                    </p>
                  </div>
                </div>
              )
            );
          })()}
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
            {selectedProject && (
              <div className="flex items-center gap-2 text-sm">
                <span className="text-muted-foreground">Project:</span>
                <Badge variant="secondary" className="gap-1 font-normal">
                  {selectedProject}
                  <button
                    type="button"
                    onClick={() => {
                      // Also clears query, not just selectedProject —
                      // leaving a stale, possibly-forgotten search term in
                      // place would otherwise silently reactivate the
                      // moment the project filter clears, re-filtering the
                      // table with no visible explanation for why.
                      setQuery("");
                      updateViewState({ selectedProject: null });
                    }}
                    aria-label={`Clear project filter (${selectedProject})`}
                    className="hover:text-foreground"
                  >
                    <X className="size-3" />
                  </button>
                </Badge>
              </div>
            )}
            <div className="flex items-center gap-2">
              <div className="relative flex-1">
                <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                <Input
                  value={query}
                  onChange={(e) => {
                    setQuery(e.target.value);
                    // Only when there's actually a project filter to clear
                    // — otherwise every keystroke would fire a
                    // router.replace with nothing to change.
                    if (selectedProject) {
                      updateViewState({ selectedProject: null });
                    }
                  }}
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
                  if (!next) return;
                  updateViewState({
                    view: next as ViewMode,
                    ...(next === "projects" ? { selectedProject: null } : {}),
                  });
                }}
              >
                <ToggleGroupItem value="projects" aria-label="Project view">
                  <FolderGit2 className="size-4" />
                </ToggleGroupItem>
                <ToggleGroupItem value="table" aria-label="Table view">
                  <Table2 className="size-4" />
                </ToggleGroupItem>
                <ToggleGroupItem value="tree" aria-label="Tree view">
                  <ListTree className="size-4" />
                </ToggleGroupItem>
              </ToggleGroup>
            </div>
            {view === "projects" ? (
              <ProjectGrid
                units={filtered ?? []}
                onSelectProject={(project) =>
                  updateViewState({ selectedProject: project, view: "table" })
                }
              />
            ) : view === "table" ? (
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
