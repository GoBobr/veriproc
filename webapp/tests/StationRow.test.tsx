import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/preact";
import { StationRow } from "../src/components/StationRow";
import { AuthProvider } from "../src/state/auth";
import type { DashboardStationRow, Role } from "../src/api/types";

function row(): DashboardStationRow {
  return {
    station_id: "st-a",
    station_name: "Station A",
    paused: false,
    running_count: 1,
    queued_count: 0,
    counts: { success: 4, failure: 1 },
    overflow: 2,
    slots: [
      { kind: "running", task_id: "t-1", retry_index: 0 },
      { kind: "completed", task_id: "t-2", retry_index: 0 },
      { kind: "failed", task_id: "t-3", retry_index: 1 },
      { kind: "empty", retry_index: 0 },
    ],
  };
}

function wrap(role: Role, ui: preact.ComponentChildren) {
  return (
    <AuthProvider initial={{ token: "tok", subject: "alice", role }}>{ui}</AuthProvider>
  );
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("StationRow", () => {
  it("renders one slot per entry, counters and overflow", () => {
    render(wrap("viewer", <StationRow instanceID="inst" row={row()} onChanged={() => {}} />));
    expect(screen.getAllByRole("button").filter((b) => b.classList.contains("slot"))).toHaveLength(4);
    expect(screen.getByText("+2")).toBeInTheDocument();
    expect(screen.getByText("✓ 4")).toBeInTheDocument();
    expect(screen.getByText("✗ 1")).toBeInTheDocument();
  });

  it("station context menu is read-only for viewers", () => {
    render(wrap("viewer", <StationRow instanceID="inst" row={row()} onChanged={() => {}} />));
    const label = screen.getByText("Station A");
    fireEvent.contextMenu(label);
    expect(screen.getByText(/Read-only/i)).toBeInTheDocument();
    expect(screen.queryByText(/Pause station/)).not.toBeInTheDocument();
  });

  it("operators see pause and submit actions", () => {
    render(wrap("operator", <StationRow instanceID="inst" row={row()} onChanged={() => {}} />));
    fireEvent.contextMenu(screen.getByText("Station A"));
    expect(screen.getByText("Pause station")).toBeInTheDocument();
    expect(screen.getByText("Submit task…")).toBeInTheDocument();
  });

  it("operator slot menu offers retry+hide on failed runs", () => {
    render(wrap("operator", <StationRow instanceID="inst" row={row()} onChanged={() => {}} />));
    const slots = document.querySelectorAll(".slot");
    fireEvent.contextMenu(slots[2]); // failed
    expect(screen.getByText("Retry task")).toBeInTheDocument();
    expect(screen.getByText("Hide from view")).toBeInTheDocument();
  });
});
