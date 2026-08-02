import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { HealthBadge, StatusBadge } from "./status-badge";

describe("StatusBadge", () => {
  it("renders the raw status text", () => {
    render(<StatusBadge value="Synced" />);
    expect(screen.getByText("Synced")).toBeInTheDocument();
  });

  it.each(["Synced", "Healthy", "OutOfSync", "Progressing", "Degraded", "Invalid", "Missing", "Pending"])(
    "renders known status %s without falling back to the default icon styling",
    (value) => {
      render(<StatusBadge value={value} />);
      expect(screen.getByText(value)).toBeInTheDocument();
    },
  );

  it("falls back gracefully for an unrecognized value instead of crashing", () => {
    render(<StatusBadge value="SomethingNew" />);
    expect(screen.getByText("SomethingNew")).toBeInTheDocument();
  });
});

describe("HealthBadge", () => {
  it("shows 'Job' instead of the computed health for a job unit — a job runs to completion and stops, so Progressing/Missing there means something different than for a service", () => {
    render(<HealthBadge health="Progressing" resourceType="job" />);
    expect(screen.getByText("Job")).toBeInTheDocument();
    expect(screen.queryByText("Progressing")).not.toBeInTheDocument();
  });

  it.each(["service", "workerPool", undefined])(
    "shows the real health status for a non-job resourceType (%s)",
    (resourceType) => {
      render(<HealthBadge health="Healthy" resourceType={resourceType} />);
      expect(screen.getByText("Healthy")).toBeInTheDocument();
      expect(screen.queryByText("Job")).not.toBeInTheDocument();
    },
  );
});
