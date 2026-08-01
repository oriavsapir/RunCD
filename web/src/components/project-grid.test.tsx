import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ProjectGrid, summarizeByProject } from "./project-grid";
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

describe("summarizeByProject", () => {
  it("groups units by project and counts status/health", () => {
    const summaries = summarizeByProject([
      unit({ app: "a", project: "proj-a", status: "Synced" }),
      unit({ app: "b", project: "proj-a", status: "OutOfSync" }),
      unit({
        app: "c",
        project: "proj-a",
        status: "Degraded",
        health: "Degraded",
      }),
      unit({ app: "d", project: "proj-b", status: "Synced", observing: true }),
    ]);

    expect(summaries).toHaveLength(2);
    const a = summaries.find((s) => s.project === "proj-a")!;
    expect(a.total).toBe(3);
    expect(a.synced).toBe(1);
    expect(a.outOfSync).toBe(1);
    expect(a.needsAttention).toBe(1);

    const b = summaries.find((s) => s.project === "proj-b")!;
    expect(b.total).toBe(1);
    expect(b.observing).toBe(1);
  });

  it("sorts projects alphabetically", () => {
    const summaries = summarizeByProject([
      unit({ project: "zeta" }),
      unit({ project: "alpha" }),
    ]);
    expect(summaries.map((s) => s.project)).toEqual(["alpha", "zeta"]);
  });
});

describe("ProjectGrid", () => {
  it("shows an empty-state message when there are no units", () => {
    render(<ProjectGrid units={[]} onSelectProject={vi.fn()} />);
    expect(screen.getByText(/no sync units match/i)).toBeInTheDocument();
  });

  it("calls onSelectProject when a project card is clicked", async () => {
    const onSelectProject = vi.fn();
    render(
      <ProjectGrid
        units={[unit({ project: "example-prod-eu" })]}
        onSelectProject={onSelectProject}
      />,
    );
    await userEvent.click(screen.getByText("example-prod-eu"));
    expect(onSelectProject).toHaveBeenCalledWith("example-prod-eu");
  });
});
