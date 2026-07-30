import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { DiffView } from "./diff-view";
import type { Unit } from "@/lib/types";

const baseUnit: Unit = {
  app: "widget-api",
  project: "example-prod-eu",
  env: "prd",
  region: "europe-west1",
  auto: true,
  status: "Synced",
  health: "Healthy",
  canSync: true,
};

describe("DiffView", () => {
  it("shows both digests and marks them as matching when in sync", () => {
    const unit: Unit = {
      ...baseUnit,
      desiredImage: "sha256:aaaa",
      liveImage: "sha256:aaaa",
    };
    render(<DiffView unit={unit} />);
    expect(screen.getAllByText("sha256:aaaa").length).toBeGreaterThan(0);
    expect(screen.getByText(/image digests match/i)).toBeInTheDocument();
  });

  it("shows a transition from live to desired when out of sync", () => {
    const unit: Unit = {
      ...baseUnit,
      status: "OutOfSync",
      desiredImage: "sha256:new",
      liveImage: "sha256:old",
    };
    render(<DiffView unit={unit} />);
    // Both digests appear twice: once in the plain desired/live fields,
    // once in the transition row — assert presence, not uniqueness.
    expect(screen.getAllByText("sha256:old").length).toBeGreaterThan(0);
    expect(screen.getAllByText("sha256:new").length).toBeGreaterThan(0);
    expect(screen.queryByText(/image digests match/i)).not.toBeInTheDocument();
  });

  it("renders 'not yet observed' for a unit that's never been reconciled", () => {
    const unit: Unit = { ...baseUnit, status: "Pending", health: "Pending" };
    render(<DiffView unit={unit} />);
    expect(screen.getAllByText(/not yet observed/i)).toHaveLength(2);
  });

  it("renders 'never' for a unit with no lastReconciledAt", () => {
    render(<DiffView unit={baseUnit} />);
    expect(screen.getByText("never")).toBeInTheDocument();
  });
});
