import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import OrphansPage from "./page";
import * as api from "@/lib/api";
import { ApiError } from "@/lib/api";

describe("OrphansPage", () => {
  it("shows the empty state when the scan finds nothing", async () => {
    vi.spyOn(api, "listOrphans").mockResolvedValue({
      orphans: [],
      partial: false,
    });
    render(<OrphansPage />);
    await waitFor(() =>
      expect(screen.getByText(/no orphans found/i)).toBeInTheDocument(),
    );
    expect(screen.queryByText(/may be incomplete/i)).not.toBeInTheDocument();
  });

  it("renders every orphan grouped under its project, only labeling the first row", async () => {
    vi.spyOn(api, "listOrphans").mockResolvedValue({
      orphans: [
        { project: "acme-prod", region: "us-central1", app: "old-worker" },
        { project: "acme-prod", region: "europe-west1", app: "stale-job" },
        { project: "acme-dev", region: "us-central1", app: "leftover" },
      ],
      partial: false,
    });
    render(<OrphansPage />);
    await waitFor(() =>
      expect(screen.getByText("old-worker")).toBeInTheDocument(),
    );
    expect(screen.getByText("stale-job")).toBeInTheDocument();
    expect(screen.getByText("leftover")).toBeInTheDocument();

    const rows = screen.getAllByRole("row").slice(1); // drop header row
    const acmeProdRows = rows.filter((r) =>
      r.textContent?.includes("old-worker") ||
      r.textContent?.includes("stale-job"),
    );
    expect(acmeProdRows[0].textContent).toContain("acme-prod");
    // Second row for the same project must not repeat the project label.
    const secondRowCells = acmeProdRows[1].querySelectorAll("td");
    expect(secondRowCells[0].textContent).toBe("");
  });

  it("shows a partial-scan warning without hiding the orphans it did find", async () => {
    vi.spyOn(api, "listOrphans").mockResolvedValue({
      orphans: [{ project: "acme-prod", region: "us-central1", app: "old-worker" }],
      partial: true,
    });
    render(<OrphansPage />);
    await waitFor(() =>
      expect(screen.getByText(/may be incomplete/i)).toBeInTheDocument(),
    );
    expect(screen.getByText("old-worker")).toBeInTheDocument();
  });

  it("shows a forbidden error with retry, and retry re-fetches", async () => {
    const spy = vi
      .spyOn(api, "listOrphans")
      .mockRejectedValueOnce(
        new ApiError("forbidden: no role grants sync access", 403),
      )
      .mockResolvedValueOnce({ orphans: [], partial: false });

    render(<OrphansPage />);
    await waitFor(() =>
      expect(screen.getByText(/forbidden: no role grants sync access/i)).toBeInTheDocument(),
    );

    await userEvent.click(screen.getByRole("button", { name: /retry/i }));
    await waitFor(() =>
      expect(screen.getByText(/no orphans found/i)).toBeInTheDocument(),
    );
    expect(spy).toHaveBeenCalledTimes(2);
  });

  it("disables Rescan while a Retry-triggered fetch is in flight, so a click can't fire a second concurrent scan", async () => {
    let resolveRetry!: (v: { orphans: []; partial: boolean }) => void;
    const spy = vi
      .spyOn(api, "listOrphans")
      .mockRejectedValueOnce(new ApiError("boom", 500))
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolveRetry = resolve;
        }),
      );

    render(<OrphansPage />);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument(),
    );

    await userEvent.click(screen.getByRole("button", { name: /retry/i }));
    // The fetch triggered by Retry hasn't resolved yet — both Retry and
    // Rescan must be disabled so a click can't start a second overlapping
    // scan (they share the same `refreshing` state).
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /retry/i })).toBeDisabled(),
    );
    expect(screen.getByRole("button", { name: /rescan/i })).toBeDisabled();

    resolveRetry({ orphans: [], partial: false });
    await waitFor(() =>
      expect(screen.getByText(/no orphans found/i)).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: /rescan/i })).not.toBeDisabled();
    expect(spy).toHaveBeenCalledTimes(2);
  });

  it("rescan re-fetches and drops a stale partial-scan warning once the new scan is complete", async () => {
    vi.spyOn(api, "listOrphans")
      .mockResolvedValueOnce({
        orphans: [{ project: "acme-prod", region: "us-central1", app: "old-worker" }],
        partial: true,
      })
      .mockResolvedValueOnce({
        orphans: [{ project: "acme-prod", region: "us-central1", app: "old-worker" }],
        partial: false,
      });

    render(<OrphansPage />);
    await waitFor(() =>
      expect(screen.getByText(/may be incomplete/i)).toBeInTheDocument(),
    );

    await userEvent.click(screen.getByRole("button", { name: /rescan/i }));
    await waitFor(() =>
      expect(screen.queryByText(/may be incomplete/i)).not.toBeInTheDocument(),
    );
    expect(screen.getByText("old-worker")).toBeInTheDocument();
  });
});
