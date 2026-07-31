"use client";

import { useEffect, useState } from "react";
import {
  AlertCircle,
  Bell,
  BellOff,
  Clock,
  GitBranch,
  KeyRound,
  Layers,
  ListChecks,
  Palette,
  ShieldCheck,
} from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { ThemeToggle } from "@/components/theme-toggle";
import { getRuntimeConfig, listRbac, listUnits } from "@/lib/api";
import type { RbacRule, RuntimeConfig, Unit } from "@/lib/types";

interface EnvSummary {
  apps: Set<string>;
  projects: Set<string>;
  regions: Set<string>;
  auto: number;
}

function groupEnvironments(units: Unit[]): Map<string, EnvSummary> {
  const byEnv = new Map<string, EnvSummary>();
  for (const u of units) {
    const g = byEnv.get(u.env) ?? {
      apps: new Set<string>(),
      projects: new Set<string>(),
      regions: new Set<string>(),
      auto: 0,
    };
    g.apps.add(u.app);
    g.projects.add(u.project);
    g.regions.add(u.region);
    if (u.auto) g.auto += 1;
    byEnv.set(u.env, g);
  }
  return byEnv;
}

function StatTile({
  icon: Icon,
  label,
  value,
  className,
}: {
  icon: typeof Layers;
  label: string;
  value: string | number;
  className?: string;
}) {
  return (
    <div className="bg-card flex items-center gap-3 rounded-lg border p-3">
      <Icon className={`size-5 shrink-0 ${className ?? "text-primary"}`} />
      <div>
        <p className="text-lg leading-none font-semibold">{value}</p>
        <p className="text-muted-foreground text-xs">{label}</p>
      </div>
    </div>
  );
}

export default function SettingsPage() {
  const [units, setUnits] = useState<Unit[] | null>(null);
  const [roles, setRoles] = useState<RbacRule[] | null>(null);
  const [config, setConfig] = useState<RuntimeConfig | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    Promise.all([listUnits(), listRbac(), getRuntimeConfig()])
      .then(([u, r, c]) => {
        if (!cancelled) {
          setUnits(u);
          setRoles(r);
          setConfig(c);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Failed to load settings");
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const envGroups = units ? groupEnvironments(units) : null;
  const envNames = envGroups ? [...envGroups.keys()].sort() : [];
  const totalApps = units ? new Set(units.map((u) => u.app)).size : 0;
  const totalProjects = units ? new Set(units.map((u) => u.project)).size : 0;
  const totalAuto = units ? units.filter((u) => u.auto).length : 0;

  return (
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-6 p-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>
        <p className="text-muted-foreground text-sm">
          Appearance, live controller configuration, environments, and who
          can sync what.
        </p>
      </div>

      {error && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>Failed to load settings</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {units && envGroups && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <StatTile icon={Layers} label="Environments" value={envNames.length} />
          <StatTile icon={ListChecks} label="Apps" value={totalApps} />
          <StatTile icon={GitBranch} label="Projects" value={totalProjects} />
          <StatTile
            icon={ShieldCheck}
            label="Auto-sync"
            value={`${totalAuto}/${units.length}`}
          />
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Palette className="text-primary size-4" />
            Appearance
          </CardTitle>
          <CardDescription>
            Stored in this browser only — not a per-user account setting.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex items-center justify-between">
          <p className="text-sm">Theme</p>
          <ThemeToggle />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <GitBranch className="text-primary size-4" />
            Controller configuration
          </CardTitle>
          <CardDescription>
            Live from the running controller — where it reads config from,
            how often it polls, and what it&apos;s allowed to manage.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {!config && !error ? (
            <Skeleton className="h-32 w-full" />
          ) : (
            config && (
              <dl className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-3 text-sm">
                <dt className="text-muted-foreground">Config source</dt>
                <dd className="font-mono text-xs break-all">
                  {config.configRepo}@{config.configBranch}:{config.configPath}
                </dd>

                <dt className="text-muted-foreground">RBAC source</dt>
                <dd className="font-mono text-xs break-all">
                  {config.configRepo}@{config.configBranch}:{config.rbacPath}
                </dd>

                <dt className="text-muted-foreground">Reconcile interval</dt>
                <dd className="flex items-center gap-1.5">
                  <Clock className="text-muted-foreground size-3.5" />
                  every {config.reconcileIntervalSeconds}s
                </dd>

                <dt className="text-muted-foreground">Managed fields</dt>
                <dd className="flex flex-wrap gap-1">
                  {config.managedFields.length === 0 ? (
                    <span className="text-muted-foreground italic">none</span>
                  ) : (
                    config.managedFields.map((f) => (
                      <Badge key={f} variant="secondary" className="font-mono text-xs font-normal">
                        {f}
                      </Badge>
                    ))
                  )}
                </dd>

                <dt className="text-muted-foreground">Slack notifications</dt>
                <dd className="flex items-center gap-1.5">
                  {config.notificationsEnabled ? (
                    <>
                      <Bell className="size-3.5 text-emerald-600 dark:text-emerald-400" />
                      enabled
                    </>
                  ) : (
                    <>
                      <BellOff className="text-muted-foreground size-3.5" />
                      not configured
                    </>
                  )}
                </dd>
              </dl>
            )
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Layers className="text-primary size-4" />
            Environments
          </CardTitle>
          <CardDescription>
            Derived from runcd.yaml, as expanded into sync units.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {!units && !error ? (
            <Skeleton className="h-24 w-full" />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Environment</TableHead>
                  <TableHead>Projects</TableHead>
                  <TableHead>Apps</TableHead>
                  <TableHead>Regions</TableHead>
                  <TableHead>Auto-sync</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {envNames.map((env) => {
                  const g = envGroups!.get(env)!;
                  return (
                    <TableRow key={env}>
                      <TableCell className="font-medium uppercase">
                        {env}
                      </TableCell>
                      <TableCell>{g.projects.size}</TableCell>
                      <TableCell>{g.apps.size}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {[...g.regions].join(", ")}
                      </TableCell>
                      <TableCell>
                        {g.auto}/{g.apps.size}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <KeyRound className="text-primary size-4" />
            Access (RBAC)
          </CardTitle>
          <CardDescription>
            Roles from rbac.yaml. Only sync is gated by this — every view above
            is open to any authenticated user.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {!roles && !error ? (
            <Skeleton className="h-24 w-full" />
          ) : roles && roles.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              No roles configured — every sync request will be denied until
              rbac.yaml grants one.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Subject</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Scope</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {roles!.map((rule) => (
                  <TableRow
                    key={`${rule.subject}-${rule.role}-${rule.scope.join(",")}`}
                  >
                    <TableCell className="font-medium">
                      {rule.subject}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className={
                          rule.role === "admin"
                            ? "bg-accent text-accent-foreground border-accent"
                            : ""
                        }
                      >
                        {rule.role}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {rule.scope.map((s) => (
                          <Badge
                            key={s}
                            variant="secondary"
                            className="font-mono text-xs font-normal"
                          >
                            {s}
                          </Badge>
                        ))}
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </main>
  );
}
