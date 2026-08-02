"use client";

import { useEffect, useRef, useState } from "react";
import { CheckCircle2, Layers, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { ApiError, syncBatch } from "@/lib/api";
import type { BatchSyncResult, Unit } from "@/lib/types";

interface SyncAllButtonProps {
  // Already scoped to whatever this button should act on (e.g. filtered to
  // the currently-selected project) — this component doesn't do its own
  // project/text filtering, it just counts and confirms against what it's
  // given.
  units: Unit[];
  project?: string | null;
  mode: "outOfSync" | "all";
  onSynced?: () => void;
}

const SUMMARY_MESSAGE_MS = 8000;

function summarize(results: BatchSyncResult[]): string {
  const synced = results.filter(
    (r) => !r.skipped && r.status === "Synced",
  ).length;
  const attempted = results.filter((r) => !r.skipped).length;
  const forbidden = results.filter((r) => r.skipped === "forbidden").length;
  const observing = results.filter((r) => r.skipped === "observing").length;
  const inProgress = results.filter((r) => r.skipped === "inProgress").length;
  const errored = results.filter((r) => r.skipped === "error").length;

  const parts = [`${attempted} attempted (${synced} synced)`];
  if (forbidden > 0) parts.push(`${forbidden} skipped — no permission`);
  if (observing > 0) parts.push(`${observing} skipped — observe mode`);
  if (inProgress > 0) parts.push(`${inProgress} already syncing`);
  if (errored > 0) parts.push(`${errored} failed`);
  return parts.join(", ");
}

// Sync All / Sync out-of-sync — the ArgoCD-style bulk action this dashboard
// was missing, so a human isn't clicking Sync unit by unit across a whole
// environment. One button per mode; render both where a bulk action makes
// sense, not a single toggle, since there's no existing dropdown/radio
// primitive in this codebase worth adding just for this (§ ui conventions:
// prefer established libraries, avoid unrequested new dependencies).
export function SyncAllButton({
  units,
  project,
  mode,
  onSynced,
}: SyncAllButtonProps) {
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [summary, setSummary] = useState<string | null>(null);
  const summaryTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (summaryTimer.current) clearTimeout(summaryTimer.current);
    };
  }, []);

  const candidates =
    mode === "outOfSync" ? units.filter((u) => u.status !== "Synced") : units;
  const syncable = candidates.filter((u) => u.canSync && !u.observing);
  const scopeLabel = project ? `in ${project}` : "across every project";

  async function handleConfirm() {
    if (summaryTimer.current) {
      clearTimeout(summaryTimer.current);
      summaryTimer.current = null;
    }
    setPending(true);
    setError(null);
    try {
      const results = await syncBatch({
        project: project ?? undefined,
        onlyOutOfSync: mode === "outOfSync",
      });
      setOpen(false);
      setSummary(summarize(results));
      onSynced?.();
      summaryTimer.current = setTimeout(() => {
        setSummary(null);
        summaryTimer.current = null;
      }, SUMMARY_MESSAGE_MS);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Sync failed");
    } finally {
      setPending(false);
    }
  }

  const label = mode === "outOfSync" ? "Sync out-of-sync" : "Sync all";
  const button = (
    <Button
      size="sm"
      variant="outline"
      disabled={syncable.length === 0 || pending}
    >
      {mode === "outOfSync" ? (
        <RefreshCw className={pending ? "animate-spin" : undefined} />
      ) : (
        <Layers />
      )}
      {label}
      {syncable.length > 0 && ` (${syncable.length})`}
    </Button>
  );

  return (
    <div className="flex flex-col items-end gap-1">
      <AlertDialog
        open={open}
        onOpenChange={(next) => {
          if (pending) return;
          setOpen(next);
          if (!next) setError(null);
        }}
      >
        <AlertDialogTrigger render={button} />
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {mode === "outOfSync" ? "Sync out-of-sync units?" : "Sync all units?"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              This deploys {syncable.length}{" "}
              {syncable.length === 1 ? "unit's" : "units'"} desired image to
              Cloud Run right now, {scopeLabel}
              {mode === "outOfSync" ? " (only ones not already Synced)" : ""}.
              {candidates.length > syncable.length &&
                ` ${candidates.length - syncable.length} more will be skipped (no permission or observe mode).`}{" "}
              To undo any of it, revert the Git commit — there&apos;s no
              separate rollback here.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
            <Button
              variant="destructive"
              onClick={handleConfirm}
              disabled={pending || syncable.length === 0}
            >
              {pending && <RefreshCw className="animate-spin" />}
              {error ? "Retry" : `Sync ${syncable.length} now`}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {summary && (
        <p
          role="status"
          aria-live="polite"
          className="flex items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400"
        >
          <CheckCircle2 className="size-3.5" />
          {summary}
        </p>
      )}
    </div>
  );
}
