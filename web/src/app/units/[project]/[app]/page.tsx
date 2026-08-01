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

// Same silent background poll as the units list (page.tsx) — without it, a
// live rollout looks frozen on this page too until a manual reload.
const POLL_INTERVAL_MS = 15000;

export default function UnitDetailPage() {
  const params = useParams<{ project: string; app: string }>();
  const project = decodeURIComponent(params.project);
  const app = decodeURIComponent(params.app);

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

  useEffect(() => {
    const id = setInterval(() => setRefreshKey((k) => k + 1), POLL_INTERVAL_MS);
    return () => clearInterval(id);
  }, []);

  const degraded =
    !!unit &&
    (unit.status === "Degraded" ||
      unit.status === "Invalid" ||
      unit.health === "Degraded" ||
      unit.health === "Invalid");
  const lastFailure = events?.find((e) => e.result === "failed");

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
          {unit && (
            <SyncButton
              unit={unit}
              onSynced={() => {
                setRefreshing(true);
                setRefreshKey((k) => k + 1);
              }}
            />
          )}
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
            {lastFailure?.error ??
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
