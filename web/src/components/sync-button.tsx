"use client";

import { useEffect, useRef, useState } from "react";
import { Check, RefreshCw } from "lucide-react";
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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { ApiError, syncUnit } from "@/lib/api";
import type { Unit } from "@/lib/types";

interface SyncButtonProps {
  unit: Pick<Unit, "app" | "project" | "canSync" | "observing">;
  onSynced?: () => void;
  size?: "default" | "sm";
}

// justSynced fades on its own after this long — long enough to notice,
// short enough not to linger once the caller's own refresh has already
// shown the new status.
const SUCCESS_MESSAGE_MS = 4000;

// Gated per §5.11: disabled entirely (not just erroring on click) when the
// logged-in user's RBAC scope doesn't cover this unit. canSync is computed
// server-side (internal/api/units.go) — the dashboard has no way to
// evaluate rbac.CanSync itself.
//
// A confirmation step before the real deploy fires (this triggers an
// actual Cloud Run revision, not a preview), plus explicit success
// feedback afterward — a one-click sync with only a spinner as feedback
// left no visible confirmation the sync actually happened.
export function SyncButton({
  unit,
  onSynced,
  size = "default",
}: SyncButtonProps) {
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [justSynced, setJustSynced] = useState(false);
  // Tracks the pending fade-out timer so a second sync attempt (or an
  // unmount) can cancel a still-running one from a prior attempt — a bare
  // setTimeout would otherwise fire regardless, clearing justSynced early
  // out from under a newer attempt's own success message, or firing a
  // setState call after the component's gone.
  const fadeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (fadeTimer.current) clearTimeout(fadeTimer.current);
    };
  }, []);

  const syncAllowed = unit.canSync && !unit.observing;

  // canSync is re-derived server-side and can flip false mid-session (an
  // RBAC hot-reload landing between polls) while the confirm dialog is
  // open. Rendering the disabled Tooltip branch below unconditionally on
  // !syncAllowed would unmount the open AlertDialog out from under the
  // user instead of closing it — adjusting state during render (not in an
  // effect) closes it first, within the same render that noticed the flip.
  const [prevSyncAllowed, setPrevSyncAllowed] = useState(syncAllowed);
  if (syncAllowed !== prevSyncAllowed) {
    setPrevSyncAllowed(syncAllowed);
    if (!syncAllowed) setOpen(false);
  }

  async function handleConfirm() {
    if (fadeTimer.current) {
      clearTimeout(fadeTimer.current);
      fadeTimer.current = null;
    }
    setPending(true);
    setError(null);
    try {
      await syncUnit(unit.project, unit.app);
      // Only close on success — closing immediately regardless of outcome
      // (the AlertDialogAction primitive's default behavior) meant a
      // failed sync's error text appeared after the modal had already
      // dismissed, easy to miss entirely.
      setOpen(false);
      setJustSynced(true);
      onSynced?.();
      fadeTimer.current = setTimeout(() => {
        setJustSynced(false);
        fadeTimer.current = null;
      }, SUCCESS_MESSAGE_MS);
    } catch (err) {
      // Left open so the failure is seen where the user is already
      // looking, not in small text below a button they've stopped
      // watching.
      setError(err instanceof ApiError ? err.message : "Sync failed");
    } finally {
      setPending(false);
    }
  }

  const button = (
    <Button size={size} variant="outline" disabled={!syncAllowed || pending}>
      <RefreshCw className={pending ? "animate-spin" : undefined} />
      Sync
    </Button>
  );

  return (
    <div className="flex flex-col items-end gap-1">
      {syncAllowed || open ? (
        <AlertDialog
          open={open}
          onOpenChange={(next) => {
            if (pending) return; // don't let Esc/backdrop close mid-request
            setOpen(next);
            if (!next) setError(null);
          }}
        >
          <AlertDialogTrigger render={button} />
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Sync {unit.app}?</AlertDialogTitle>
              <AlertDialogDescription>
                This deploys {unit.project}/{unit.app}&apos;s desired image to
                Cloud Run right now. To undo it, revert the Git commit —
                there&apos;s no separate rollback here.
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
                disabled={pending}
              >
                {pending && <RefreshCw className="animate-spin" />}
                {error ? "Retry" : "Sync now"}
              </Button>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      ) : (
        <Tooltip>
          <TooltipTrigger render={<span tabIndex={0} />}>
            {button}
          </TooltipTrigger>
          <TooltipContent>
            {unit.observing
              ? "This app is in observe mode (sync.observe) — sync is disabled"
              : "You don't have permission to sync this app/project"}
          </TooltipContent>
        </Tooltip>
      )}
      {justSynced && (
        <p
          role="status"
          aria-live="polite"
          className="flex items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400"
        >
          <Check className="size-3.5" />
          Synced
        </p>
      )}
    </div>
  );
}
