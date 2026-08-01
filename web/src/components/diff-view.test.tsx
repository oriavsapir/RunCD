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
  observing: false,
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
    expect(screen.getByText(/image digest matches/i)).toBeInTheDocument();
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
    expect(screen.queryByText(/image digest matches/i)).not.toBeInTheDocument();
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

  // Regression test: an app that excludes traffic from management can be
  // OutOfSync purely from a traffic mismatch while its image digests still
  // match — without this note, the "image digest matches" caption right
  // under a red OutOfSync badge would look contradictory.
  it("explains an excluded field when the unit has ignoreFields set", () => {
    const unit: Unit = {
      ...baseUnit,
      status: "OutOfSync",
      desiredImage: "sha256:aaaa",
      liveImage: "sha256:aaaa",
      ignoreFields: ["traffic"],
    };
    render(<DiffView unit={unit} />);
    expect(screen.getByText(/doesn't manage: traffic/i)).toBeInTheDocument();
  });

  it("explains an excluded precondition when the unit has ignorePreconditions set", () => {
    const unit: Unit = {
      ...baseUnit,
      ignorePreconditions: ["pubsubTopic:orders-events"],
    };
    render(<DiffView unit={unit} />);
    expect(
      screen.getByText(/skips these preconditions.*pubsubTopic:orders-events/i),
    ).toBeInTheDocument();
  });

  it("shows no exclusions note for a unit with none configured", () => {
    render(<DiffView unit={baseUnit} />);
    expect(screen.queryByText(/doesn't manage/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/skips these preconditions/i)).not.toBeInTheDocument();
  });
});
