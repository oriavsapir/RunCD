import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { HistoryTable } from "./history-table";
import type { SyncEvent } from "@/lib/types";

const baseEvent: SyncEvent = {
  id: 1,
  trigger: "manual",
  actor: "alice@company.com",
  fromImage: "sha256:aaaaaaaaaaaaaaaaaaaaaaaa",
  toImage: "sha256:bbbbbbbbbbbbbbbbbbbbbbbb",
  startedAt: "2026-07-30T10:00:00Z",
  finishedAt: "2026-07-30T10:00:05Z",
  result: "succeeded",
};

describe("HistoryTable", () => {
  it("shows an empty-state message when there are no events", () => {
    render(<HistoryTable events={[]} />);
    expect(screen.getByText(/no sync attempts recorded/i)).toBeInTheDocument();
  });

  it("renders trigger, actor, and result for each event", () => {
    render(<HistoryTable events={[baseEvent]} />);
    expect(screen.getByText("manual")).toBeInTheDocument();
    expect(screen.getByText("alice@company.com")).toBeInTheDocument();
    expect(screen.getByText("succeeded")).toBeInTheDocument();
  });

  it("shows the error message for a failed sync", () => {
    const failed: SyncEvent = {
      ...baseEvent,
      id: 2,
      result: "failed",
      error: "precondition failed: pubsubTopic does not exist",
    };
    render(<HistoryTable events={[failed]} />);
    expect(screen.getByText("failed")).toBeInTheDocument();
    expect(
      screen.getByText(/precondition failed: pubsubTopic/i),
    ).toBeInTheDocument();
  });

  it("renders an em-dash for a missing actor (auto-triggered sync)", () => {
    const auto: SyncEvent = { ...baseEvent, trigger: "auto", actor: undefined };
    render(<HistoryTable events={[auto]} />);
    expect(screen.getByText("—")).toBeInTheDocument();
  });

  it("shows the deploy duration for a finished event", () => {
    render(<HistoryTable events={[baseEvent]} />);
    expect(screen.getByText("5s")).toBeInTheDocument();
  });

  it("shows elapsed time (not a fixed duration) for an in_progress event", () => {
    const inProgress: SyncEvent = {
      ...baseEvent,
      id: 3,
      result: "in_progress",
      finishedAt: undefined,
    };
    render(<HistoryTable events={[inProgress]} />);
    expect(screen.getByText(/so far/i)).toBeInTheDocument();
  });
});
