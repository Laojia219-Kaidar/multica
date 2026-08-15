import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WorkWall } from "./work-wall";
import type { EmployeeLiveActivityV1 } from "@multica/core/api/workwall";

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

describe("WorkWall", () => {
  it("renders one terminal window per employee with text (not colour-only) presence", () => {
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
      />,
    );
    expect(screen.getByText("Emory")).toBeDefined();
    expect(screen.getByText("Coco")).toBeDefined();
    expect(screen.getAllByText(/工作中/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/空闲/).length).toBeGreaterThan(0);
  });

  it("expands in place on click without a second page", () => {
    render(
      <WorkWall
        employees={[
          emp({
            recent_events: [
              {
                event_id: "ev-1",
                kind: "run.started",
                safe_summary: "run started",
                occurred_at: "2026-08-13T12:00:00Z",
              },
            ],
          }),
        ]}
      />,
    );
    expect(screen.queryByTestId("terminal-card-expanded")).toBeNull();
    fireEvent.click(screen.getByText("Emory"));
    expect(screen.getByTestId("terminal-card-expanded")).toBeDefined();
    expect(screen.getByText(/run started/)).toBeDefined();
  });

  it("filters by presence state", () => {
    render(
      <WorkWall
        employees={[
          emp({ display_name: "Emory" }),
          emp({
            agent_id: "agt-2",
            employee_id: "emp-2",
            display_name: "Coco",
            presence_state: "idle",
            work_stage: "none",
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "○ 空闲" }));
    expect(screen.queryByText("Emory")).toBeNull();
    expect(screen.getByText("Coco")).toBeDefined();
  });
});

  it("shows a status summary bar with counts", () => {
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
          emp({
            agent_id: "agt-3",
            employee_id: "emp-3",
            display_name: "Drake",
            presence_state: "blocked",
            work_stage: "unknown",
            token_usage: 42,
          }),
        ]}
      />,
    );
    expect(screen.getByTestId("work-wall-status-bar")).toBeDefined();
    expect(screen.getByText("员工 3")).toBeDefined();
    expect(screen.getByText("工作中 1")).toBeDefined();
    expect(screen.getByText("等待/阻塞 1")).toBeDefined();
    expect(screen.getByText("Token 42")).toBeDefined();
  });

  it("filters by search text", () => {
    render(
      <WorkWall
        employees={[
          emp({ display_name: "Emory", project_title: "工作流与员工记忆系统", issue_title: "事件协议" }),
          emp({
            agent_id: "agt-2",
            employee_id: "emp-2",
            display_name: "Coco",
            project_title: "成果中心",
            presence_state: "idle",
            work_stage: "none",
          }),
        ]}
      />,
    );
    fireEvent.change(screen.getByTestId("work-wall-search"), { target: { value: "emory" } });
    expect(screen.queryByText("Coco")).toBeNull();
    expect(screen.getByText("Emory")).toBeDefined();

    fireEvent.change(screen.getByTestId("work-wall-search"), { target: { value: "" } });
    expect(screen.getByText("Coco")).toBeDefined();
  });
