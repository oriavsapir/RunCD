import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import SettingsPage, { groupEnvironments } from "./page";
import * as api from "@/lib/api";
import type { RbacRule, RuntimeConfig, Unit } from "@/lib/types";

function unit(overrides: Partial<Unit>): Unit {
  return {
    app: "widget-api",
    project: "example-prod-eu",
    env: "prd",
    region: "europe-west1",
    auto: true,
    status: "Synced",
    health: "Healthy",
    canSync: true,
    observing: false,
    ...overrides,
  };
}

describe("groupEnvironments", () => {
  it("counts auto-sync as a fraction of total sync units, not distinct apps", () => {
    // One app fanning out to 3 projects (all auto), plus a second app in
    // one project (also auto) — 4 units, 2 distinct apps, all auto-synced.
    // The auto-sync ratio must read 4/4 (units), not 4/2 (apps) — the
    // review finding this regression-tests.
    const units = [
      unit({ app: "widget-api", project: "example-prod-us", auto: true }),
      unit({ app: "widget-api", project: "example-prod-eu", auto: true }),
      unit({ app: "widget-api", project: "example-prod-asia", auto: true }),
      unit({ app: "notification-service", project: "example-prod-us", auto: true }),
    ];
    const groups = groupEnvironments(units);
    const prd = groups.get("prd")!;
    expect(prd.apps.size).toBe(2);
    expect(prd.units).toBe(4);
    expect(prd.auto).toBe(4);
  });

  it("only counts auto=true units toward the auto tally", () => {
    const units = [
      unit({ app: "widget-api", project: "example-prod-us", auto: true }),
      unit({ app: "widget-api", project: "example-prod-eu", auto: false }),
    ];
    const groups = groupEnvironments(units);
    const prd = groups.get("prd")!;
    expect(prd.units).toBe(2);
    expect(prd.auto).toBe(1);
  });
});

const baseConfig: RuntimeConfig = {
  configRepo: "git@github.com:acme/config.git",
  configBranch: "main",
  configPath: "runcd.yaml",
  rbacPath: "rbac.yaml",
  reconcileIntervalSeconds: 60,
  managedFields: ["env", "traffic"],
  notificationsEnabled: false,
};

const baseRole: RbacRule = {
  subject: "user:alice@company.com",
  role: "syncer",
  scope: ["env:prd"],
};

function unit2(overrides: Partial<Unit>): Unit {
  return {
    app: "widget-api",
    project: "example-prod-eu",
    env: "prd",
    region: "europe-west1",
    auto: true,
    status: "Synced",
    health: "Healthy",
    canSync: true,
    observing: false,
    ...overrides,
  };
}

describe("SettingsPage", () => {
  it("renders config, environments, and RBAC once every fetch succeeds", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([unit2({})]);
    vi.spyOn(api, "listRbac").mockResolvedValue([baseRole]);
    vi.spyOn(api, "getRuntimeConfig").mockResolvedValue(baseConfig);

    render(<SettingsPage />);

    await waitFor(() =>
      expect(screen.getByText(/every 60s/i)).toBeInTheDocument(),
    );
    expect(screen.getByText("user:alice@company.com")).toBeInTheDocument();
    expect(screen.getByText("prd")).toBeInTheDocument();
  });

  it("shows an error with a Retry button for RBAC failing, while units and config still render", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([unit2({})]);
    vi.spyOn(api, "listRbac").mockRejectedValue(new Error("rbac.yaml missing"));
    vi.spyOn(api, "getRuntimeConfig").mockResolvedValue(baseConfig);

    render(<SettingsPage />);

    await waitFor(() =>
      expect(screen.getByText("rbac.yaml missing")).toBeInTheDocument(),
    );
    // Sibling sections aren't blanked out by the RBAC failure.
    expect(screen.getByText(/every 60s/i)).toBeInTheDocument();
    expect(screen.getByText("prd")).toBeInTheDocument();
  });

  it("retrying after an error refetches", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([unit2({})]);
    const rbacSpy = vi
      .spyOn(api, "listRbac")
      .mockRejectedValueOnce(new Error("rbac.yaml missing"))
      .mockResolvedValueOnce([baseRole]);
    vi.spyOn(api, "getRuntimeConfig").mockResolvedValue(baseConfig);

    render(<SettingsPage />);

    await waitFor(() =>
      expect(screen.getByText("rbac.yaml missing")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByRole("button", { name: /retry/i }));

    await waitFor(() =>
      expect(screen.getByText("user:alice@company.com")).toBeInTheDocument(),
    );
    expect(rbacSpy).toHaveBeenCalledTimes(2);
  });

  it("disables the header Refresh button and the Retry button itself while a retry is in flight", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([unit2({})]);
    let resolveRetry!: (v: RbacRule[]) => void;
    vi.spyOn(api, "listRbac")
      .mockRejectedValueOnce(new Error("rbac.yaml missing"))
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolveRetry = resolve;
        }),
      );
    vi.spyOn(api, "getRuntimeConfig").mockResolvedValue(baseConfig);

    render(<SettingsPage />);
    await waitFor(() =>
      expect(screen.getByText("rbac.yaml missing")).toBeInTheDocument(),
    );

    await userEvent.click(screen.getByRole("button", { name: /retry/i }));
    // The retry's refetch hasn't resolved yet — both the section's own
    // Retry button and the header's Refresh button must be disabled so a
    // click can't start a second overlapping refetch of all three sections.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /retry/i })).toBeDisabled(),
    );
    expect(screen.getByRole("button", { name: /refresh/i })).toBeDisabled();

    resolveRetry([baseRole]);
    await waitFor(() =>
      expect(screen.getByText("user:alice@company.com")).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: /refresh/i })).not.toBeDisabled();
  });

  it("shows 'No roles configured' for an empty RBAC config instead of an empty table", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([unit2({})]);
    vi.spyOn(api, "listRbac").mockResolvedValue([]);
    vi.spyOn(api, "getRuntimeConfig").mockResolvedValue(baseConfig);

    render(<SettingsPage />);

    await waitFor(() =>
      expect(screen.getByText(/no roles configured/i)).toBeInTheDocument(),
    );
  });

  it("shows 'none' for empty managedFields instead of an empty badge list", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([unit2({})]);
    vi.spyOn(api, "listRbac").mockResolvedValue([baseRole]);
    vi.spyOn(api, "getRuntimeConfig").mockResolvedValue({
      ...baseConfig,
      managedFields: [],
    });

    render(<SettingsPage />);

    await waitFor(() => expect(screen.getByText(/^none$/i)).toBeInTheDocument());
  });

  it("renders the notifyByEnv table with sink and rules per environment", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([unit2({})]);
    vi.spyOn(api, "listRbac").mockResolvedValue([baseRole]);
    vi.spyOn(api, "getRuntimeConfig").mockResolvedValue({
      ...baseConfig,
      notifyByEnv: {
        prd: { sink: "slack-prod", rules: ["syncFailed", "healthDegraded"] },
      },
    });

    render(<SettingsPage />);

    await waitFor(() =>
      expect(screen.getByText("slack-prod")).toBeInTheDocument(),
    );
    expect(screen.getByText("syncFailed")).toBeInTheDocument();
    expect(screen.getByText("healthDegraded")).toBeInTheDocument();
  });
});
