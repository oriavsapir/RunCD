import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SyncButton } from "./sync-button";
import * as api from "@/lib/api";

describe("SyncButton", () => {
  it("is disabled when canSync is false — the RBAC gate (§5.11)", () => {
    render(
      <SyncButton unit={{ app: "widget-api", project: "example-prod-eu", canSync: false }} />,
    );
    expect(screen.getByRole("button", { name: /sync/i })).toBeDisabled();
  });

  it("shows a confirmation dialog before syncing, and calls syncUnit only after confirming", async () => {
    const spy = vi.spyOn(api, "syncUnit").mockResolvedValue({
      app: "widget-api",
      project: "example-prod-eu",
      status: "Synced",
      health: "Healthy",
    });
    const onSynced = vi.fn();
    render(
      <SyncButton
        unit={{ app: "widget-api", project: "example-prod-eu", canSync: true }}
        onSynced={onSynced}
      />,
    );

    const button = screen.getByRole("button", { name: /sync/i });
    expect(button).toBeEnabled();
    await userEvent.click(button);

    expect(spy).not.toHaveBeenCalled();
    const confirm = await screen.findByRole("button", { name: /sync now/i });
    await userEvent.click(confirm);

    await waitFor(() => expect(onSynced).toHaveBeenCalledTimes(1));
    expect(spy).toHaveBeenCalledWith("example-prod-eu", "widget-api");
    expect(await screen.findByText(/synced/i)).toBeInTheDocument();
  });

  it("cancelling the confirmation never calls syncUnit", async () => {
    const spy = vi.spyOn(api, "syncUnit").mockResolvedValue({
      app: "widget-api",
      project: "example-prod-eu",
      status: "Synced",
      health: "Healthy",
    });
    render(
      <SyncButton unit={{ app: "widget-api", project: "example-prod-eu", canSync: true }} />,
    );

    await userEvent.click(screen.getByRole("button", { name: /sync/i }));
    await userEvent.click(await screen.findByRole("button", { name: /cancel/i }));

    expect(spy).not.toHaveBeenCalled();
  });

  it("shows the server error inline instead of silently failing", async () => {
    vi.spyOn(api, "syncUnit").mockRejectedValue(
      new api.ApiError("forbidden: no role grants sync access", 403),
    );
    render(
      <SyncButton unit={{ app: "widget-api", project: "example-prod-eu", canSync: true }} />,
    );

    await userEvent.click(screen.getByRole("button", { name: /sync/i }));
    await userEvent.click(await screen.findByRole("button", { name: /sync now/i }));

    await waitFor(() =>
      expect(
        screen.getByText(/forbidden: no role grants sync access/i),
      ).toBeInTheDocument(),
    );
  });
});
