import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { UnitTable } from "./unit-table";
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
    ...overrides,
  };
}

describe("UnitTable", () => {
  it("shows an empty-state message when there are no units", () => {
    render(<UnitTable units={[]} />);
    expect(screen.getByText(/no sync units configured/i)).toBeInTheDocument();
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
});
