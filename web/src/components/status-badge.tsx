import {
  AlertTriangle,
  Briefcase,
  CheckCircle2,
  Clock,
  HelpCircle,
  Loader2,
  XCircle,
  type LucideIcon,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

interface StatusConfig {
  icon: LucideIcon;
  spin?: boolean;
  className: string;
}

// Covers every value internal/reconcile's Status and Health enums can
// produce, plus the dashboard-only "Pending" sentinel for a unit that
// hasn't been reconciled yet.
const STATUS_CONFIG: Record<string, StatusConfig> = {
  Synced: {
    icon: CheckCircle2,
    className:
      "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-800",
  },
  Healthy: {
    icon: CheckCircle2,
    className:
      "bg-emerald-50 text-emerald-700 border-emerald-200 dark:bg-emerald-950 dark:text-emerald-300 dark:border-emerald-800",
  },
  OutOfSync: {
    icon: AlertTriangle,
    className:
      "bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-800",
  },
  Progressing: {
    icon: Loader2,
    spin: true,
    className:
      "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950 dark:text-blue-300 dark:border-blue-800",
  },
  Degraded: {
    icon: AlertTriangle,
    className:
      "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-800",
  },
  Invalid: {
    icon: XCircle,
    className:
      "bg-red-50 text-red-700 border-red-200 dark:bg-red-950 dark:text-red-300 dark:border-red-800",
  },
  Missing: {
    icon: HelpCircle,
    className:
      "bg-neutral-100 text-neutral-600 border-neutral-200 dark:bg-neutral-900 dark:text-neutral-400 dark:border-neutral-800",
  },
  Pending: {
    icon: Clock,
    className:
      "bg-neutral-100 text-neutral-600 border-neutral-200 dark:bg-neutral-900 dark:text-neutral-400 dark:border-neutral-800",
  },
};

const FALLBACK_CONFIG: StatusConfig = STATUS_CONFIG.Missing;

export function StatusBadge({ value }: { value: string }) {
  const config = STATUS_CONFIG[value] ?? FALLBACK_CONFIG;
  const Icon = config.icon;
  return (
    <Badge
      variant="outline"
      className={cn("gap-1 font-medium", config.className)}
    >
      <Icon className={cn("size-3", config.spin && "animate-spin")} />
      {value}
    </Badge>
  );
}

// HealthBadge is StatusBadge's counterpart specifically for the Health
// column: a job runs to completion and stops, so Healthy/Progressing/
// Missing for one is really "did the most recent execution succeed" — a
// fundamentally different, noisier signal than a service's continuous
// up/down state (and one that can flip from executions RunCD never
// triggered, e.g. an external scheduler). Rather than dress that up as a
// health status, a job's Health column just says "Job."
export function HealthBadge({
  health,
  resourceType,
}: {
  health: string;
  resourceType?: string;
}) {
  if (resourceType === "job") {
    return (
      <Badge
        variant="outline"
        className="gap-1 font-medium bg-neutral-100 text-neutral-600 border-neutral-200 dark:bg-neutral-900 dark:text-neutral-400 dark:border-neutral-800"
      >
        <Briefcase className="size-3" />
        Job
      </Badge>
    );
  }
  return <StatusBadge value={health} />;
}
