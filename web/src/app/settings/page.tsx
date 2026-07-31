"use client";

import { useEffect, useState } from "react";
import { AlertCircle, KeyRound, Layers, Palette } from "lucide-react";
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
import { listRbac, listUnits } from "@/lib/api";
import type { RbacRule, Unit } from "@/lib/types";

function groupEnvironments(units: Unit[]) {
  const byEnv = new Map<
    string,
    { apps: Set<string>; projects: Set<string>; regions: Set<string>; auto: number }
  >();
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

export default function SettingsPage() {
  const [units, setUnits] = useState<Unit[] | null>(null);
  const [roles, setRoles] = useState<RbacRule[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    Promise.all([listUnits(), listRbac()])
      .then(([u, r]) => {
        if (!cancelled) {
          setUnits(u);
          setRoles(r);
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

  return (
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-6 p-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>
        <p className="text-muted-foreground text-sm">
          Appearance, configured environments, and who can sync what.
        </p>
      </div>

      {error && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>Failed to load settings</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
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
