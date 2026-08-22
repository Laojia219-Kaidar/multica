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

describe("WorkWall execution chain", () => {
  it("renders the full execution chain in the expanded card", () => {
    render(
      <WorkWall
        employees={[
          emp({
            issue_id: "11111111-1111-1111-1111-111111111111",
            issue_identifier: "HIV-797",
            issue_title: "[DEV] Work Wall complete execution-chain projection",
            project_id: "22222222-2222-2222-2222-222222222222",
            project_title: "HIVECREW 自我开发项目",
            task_id: "33333333-3333-3333-3333-333333333333",
            run_id: "44444444-4444-4444-4444-444444444444",
            runtime_profile_id: "55555555-5555-5555-5555-555555555555",
            runtime_profile_name: "glm-5.3 运行档案",
            execution_receipt_ref: "receipt://33333333-3333-3333-3333-333333333333",
            execution_receipt_status: "completed",
          }),
        ]}
      />,
    );
    expect(screen.queryByTestId("terminal-card-chain")).toBeNull();
    fireEvent.click(screen.getByText("Emory"));
    const chain = screen.getByTestId("terminal-card-chain");
    expect(chain.textContent).toContain("HIV-797");
    expect(chain.textContent).toContain("[DEV] Work Wall complete execution-chain projection");
    expect(chain.textContent).toContain("11111111-1111-1111-1111-111111111111");
    expect(chain.textContent).toContain("HIVECREW 自我开发项目");
    expect(chain.textContent).toContain("33333333-3333-3333-3333-333333333333");
    expect(chain.textContent).toContain("44444444-4444-4444-4444-444444444444");
    expect(chain.textContent).toContain("glm-5.3 运行档案");
    expect(chain.textContent).toContain("receipt://33333333-3333-3333-3333-333333333333");
    expect(chain.textContent).toContain("已完成");
  });

  it("labels a direct task without fabricating a Run reference", () => {
    render(
      <WorkWall
        employees={[
          emp({
            issue_id: "11111111-1111-1111-1111-111111111111",
            issue_identifier: "HIV-797",
            issue_title: "[DEV] Work Wall complete execution-chain projection",
            task_id: "33333333-3333-3333-3333-333333333333",
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByText("Emory"));
    const chain = screen.getByTestId("terminal-card-chain");
    expect(chain.textContent).toContain("无独立 Run ID");
    expect(chain.textContent).not.toContain("44444444");
    // No receipt evidence: nothing rendered, no invented status.
    expect(chain.textContent).not.toContain("Receipt");
  });

  it("renders no chain block when no evidence exists", () => {
    render(<WorkWall employees={[emp()]} />);
    fireEvent.click(screen.getByText("Emory"));
    expect(screen.queryByTestId("terminal-card-chain")).toBeNull();
  });

  it("shows the issue identifier on the collapsed card", () => {
    render(
      <WorkWall
        employees={[
          emp({
            issue_identifier: "HIV-797",
            issue_title: "[DEV] Work Wall complete execution-chain projection",
          }),
        ]}
      />,
    );
    expect(screen.getByText(/HIV-797 · /)).toBeDefined();
  });
});
