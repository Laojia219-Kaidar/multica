import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WorkWall } from "./work-wall";
import type {
  A2Pane,
  EmployeeLiveActivityV1,
  TerminalPane,
} from "@multica/core/api/workwall";

function emp(over: Partial<EmployeeLiveActivityV1> = {}): EmployeeLiveActivityV1 {
  return {
    schema_version: "hivecrew.employee-live-activity.v1",
    workspace_id: "ws-1",
    employee_id: "emp-1",
    agent_id: "agt-1",
    display_name: "Emory",
    presence_state: "working",
    work_stage: "coding",
    recent_events: [],
    source_refs: ["agent://agt-1"],
    observed_at: "2026-08-13T12:00:00Z",
    freshness_state: "fresh",
    ...over,
  };
}

function pane(over: Partial<A2Pane> = {}): A2Pane {
  return {
    schema_version: "hivecrew.workwall.a2-pane.v1",
    workspace_id: "ws-1",
    work_ref: "wr-1",
    source_event_id: "evt-1",
    employee_id: "emp-1",
    session_id: "pixel:0.1",
    execution_state: "active",
    working: true,
    surface_kind: "terminal",
    freshness_state: "fresh",
    observed_at: "2026-08-24T12:00:00Z",
    activity_summary: "执行中",
    source_refs: ["work_event://evt-1"],
    ...over,
  };
}

function terminalPane(over: Partial<TerminalPane> = {}): TerminalPane {
  return {
    host: "pixel-main",
    session_name: "pixel:0.1",
    window_index: 0,
    pane_index: 1,
    current_command: "npm test",
    agent_hint: "Pixel",
    tail_text: "npm test\nPASS",
    heartbeat_at: "2026-08-24T12:00:00Z",
    ...over,
  };
}

