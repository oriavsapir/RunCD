import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, useState } from "react";
import Home from "./page";
import * as api from "@/lib/api";
import type { Unit } from "@/lib/types";

// A minimal reactive fake of next/navigation's router: replace() actually
// updates the "URL" (not just records the call), and useSearchParams
// re-renders subscribers — mirroring how a real router.replace causes
// Next to re-render the page with updated searchParams. Without this, the
// page's derive-from-URL rendering (no local view/selectedProject state)
// couldn't be exercised by clicking through the UI at all.
const { getParams, setParams, replaceMock, subscribe } = vi.hoisted(() => {
  let params = new URLSearchParams();
  const listeners = new Set<() => void>();
  const notify = () => listeners.forEach((l) => l());
  return {
    getParams: () => params,
    setParams: (next: URLSearchParams) => {
      params = next;
      notify();
    },
    replaceMock: vi.fn((url: string) => {
      const qs = url.includes("?") ? url.slice(url.indexOf("?") + 1) : "";
      params = new URLSearchParams(qs);
      notify();
    }),
    subscribe: (fn: () => void) => {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
  };
});

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: replaceMock }),
  usePathname: () => "/",
  useSearchParams: () => {
    const [, forceRender] = useState(0);
    useEffect(() => subscribe(() => forceRender((n) => n + 1)), []);
    return getParams();
  },
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
  beforeEach(() => {
    setParams(new URLSearchParams());
    replaceMock.mockClear();
  });

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
    expect(replaceMock).toHaveBeenCalledWith(
      "/?project=acme",
      expect.objectContaining({ scroll: false }),
    );
  });

  it("a ?project= URL with no view lands on the filtered table, not the project grid", async () => {
    setParams(new URLSearchParams("project=acme"));
    vi.spyOn(api, "listUnits").mockResolvedValue([
      unit({ app: "widget-api", project: "acme" }),
      unit({ app: "other-app", project: "acme-staging" }),
    ]);

    render(<Home />);

    await waitFor(() =>
      expect(screen.getByText("widget-api")).toBeInTheDocument(),
    );
    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(screen.queryByText("other-app")).not.toBeInTheDocument();
  });

  it("picks up an external URL change (e.g. browser Back) and updates the displayed filter", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([
      unit({ app: "widget-api", project: "acme" }),
      unit({ app: "other-app", project: "acme-staging" }),
    ]);

    render(<Home />);
    await waitFor(() => expect(screen.getByText("acme")).toBeInTheDocument());

    setParams(new URLSearchParams("project=acme-staging&view=table"));

    await waitFor(() =>
      expect(screen.getByText("other-app")).toBeInTheDocument(),
    );
    expect(screen.queryByText("widget-api")).not.toBeInTheDocument();
  });
});
