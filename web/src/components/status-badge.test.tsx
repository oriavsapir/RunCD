import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { StatusBadge } from "./status-badge";

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
