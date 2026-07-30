"use client";

import { useState } from "react";
import { RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ApiError, syncUnit } from "@/lib/api";
import type { Unit } from "@/lib/types";

interface SyncButtonProps {
  unit: Pick<Unit, "app" | "project" | "canSync">;
  onSynced?: () => void;
  size?: "default" | "sm";
}

// Gated per §5.11: disabled entirely (not just erroring on click) when the
// logged-in user's RBAC scope doesn't cover this unit. canSync is computed
// server-side (internal/api/units.go) — the dashboard has no way to
// evaluate rbac.CanSync itself.
export function SyncButton({ unit, onSynced, size = "default" }: SyncButtonProps) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleClick() {
    setPending(true);
    setError(null);
    try {
      await syncUnit(unit.project, unit.app);
      onSynced?.();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Sync failed");
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="flex flex-col items-end gap-1">
      <Button
        size={size}
        variant="outline"
        disabled={!unit.canSync || pending}
        onClick={handleClick}
        title={
          unit.canSync
            ? undefined
            : "You don't have permission to sync this app/project"
        }
      >
        <RefreshCw className={pending ? "animate-spin" : undefined} />
        Sync
      </Button>
      {error && <p className="text-destructive text-xs">{error}</p>}
    </div>
  );
}
