"use client";

import { useState } from "react";
import { Check, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
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
  unit: Pick<Unit, "app" | "project" | "canSync">;
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
export function SyncButton({ unit, onSynced, size = "default" }: SyncButtonProps) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [justSynced, setJustSynced] = useState(false);

  async function handleConfirm() {
    setPending(true);
    setError(null);
    setJustSynced(false);
    try {
      await syncUnit(unit.project, unit.app);
      setJustSynced(true);
      onSynced?.();
      setTimeout(() => setJustSynced(false), SUCCESS_MESSAGE_MS);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Sync failed");
    } finally {
      setPending(false);
    }
  }

  const button = (
    <Button size={size} variant="outline" disabled={!unit.canSync || pending}>
      <RefreshCw className={pending ? "animate-spin" : undefined} />
      Sync
    </Button>
  );

  return (
    <div className="flex flex-col items-end gap-1">
      {unit.canSync ? (
        <AlertDialog>
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
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction onClick={handleConfirm} disabled={pending}>
                Sync now
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      ) : (
        <Tooltip>
          <TooltipTrigger render={<span tabIndex={0} />}>
            {button}
          </TooltipTrigger>
          <TooltipContent>
            You don&apos;t have permission to sync this app/project
          </TooltipContent>
        </Tooltip>
      )}
      {justSynced && !error && (
        <p className="flex items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400">
          <Check className="size-3.5" />
          Synced
        </p>
      )}
      {error && <p className="text-destructive text-xs">{error}</p>}
    </div>
  );
}
