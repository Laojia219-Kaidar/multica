import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WorkWall } from "./work-wall";
import type {
  A2Pane,
  EmployeeLiveActivityV1,
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
    pane_id: "pane-1",
    employee_id: "emp-1",
    session_id: "sess-1",
    kind: "terminal",
    display_name: "Emory",
    presence_state: "working",
    work_stage: "coding",
    tail_text: "npm test\nPASS",
    observed_at: "2026-08-24T12:00:00Z",
    freshness_state: "fresh",
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
      />,
    );
    expect(screen.getByText("Emory")).toBeDefined();
    expect(screen.getByText("Coco")).toBeDefined();
  });

  it("shows the terminal tail when a pane is joined by employee_id", () => {
    render(<WorkWall employees={[emp()]} panes={[pane()]} />);
    const tails = screen.getAllByTestId("pane-tail");
    expect(tails.length).toBe(1);
    expect(tails[0]?.textContent).toContain("PASS");
  });

  it("shows the session_id as the terminal session name", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[pane({ session_id: "pixel-main:0.1" })]}
      />,
    );
    const sessionId = screen.getByTestId("pane-session-id");
    expect(sessionId.textContent).toContain("pixel-main:0.1");
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
      />,
    );
    const tails = screen.getAllByTestId("work-site-card");
    expect(tails.length).toBe(2);
    // Only emp-1 should have a real pane with session_id
    const sessionEls = screen.getAllByTestId("pane-session-id");
    expect(sessionEls.length).toBe(1);
  });

  it("terminal and event_console are mutually exclusive per employee", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[
          pane({ pane_id: "ev", kind: "event_console", session_id: "ev-sess" }),
          pane({ pane_id: "tm", kind: "terminal", session_id: "tm-sess" }),
        ]}
      />,
    );
    // Only one pane renders (terminal wins)
    const sessionEls = screen.getAllByTestId("pane-session-id");
    expect(sessionEls.length).toBe(1);
    expect(sessionEls[0]?.textContent).toContain("tm-sess");
  });

  it("unmatched panes are counted but never rendered as employees", () => {
    render(
      <WorkWall
        employees={[emp()]}
        panes={[
          pane({ employee_id: "emp-orphan", pane_id: "orphan-1" }),
          pane({ employee_id: "emp-orphan2", pane_id: "orphan-2" }),
        ]}
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
    render(<WorkWall employees={employees} panes={[]} />);
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
    render(<WorkWall employees={employees} panes={[]} />);

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
    render(<WorkWall employees={[emp()]} panes={[]} />);
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
    render(<WorkWall employees={[emp()]} panes={[pane()]} />);
    const wall = screen.getByTestId("work-wall");
    // No hardcoded terminal green
    expect(wall.className).not.toMatch(/bg-green-/);
    expect(wall.className).not.toMatch(/bg-zinc-/);
    expect(wall.className).not.toMatch(/bg-black/);
    expect(wall.className).not.toMatch(/text-green-/);
    expect(wall.className).not.toMatch(/text-zinc-/);
  });
});
