import { CheckCircle2, Loader2, XCircle } from "lucide-react";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import type { SyncEvent, SyncResult } from "@/lib/types";

const RESULT_CONFIG: Record<
  SyncResult,
  { icon: typeof CheckCircle2; spin?: boolean; className: string }
> = {
  succeeded: {
    icon: CheckCircle2,
    className:
      "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-800",
  },
  failed: {
    icon: XCircle,
    className:
      "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-800",
  },
  in_progress: {
    icon: Loader2,
    spin: true,
    className:
      "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950 dark:text-blue-300 dark:border-blue-800",
  },
};

function ResultBadge({ result }: { result: SyncResult }) {
  const config = RESULT_CONFIG[result];
  const Icon = config.icon;
  return (
    <Badge
      variant="outline"
      className={`gap-1 font-medium ${config.className}`}
    >
      <Icon className={`size-3 ${config.spin ? "animate-spin" : ""}`} />
      {result}
    </Badge>
  );
}

function shortDigest(digest?: string): string {
  if (!digest) return "—";
  const i = digest.indexOf(":");
  return i === -1 ? digest.slice(0, 12) : digest.slice(0, i + 9);
}

// For in_progress rows this is elapsed-so-far, not a fixed duration — so a
// row stuck from a mid-deploy controller crash is visibly stale rather than
// looking like it just started.
function formatElapsed(startedAt: string, finishedAt?: string): string {
  const ms =
    (finishedAt ? new Date(finishedAt) : new Date()).getTime() -
    new Date(startedAt).getTime();
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  return `${Math.round(minutes / 60)}h`;
}

export function HistoryTable({ events }: { events: SyncEvent[] }) {
  if (events.length === 0) {
    return (
      <p className="text-muted-foreground text-sm">
        No sync attempts recorded yet for this unit.
      </p>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Started</TableHead>
          <TableHead>Trigger</TableHead>
          <TableHead>Actor</TableHead>
          <TableHead>Image</TableHead>
          <TableHead>Duration</TableHead>
          <TableHead>Result</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((e) => (
          <TableRow key={e.id}>
            <TableCell className="text-muted-foreground whitespace-nowrap">
              {new Date(e.startedAt).toLocaleString()}
            </TableCell>
            <TableCell>{e.trigger}</TableCell>
            <TableCell>{e.actor || "—"}</TableCell>
            <TableCell className="font-mono text-xs">
              {shortDigest(e.fromImage)} → {shortDigest(e.toImage)}
            </TableCell>
            <TableCell className="text-muted-foreground whitespace-nowrap">
              {formatElapsed(e.startedAt, e.finishedAt)}
              {e.result === "in_progress" && " so far"}
            </TableCell>
            <TableCell>
              <ResultBadge result={e.result} />
              {e.error && (
                <p className="text-destructive mt-1 max-w-xs text-xs break-words">
                  {e.error}
                </p>
              )}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
