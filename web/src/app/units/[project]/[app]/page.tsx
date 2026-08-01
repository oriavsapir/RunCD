"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { AlertCircle, ArrowLeft, RefreshCw } from "lucide-react";
import { DiffView } from "@/components/diff-view";
import { HistoryTable } from "@/components/history-table";
import { SyncButton } from "@/components/sync-button";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { ApiError, getUnit, getUnitHistory } from "@/lib/api";
import type { SyncEvent, Unit } from "@/lib/types";
import { usePolling } from "@/lib/use-polling";

// Same silent background poll as the units list (page.tsx) — without it, a
// live rollout looks frozen on this page too until a manual reload.
const POLL_INTERVAL_MS = 15000;

// decodeURIComponent throws URIError on a malformed percent-escape (e.g. a
// lone "%" from a hand-edited URL or bad bookmark) — with no error.tsx
// anywhere in this app, an uncaught throw here would crash the whole page
// render instead of just failing the eventual API lookup cleanly.
function safeDecodeURIComponent(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

export default function UnitDetailPage() {
  const params = useParams<{ project: string; app: string }>();
  const project = safeDecodeURIComponent(params.project);
  const app = safeDecodeURIComponent(params.app);

  const [unit, setUnit] = useState<Unit | null>(null);
  const [events, setEvents] = useState<SyncEvent[] | null>(null);
  const [unitError, setUnitError] = useState<string | null>(null);
  const [eventsError, setEventsError] = useState<string | null>(null);
  // History is RBAC-gated the same as triggering a sync (sync_events.error
  // carries raw deploy/DB error text) — a viewer without sync rights for
  // this unit gets a 403, which is an expected, clean "no access" state,
  // not a real failure worth a destructive-styled error banner.
  const [eventsForbidden, setEventsForbidden] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  // Distinguishes "refetching after a sync/poll" from the initial load —
  // without it, a just-synced success indicator could sit next to a diff
  // view still showing the pre-sync state until the refetch resolves, with
  // no signal that a fresher answer is on the way.
  const [refreshing, setRefreshing] = useState(false);
  const [lastUpdatedAt, setLastUpdatedAt] = useState<Date | null>(null);

  // Promise.allSettled, not .all: a failing history fetch shouldn't blank
  // out the diff view (or vice versa) — each result applies independently.
  useEffect(() => {
    let cancelled = false;
    Promise.allSettled([getUnit(project, app), getUnitHistory(project, app)])
      .then(([u, h]) => {
        if (cancelled) return;
        if (u.status === "fulfilled") {
          setUnit(u.value);
          setUnitError(null);
          setLastUpdatedAt(new Date());
        } else {
          setUnitError(
            u.reason instanceof Error
              ? u.reason.message
              : "Failed to load unit",
          );
        }
        if (h.status === "fulfilled") {
          setEvents(h.value);
          setEventsError(null);
          setEventsForbidden(false);
        } else if (h.reason instanceof ApiError && h.reason.status === 403) {
          setEventsError(null);
          setEventsForbidden(true);
        } else {
          setEventsError(
            h.reason instanceof Error
              ? h.reason.message
              : "Failed to load sync history",
          );
          setEventsForbidden(false);
        }
      })
      .finally(() => {
        if (!cancelled) setRefreshing(false);
      });
    return () => {
      cancelled = true;
    };
  }, [project, app, refreshKey]);

  usePolling(POLL_INTERVAL_MS, () => setRefreshKey((k) => k + 1));

  const degraded =
    !!unit &&
    (unit.status === "Degraded" ||
      unit.status === "Invalid" ||
      unit.health === "Degraded" ||
      unit.health === "Invalid");
  // Only shown as "the reason" if the *most recent* sync attempt is itself
  // the failure — events is sorted most-recent-first, so if anything newer
  // has happened since (a later success, or even a later in_progress
  // attempt), an older failure's error text is likely stale/resolved and
  // showing it would be actively misleading. A wall-clock cutoff instead
  // (e.g. "only if within the last hour") would get this backwards: it'd
  // hide a real, still-unresolved error exactly when it's been failing the
  // longest and someone finally checks.
  const recentFailure =
    events?.[0]?.result === "failed" ? events[0] : undefined;

  return (
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-6 p-6">
      <div>
        <Link
          href="/"
          className="text-muted-foreground mb-2 inline-flex items-center gap-1 text-sm hover:underline"
        >
          <ArrowLeft className="size-3.5" />
          All sync units
        </Link>
        <div className="flex items-start justify-between gap-4">
          <div>
            <h1 className="text-2xl font-semibold tracking-tight">{app}</h1>
            <p className="text-muted-foreground text-sm">{project}</p>
          </div>
          <div className="flex flex-col items-end gap-1">
            {unit && (
              <SyncButton
                unit={unit}
                onSynced={() => {
                  setRefreshing(true);
                  setRefreshKey((k) => k + 1);
                }}
              />
            )}
            {lastUpdatedAt && (
              <p className="text-muted-foreground text-xs">
                Updated {lastUpdatedAt.toLocaleTimeString()}
              </p>
            )}
          </div>
        </div>
      </div>

      {unitError && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>Failed to load unit</AlertTitle>
          <AlertDescription>{unitError}</AlertDescription>
        </Alert>
      )}

      {eventsError && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>Failed to load sync history</AlertTitle>
          <AlertDescription>{eventsError}</AlertDescription>
        </Alert>
      )}

      {!unitError && degraded && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>
            {unit!.status === "Degraded" || unit!.status === "Invalid"
              ? `Sync status: ${unit!.status}`
              : `Health: ${unit!.health}`}
          </AlertTitle>
          <AlertDescription>
            {recentFailure?.error ??
              "This unit is out of a healthy, synced state. See the diff and sync history below."}
          </AlertDescription>
        </Alert>
      )}

      {!unit && !unitError ? (
        <Skeleton className="h-48 w-full" />
      ) : (
        unit && (
          <div className="relative">
            <DiffView unit={unit} />
            {refreshing && (
              <p className="text-muted-foreground absolute top-2 right-2 flex items-center gap-1 text-xs">
                <RefreshCw className="size-3 animate-spin" />
                Refreshing…
              </p>
            )}
          </div>
        )
      )}

      <div>
        <h2 className="mb-2 text-lg font-semibold tracking-tight">
          Sync history
        </h2>
        {eventsForbidden ? (
          <p className="text-muted-foreground text-sm">
            You don&apos;t have sync access to this app/project, so its history
            isn&apos;t shown here.
          </p>
        ) : !events && !eventsError ? (
          <Skeleton className="h-32 w-full" />
        ) : (
          events && <HistoryTable events={events} />
        )}
      </div>
    </main>
  );
}
