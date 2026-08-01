import { ArrowRight, GitCompare, Info, MapPin, Zap, ZapOff } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { StatusBadge } from "@/components/status-badge";
import type { Unit } from "@/lib/types";

function ImageValue({ digest }: { digest?: string }) {
  if (!digest) {
    return <span className="text-muted-foreground italic">not yet observed</span>;
  }
  return <code className="text-xs break-all">{digest}</code>;
}

// Per-unit diff view: desired vs live state, per §5.11.
export function DiffView({ unit }: { unit: Unit }) {
  const inSync =
    !!unit.desiredImage && unit.desiredImage === unit.liveImage;
  const hasExclusions =
    (unit.ignoreFields?.length ?? 0) > 0 ||
    (unit.ignorePreconditions?.length ?? 0) > 0;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <GitCompare className="text-primary size-4" />
          Desired vs Live
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex items-center gap-3">
          <StatusBadge value={unit.status} />
          <StatusBadge value={unit.health} />
          {inSync && (
            <span className="text-muted-foreground text-xs">
              image digest matches
            </span>
          )}
        </div>

        {hasExclusions && (
          // Status/Health can reflect a field or precondition this app
          // deliberately excludes from management (config.App.ignoreFields/
          // ignorePreconditions) — without this note, the image-only
          // comparison above can look contradictory (e.g. "image digest
          // matches" right under a red OutOfSync badge caused entirely by
          // an excluded field like traffic).
          <div className="bg-muted/50 flex items-start gap-2 rounded-md p-3 text-xs">
            <Info className="text-muted-foreground mt-0.5 size-3.5 shrink-0" />
            <div className="text-muted-foreground flex flex-col gap-1">
              {unit.ignoreFields && unit.ignoreFields.length > 0 && (
                <span>
                  This app doesn&apos;t manage: {unit.ignoreFields.join(", ")} —
                  Status may reflect a difference there that the image
                  comparison below can&apos;t show.
                </span>
              )}
              {unit.ignorePreconditions && unit.ignorePreconditions.length > 0 && (
                <span>
                  This app skips these preconditions:{" "}
                  {unit.ignorePreconditions.join(", ")}
                </span>
              )}
            </div>
          </div>
        )}

        <dl className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-3 text-sm">
          <dt className="text-muted-foreground">Desired image</dt>
          <dd>
            <ImageValue digest={unit.desiredImage} />
          </dd>

          <dt className="text-muted-foreground">Live image</dt>
          <dd>
            <ImageValue digest={unit.liveImage} />
          </dd>

          {!inSync && unit.desiredImage && unit.liveImage && (
            <>
              <dt className="text-muted-foreground">Transition</dt>
              <dd className="flex items-center gap-2">
                <span className="sr-only">from</span>
                <code className="text-xs break-all">{unit.liveImage}</code>
                <ArrowRight
                  className="text-muted-foreground size-3.5 shrink-0"
                  aria-hidden="true"
                />
                <span className="sr-only">to</span>
                <code className="text-xs break-all">{unit.desiredImage}</code>
              </dd>
            </>
          )}

          <dt className="text-muted-foreground">Region</dt>
          <dd className="flex items-center gap-1.5">
            <MapPin className="text-muted-foreground size-3.5" />
            {unit.region}
          </dd>

          <dt className="text-muted-foreground">Auto-sync</dt>
          <dd className="flex items-center gap-1.5">
            {unit.auto ? (
              <>
                <Zap className="size-3.5 text-emerald-600 dark:text-emerald-400" />
                enabled
              </>
            ) : (
              <>
                <ZapOff className="text-muted-foreground size-3.5" />
                gated (manual only)
              </>
            )}
          </dd>

          <dt className="text-muted-foreground">Last reconciled</dt>
          <dd>
            {unit.lastReconciledAt ? (
              new Date(unit.lastReconciledAt).toLocaleString()
            ) : (
              <span className="text-muted-foreground italic">never</span>
            )}
          </dd>
        </dl>
      </CardContent>
    </Card>
  );
}
