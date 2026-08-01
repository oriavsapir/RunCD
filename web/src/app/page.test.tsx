import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Home from "./page";
import * as api from "@/lib/api";
import type { Unit } from "@/lib/types";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn() }),
  usePathname: () => "/",
  useSearchParams: () => new URLSearchParams(),
}));

function unit(overrides: Partial<Unit>): Unit {
  return {
    app: "widget-api",
    project: "acme",
    env: "prd",
    region: "us-central1",
    auto: true,
    status: "Synced",
    health: "Healthy",
    canSync: true,
    observing: false,
    ...overrides,
  };
}

describe("Home", () => {
  it("clicking a project card shows only that exact project, not substring matches", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([
      unit({ app: "widget-api", project: "acme" }),
      unit({ app: "other-app", project: "acme-staging" }),
    ]);

    render(<Home />);

    await waitFor(() => expect(screen.getByText("acme")).toBeInTheDocument());
    await userEvent.click(screen.getByText("acme"));

    await waitFor(() =>
      expect(screen.getByText("widget-api")).toBeInTheDocument(),
    );
    expect(screen.queryByText("other-app")).not.toBeInTheDocument();
  });
});
