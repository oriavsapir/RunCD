import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UnitTable, UnitTree } from "./unit-table";
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

describe("UnitTable", () => {
  it("shows an empty-state message when there are no units", () => {
    render(<UnitTable units={[]} />);
    expect(screen.getByText(/no sync units match/i)).toBeInTheDocument();
  });

  it("groups units by environment, per §5.11", () => {
    render(
      <UnitTable
        units={[
          unit({ app: "widget-api", project: "example-prod-eu", env: "prd" }),
          unit({ app: "notification-service", project: "example-dev-01", env: "dev" }),
        ]}
      />,
    );
    expect(screen.getByText("prd")).toBeInTheDocument();
    expect(screen.getByText("dev")).toBeInTheDocument();
    expect(screen.getByText("widget-api")).toBeInTheDocument();
    expect(screen.getByText("notification-service")).toBeInTheDocument();
  });

  it("places each app's row under its own environment's table, not a shared one", () => {
    render(
      <UnitTable
        units={[
          unit({ app: "widget-api", project: "example-prod-eu", env: "prd" }),
          unit({ app: "notification-service", project: "example-dev-01", env: "dev" }),
        ]}
      />,
    );
    const prdSection = screen.getByText("prd").closest("section")!;
    const devSection = screen.getByText("dev").closest("section")!;
    expect(
      prdSection.querySelector("tbody")?.textContent,
    ).toContain("widget-api");
    expect(
      prdSection.querySelector("tbody")?.textContent,
    ).not.toContain("notification-service");
    expect(
      devSection.querySelector("tbody")?.textContent,
    ).toContain("notification-service");
  });

  it("sorts rows within an environment by project\\x00app, not project alone", () => {
    render(
      <UnitTable
        units={[
          unit({ app: "z-app", project: "a-project", env: "prd" }),
          unit({ app: "a-app", project: "a-project", env: "prd" }),
        ]}
      />,
    );
    const rows = screen.getAllByRole("row").slice(1); // drop header row
    const appOrder = rows.map((r) => r.textContent);
    expect(appOrder[0]).toContain("a-app");
    expect(appOrder[1]).toContain("z-app");
  });
});

describe("UnitTree", () => {
  it("keeps a manually-collapsed environment collapsed across a re-render (e.g. a data refresh)", async () => {
    const units = [
      unit({ app: "widget-api", project: "example-prod-eu", env: "prd" }),
    ];
    const { rerender } = render(<UnitTree units={units} />);

    expect(screen.getByText("widget-api")).toBeVisible();

    await userEvent.click(screen.getByText("prd"));
    expect(screen.getByText("widget-api")).not.toBeVisible();

    // A bare `open` attribute would be reapplied by React here, silently
    // re-expanding the node — this simulates the periodic poll re-rendering
    // with a new (but equal) units array.
    rerender(<UnitTree units={[...units]} />);
    expect(screen.getByText("widget-api")).not.toBeVisible();
  });
});
