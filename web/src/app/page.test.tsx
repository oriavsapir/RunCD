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

  it("clearing the project filter chip also clears a stale search query", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([
      unit({ app: "widget-api", project: "acme" }),
      unit({ app: "other-app", project: "acme-staging" }),
    ]);

    render(<Home />);
    await waitFor(() => expect(screen.getByText("acme")).toBeInTheDocument());

    // Type a query first, then select a project — selecting a project
    // doesn't clear a pre-existing query, it's just superseded while a
    // project filter is active. "widget" still leaves the "acme" card
    // visible (it still has a matching unit), unlike a query that would
    // filter the card away entirely before it could be clicked.
    await userEvent.type(
      screen.getByPlaceholderText(/filter by app, project/i),
      "widget",
    );
    await userEvent.click(screen.getByText("acme"));
    await waitFor(() =>
      expect(screen.getByText("widget-api")).toBeInTheDocument(),
    );

    await userEvent.click(
      screen.getByRole("button", { name: /clear project filter/i }),
    );

    // Without the fix, the stale "widget" query would silently reactivate
    // once the project filter cleared, hiding other-app again.
    await waitFor(() =>
      expect(screen.getByText("widget-api")).toBeInTheDocument(),
    );
    expect(screen.getByText("other-app")).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/filter by app, project/i)).toHaveValue(
      "",
    );
  });

  it("typing in search while no project is selected doesn't call router.replace", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([
      unit({ app: "widget-api", project: "acme" }),
      unit({ app: "other-app", project: "acme-staging" }),
    ]);

    render(<Home />);
    await waitFor(() => expect(screen.getByText("acme")).toBeInTheDocument());

    replaceMock.mockClear();
    await userEvent.type(
      screen.getByPlaceholderText(/filter by app, project/i),
      "widget",
    );

    expect(replaceMock).not.toHaveBeenCalled();
  });

  it("shows an error banner when the units fetch fails", async () => {
    vi.spyOn(api, "listUnits").mockRejectedValue(new Error("backend unreachable"));

    render(<Home />);

    await waitFor(() =>
      expect(screen.getByText(/failed to load sync units/i)).toBeInTheDocument(),
    );
    expect(screen.getByText("backend unreachable")).toBeInTheDocument();
  });

  it("shows stat tile counts, including the 'other' bucket for units matching no tile", async () => {
    vi.spyOn(api, "listUnits").mockResolvedValue([
      unit({ app: "a", status: "Synced", health: "Healthy" }),
      unit({ app: "b", status: "OutOfSync", health: "Missing" }),
      unit({ app: "c", status: "Invalid", health: "Invalid" }),
    ]);

    render(<Home />);

    await waitFor(() => expect(screen.getByText("Total")).toBeInTheDocument());
    expect(screen.getByText("3")).toBeInTheDocument(); // Total
    expect(screen.getByText("Synced")).toBeInTheDocument();
    expect(screen.getByText("Out of sync")).toBeInTheDocument();
    // "c" (Invalid/Invalid) matches none of the STAT_TILES, so it's
    // accounted for separately rather than silently missing from Total.
    expect(
      screen.getByText(/other \(pending\/missing\/invalid\)/i),
    ).toBeInTheDocument();
  });

  it("displays the most recently fetched units when a stale request resolves after a newer refresh", async () => {
    let resolveFirst!: (units: Unit[]) => void;
    const first = new Promise<Unit[]>((res) => {
      resolveFirst = res;
    });
    vi.spyOn(api, "listUnits")
      .mockImplementationOnce(() => first)
      .mockImplementationOnce(() =>
        Promise.resolve([unit({ app: "fresh-app", project: "fresh-project" })]),
      );

    render(<Home />);

    await userEvent.click(screen.getByRole("button", { name: /refresh/i }));

    await waitFor(() =>
      expect(screen.getByText("fresh-project")).toBeInTheDocument(),
    );

    // The stale first response arrives late — it must not clobber the
    // newer data already on screen.
    resolveFirst([unit({ app: "stale-app", project: "stale-project" })]);
    await Promise.resolve();
    await Promise.resolve();

    expect(screen.getByText("fresh-project")).toBeInTheDocument();
    expect(screen.queryByText("stale-project")).not.toBeInTheDocument();
  });
});
