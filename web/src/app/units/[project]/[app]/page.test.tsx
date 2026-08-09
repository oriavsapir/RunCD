import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import UnitDetailPage from "./page";
import * as api from "@/lib/api";
import type { SyncEvent, Unit } from "@/lib/types";

const { getMockParams, setMockParams } = vi.hoisted(() => {
  let current = { project: "example-prod-eu", app: "widget-api" };
  return {
    getMockParams: () => current,
    setMockParams: (next: typeof current) => {
      current = next;
    },
  };
});

vi.mock("next/navigation", () => ({
  useParams: () => getMockParams(),
}));

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

function event(overrides: Partial<SyncEvent>): SyncEvent {
  return {
    id: 1,
    trigger: "manual",
    toImage: "sha256:aaaa",
    startedAt: "2026-07-30T10:00:00Z",
    finishedAt: "2026-07-30T10:00:05Z",
    result: "succeeded",
    ...overrides,
  };
}

describe("UnitDetailPage", () => {
  beforeEach(() => {
    setMockParams({ project: "example-prod-eu", app: "widget-api" });
  });

  it("shows a 'no access' message (not a destructive alert) when history is forbidden", async () => {
    vi.spyOn(api, "getUnit").mockResolvedValue(unit({}));
    vi.spyOn(api, "getUnitHistory").mockRejectedValue(
      new api.ApiError("forbidden", 403),
    );

    render(<UnitDetailPage />);

    await waitFor(() =>
      expect(
        screen.getByText(/don't have sync access to this app\/project/i),
      ).toBeInTheDocument(),
    );
    expect(screen.queryByText(/failed to load sync history/i)).not.toBeInTheDocument();
    // DiffView still renders — the unit fetch succeeded independently.
    expect(screen.getByText(/desired vs live/i)).toBeInTheDocument();
  });

  it("shows a destructive error for a non-403 history failure while the diff view still renders", async () => {
    vi.spyOn(api, "getUnit").mockResolvedValue(unit({}));
    vi.spyOn(api, "getUnitHistory").mockRejectedValue(new Error("db exploded"));

    render(<UnitDetailPage />);

    await waitFor(() =>
      expect(screen.getByText(/failed to load sync history/i)).toBeInTheDocument(),
    );
    expect(screen.getByText("db exploded")).toBeInTheDocument();
    expect(screen.getByText(/desired vs live/i)).toBeInTheDocument();
  });

  it("shows a banner and no diff view when the unit fetch itself fails", async () => {
    vi.spyOn(api, "getUnit").mockRejectedValue(new Error("unit not found"));
    vi.spyOn(api, "getUnitHistory").mockResolvedValue([]);

    render(<UnitDetailPage />);

    await waitFor(() =>
      expect(screen.getByText(/failed to load unit/i)).toBeInTheDocument(),
    );
    expect(screen.getByText("unit not found")).toBeInTheDocument();
    expect(screen.queryByText(/desired vs live/i)).not.toBeInTheDocument();
  });

  it("shows the most recent failure's error in the degraded banner", async () => {
    vi.spyOn(api, "getUnit").mockResolvedValue(
      unit({ status: "Degraded", health: "Degraded" }),
    );
    vi.spyOn(api, "getUnitHistory").mockResolvedValue([
      event({ id: 2, result: "failed", error: "precondition failed: topic missing" }),
      event({ id: 1, result: "succeeded" }),
    ]);

    render(<UnitDetailPage />);

    await waitFor(() =>
      expect(screen.getByText(/sync status: degraded/i)).toBeInTheDocument(),
    );
    const banner = screen.getByText(/sync status: degraded/i).closest("div[role='alert']")!;
    expect(
      within(banner).getByText(/precondition failed: topic missing/i),
    ).toBeInTheDocument();
  });

  it("falls back to generic prose in the degraded banner when the most recent event isn't a failure", async () => {
    vi.spyOn(api, "getUnit").mockResolvedValue(
      unit({ status: "Synced", health: "Degraded" }),
    );
    vi.spyOn(api, "getUnitHistory").mockResolvedValue([
      event({ id: 1, result: "succeeded" }),
    ]);

    render(<UnitDetailPage />);

    await waitFor(() =>
      expect(screen.getByText(/^health: degraded$/i)).toBeInTheDocument(),
    );
    expect(
      screen.getByText(/out of a healthy, synced state/i),
    ).toBeInTheDocument();
  });

  it("ignores a stale response that resolves after navigating to a different unit (the cancelled-flag guard)", async () => {
    let resolveFirst!: (u: Unit) => void;
    const first = new Promise<Unit>((res) => {
      resolveFirst = res;
    });
    vi.spyOn(api, "getUnit")
      .mockImplementationOnce(() => first)
      .mockImplementationOnce(() => Promise.resolve(unit({ region: "us-east1" })));
    vi.spyOn(api, "getUnitHistory").mockResolvedValue([]);

    setMockParams({ project: "example-prod-eu", app: "widget-api" });
    const { rerender } = render(<UnitDetailPage />);

    // Navigate to a second unit before the first request has resolved —
    // this changes project/app in the fetch effect's deps, re-running it.
    setMockParams({ project: "example-prod-eu", app: "other-app" });
    rerender(<UnitDetailPage />);

    await waitFor(() => expect(screen.getByText("us-east1")).toBeInTheDocument());

    // The stale first response arrives late — it must not clobber the
    // newer data already on screen.
    resolveFirst(unit({ region: "stale-region" }));
    await Promise.resolve();
    await Promise.resolve();

    expect(screen.getByText("us-east1")).toBeInTheDocument();
    expect(screen.queryByText("stale-region")).not.toBeInTheDocument();
  });
});
