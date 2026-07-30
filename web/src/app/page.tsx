"use client";

import { useEffect, useState } from "react";
import { AlertCircle } from "lucide-react";
import { UnitTable } from "@/components/unit-table";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { listUnits } from "@/lib/api";
import type { Unit } from "@/lib/types";

export default function Home() {
  const [units, setUnits] = useState<Unit[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);

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

  return (
    <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-6 p-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Sync Units</h1>
        <p className="text-muted-foreground text-sm">
          Every configured sync unit and its last-known status.
        </p>
      </div>

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
          <UnitTable
            units={units}
            onSynced={() => setRefreshKey((k) => k + 1)}
          />
        )
      )}
    </main>
  );
}