describe("WorkWall (A2 4×2)", () => {
  it("renders one card per employee from the roster (primary)", () => {
    render(
      <WorkWall
        employees={[
          emp(),
          emp({
            agent_id: "agt-2",
            employee_id: "emp-2",
            display_name: "Coco",
            presence_state: "idle",
            work_stage: "none",
          }),
        ]}
        panes={[]}
        terminalPresence={[]}
      />,
    );
    expect(screen.getByText("Emory")).toBeDefined();
    expect(screen.getByText("Coco")).toBeDefined();
  });

  it("shows terminal tail ONLY when surface_kind=terminal + session_id matches TerminalPane.session_name", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[pane()]}
        terminalPresence={[terminalPane()]}
      />,
    );
    const tails = screen.getAllByTestId("pane-tail");
    expect(tails.length).toBe(1);
    expect(tails[0]?.textContent).toContain("PASS");
    // Session label shows terminal session_name, not pane.session_id
    const sessionEl = screen.getByTestId("pane-session-id");
    expect(sessionEl.textContent).toBe("pixel:0.1");
  });

  it("does NOT show terminal when surface_kind=event_console (mutually exclusive)", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[pane({ surface_kind: "event_console", session_id: undefined })]}
        terminalPresence={[terminalPane()]}
      />,
    );
    // No terminal tail
    expect(screen.queryByTestId("pane-tail")).toBeNull();
    // Event console shown instead
    expect(screen.getByText("事件台")).toBeDefined();
    // 执行中 appears in event console execution state; use All since card header may also have "工作中"
    const execTexts = screen.getAllByText(/执行中/);
    expect(execTexts.length).toBeGreaterThan(0);
  });

  it("does NOT show terminal when session_id has no matching TerminalPane", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[pane({ session_id: "unknown-session" })]}
        terminalPresence={[terminalPane()]}
      />,
    );
    // No terminal tail — falls back to event console
    expect(screen.queryByTestId("pane-tail")).toBeNull();
    expect(screen.getByText("事件台")).toBeDefined();
  });

  it("does NOT show terminal when pane has no session_id", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[pane({ surface_kind: "terminal", session_id: undefined })]}
        terminalPresence={[terminalPane()]}
      />,
    );
    expect(screen.queryByTestId("pane-tail")).toBeNull();
    // Falls back to event console
    expect(screen.getByText("事件台")).toBeDefined();
  });

  it("never fabricates a terminal for an employee without a pane", () => {
    render(
      <WorkWall
        employees={[
          emp(),
          emp({
            agent_id: "agt-2",
            employee_id: "emp-2",
            display_name: "Coco",
          }),
        ]}
        panes={[pane({ employee_id: "emp-1" })]}
        terminalPresence={[terminalPane()]}
      />,
    );
    const cards = screen.getAllByTestId("work-site-card");
    expect(cards.length).toBe(2);
    // Only emp-1 has a terminal tail
    const tails = screen.queryAllByTestId("pane-tail");
    expect(tails.length).toBe(1);
  });

  it("tail_text comes from TerminalPane only (never from A2 pane)", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[pane({ activity_summary: "A2 summary text" })]}
        terminalPresence={[terminalPane({ tail_text: "REAL_TERMINAL_OUTPUT" })]}
      />,
    );
    const tail = screen.getByTestId("pane-tail");
    expect(tail.textContent).toContain("REAL_TERMINAL_OUTPUT");
    expect(tail.textContent).not.toContain("A2 summary text");
  });

  it("unmatched panes are counted but never rendered as employees", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[
          pane({ employee_id: "emp-orphan", work_ref: "orphan-1", source_event_id: "ev-o1" }),
          pane({ employee_id: "emp-orphan2", work_ref: "orphan-2", source_event_id: "ev-o2" }),
        ]}
        terminalPresence={[]}
      />,
    );
    // Only 1 employee card
    expect(screen.getAllByTestId("work-site-card").length).toBe(1);
    // Unmatched count shown
    expect(screen.getByText(/2 个未匹配 pane/)).toBeDefined();
  });

  it("34 employees yield exactly 5 pages of 8 (4×2)", () => {
    const employees: EmployeeLiveActivityV1[] = Array.from(
      { length: 34 },
      (_, i) =>
        emp({
          employee_id: `emp-${i + 1}`,
          agent_id: `agt-${i + 1}`,
          display_name: `员工${i + 1}`,
        }),
    );
    render(<WorkWall employees={employees} panes={[]} terminalPresence={[]} />);
    // Page 1: 8 cards
    expect(screen.getAllByTestId("work-site-card").length).toBe(8);
    expect(screen.getByText("1 / 5")).toBeDefined();

    // Navigate to last page
    for (let i = 0; i < 5; i++) {
      fireEvent.click(screen.getByTestId("work-wall-next-page"));
    }
    // Last page: 2 cards (34 = 8*4 + 2)
    expect(screen.getAllByTestId("work-site-card").length).toBe(2);
    expect(screen.getByText("显示 33–34 共 34 人")).toBeDefined();
  });

  it("resets to page 1 when filters change", () => {
    const employees: EmployeeLiveActivityV1[] = Array.from(
      { length: 20 },
      (_, i) =>
        emp({
          employee_id: `emp-${i + 1}`,
          agent_id: `agt-${i + 1}`,
          display_name: `员工${i + 1}`,
          presence_state: i < 15 ? "working" : "idle",
        }),
    );
    render(<WorkWall employees={employees} panes={[]} terminalPresence={[]} />);

    // Go to page 2
    fireEvent.click(screen.getByTestId("work-wall-next-page"));
    expect(screen.getByText("2 / 3")).toBeDefined();

    // Apply presence filter — should reset to page 1
    fireEvent.click(screen.getByTestId("work-wall-presence-idle"));
    expect(screen.getByText("1 / 1")).toBeDefined();
    // Only idle employees (5) fit on one page
    expect(screen.getAllByTestId("work-site-card").length).toBe(5);
  });

  it("grid has 4 columns at xl breakpoint (4×2 geometry)", () => {
    render(<WorkWall employees={[emp()]} panes={[]} terminalPresence={[]} />);
    const grid = screen.getByTestId("work-wall-grid");
    const cls = grid.className;
    // xl:grid-cols-4 → the 4-column xl layout we promised
    expect(cls).toContain("xl:grid-cols-4");
  });

  it("filters by search text", () => {
    render(
      <WorkWall
        employees={[
          emp({
            display_name: "Emory",
            project_title: "工作流与员工记忆系统",
            issue_title: "事件协议",
          }),
          emp({
            agent_id: "agt-2",
            employee_id: "emp-2",
            display_name: "Coco",
            project_title: "成果中心",
            presence_state: "idle",
            work_stage: "none",
          }),
        ]}
        panes={[]}
        terminalPresence={[]}
      />,
    );
    fireEvent.change(screen.getByTestId("work-wall-search"), {
      target: { value: "emory" },
    });
    expect(screen.queryByText("Coco")).toBeNull();
    expect(screen.getByText("Emory")).toBeDefined();

    fireEvent.change(screen.getByTestId("work-wall-search"), {
      target: { value: "" },
    });
    expect(screen.getByText("Coco")).toBeDefined();
  });

  it("uses semantic surface/status tokens (no hardcoded green/zinc/black)", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[pane()]}
        terminalPresence={[terminalPane()]}
      />,
    );
    const wall = screen.getByTestId("work-wall");
    // No hardcoded terminal green
    expect(wall.className).not.toMatch(/bg-green-/);
    expect(wall.className).not.toMatch(/bg-zinc-/);
    expect(wall.className).not.toMatch(/bg-black/);
    expect(wall.className).not.toMatch(/text-green-/);
    expect(wall.className).not.toMatch(/text-zinc-/);
  });

  it("first server-ordered pane per employee wins (not newest by observed_at)", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[
          pane({ work_ref: "first-wr", source_event_id: "ev-first", surface_kind: "event_console", session_id: undefined, observed_at: "2026-08-24T10:00:00Z" }),
          pane({ work_ref: "second-wr", source_event_id: "ev-second", surface_kind: "terminal", session_id: "pixel:0.1", observed_at: "2026-08-24T14:00:00Z" }),
        ]}
        terminalPresence={[terminalPane()]}
      />,
    );
    // First pane is event_console → no terminal tail shown
    // (join keeps first match, not terminal-preference)
    expect(screen.queryByTestId("pane-tail")).toBeNull();
    expect(screen.getByText("事件台")).toBeDefined();
  });
});
