import { describe, expect, it } from "vitest";
import { groupEnvironments } from "./page";
import type { Unit } from "@/lib/types";

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
