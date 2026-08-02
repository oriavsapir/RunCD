import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SyncAllButton } from "./sync-all-button";
import * as api from "@/lib/api";
import type { Unit } from "@/lib/types";

function unit(overrides: Partial<Unit>): Unit {
  return {
    app: "app",
    project: "proj",
    env: "env",
    region: "us-central1",
    auto: false,
    status: "OutOfSync",
    health: "Missing",
    canSync: true,
    observing: false,
    ...overrides,
  };
}

describe("SyncAllButton", () => {
  it("mode=outOfSync only counts units that aren't already Synced", () => {
    render(
      <SyncAllButton
        mode="outOfSync"
        units={[
          unit({ app: "a", status: "OutOfSync" }),
          unit({ app: "b", status: "Synced" }),
          unit({ app: "c", status: "Missing" }),
        ]}
      />,
    );
    expect(
      screen.getByRole("button", { name: /sync out-of-sync \(2\)/i }),
    ).toBeInTheDocument();
  });

  it("mode=all counts every syncable unit regardless of status", () => {
    render(
      <SyncAllButton
        mode="all"
        units={[
          unit({ app: "a", status: "OutOfSync" }),
          unit({ app: "b", status: "Synced" }),
        ]}
      />,
    );
    expect(
      screen.getByRole("button", { name: /sync all \(2\)/i }),
    ).toBeInTheDocument();
  });

  it("excludes units the caller can't sync or that are observing from the count", () => {
    render(
      <SyncAllButton
        mode="all"
        units={[
          unit({ app: "a", canSync: true }),
          unit({ app: "b", canSync: false }),
          unit({ app: "c", observing: true }),
        ]}
      />,
    );
    expect(
      screen.getByRole("button", { name: /sync all \(1\)/i }),
    ).toBeInTheDocument();
  });

  it("is disabled with nothing syncable", () => {
    render(
      <SyncAllButton mode="all" units={[unit({ app: "a", canSync: false })]} />,
    );
    expect(screen.getByRole("button", { name: /sync all/i })).toBeDisabled();
  });

  it("confirms before calling syncBatch, passing project and filter", async () => {
    const spy = vi.spyOn(api, "syncBatch").mockResolvedValue([
      { app: "a", project: "proj", status: "Synced", health: "Healthy" },
    ]);
    const onSynced = vi.fn();
    render(
      <SyncAllButton
        mode="outOfSync"
        project="proj"
        units={[unit({ app: "a" })]}
        onSynced={onSynced}
      />,
    );

    await userEvent.click(
      screen.getByRole("button", { name: /sync out-of-sync \(1\)/i }),
    );
    expect(spy).not.toHaveBeenCalled();

    await userEvent.click(await screen.findByRole("button", { name: /sync 1 now/i }));

    await waitFor(() => expect(onSynced).toHaveBeenCalledTimes(1));
    expect(spy).toHaveBeenCalledWith({ project: "proj", onlyOutOfSync: true });
    expect(await screen.findByText(/1 attempted \(1 synced\)/i)).toBeInTheDocument();
  });

  it("summarizes skipped units after a batch sync", async () => {
    vi.spyOn(api, "syncBatch").mockResolvedValue([
      { app: "a", project: "proj", status: "Synced", health: "Healthy" },
      { app: "b", project: "proj", skipped: "forbidden" },
      { app: "c", project: "proj", skipped: "observing" },
    ]);
    render(
      <SyncAllButton
        mode="all"
        units={[unit({ app: "a" }), unit({ app: "b" }), unit({ app: "c" })]}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: /sync all \(3\)/i }));
    await userEvent.click(await screen.findByRole("button", { name: /sync 3 now/i }));

    expect(
      await screen.findByText(
        /1 attempted \(1 synced\), 1 skipped — no permission, 1 skipped — observe mode/i,
      ),
    ).toBeInTheDocument();
  });

  it("shows the server error inline instead of silently failing", async () => {
    vi.spyOn(api, "syncBatch").mockRejectedValue(
      new api.ApiError("sync failed", 500),
    );
    render(<SyncAllButton mode="all" units={[unit({ app: "a" })]} />);

    await userEvent.click(screen.getByRole("button", { name: /sync all \(1\)/i }));
    await userEvent.click(await screen.findByRole("button", { name: /sync 1 now/i }));

    await waitFor(() =>
      expect(screen.getByText(/sync failed/i)).toBeInTheDocument(),
    );
  });
});
