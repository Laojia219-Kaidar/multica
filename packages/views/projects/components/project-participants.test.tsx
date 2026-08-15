import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ProjectParticipants } from "./project-participants";
import type {
  ProjectParticipantsData,
  WorkParticipant,
} from "@multica/core/types";

function makeParticipant(overrides: Partial<WorkParticipant> = {}): WorkParticipant {
  return {
    actor_type: "registered_employee",
    actor_id: "DE-ALICE",
    employee_id: "DE-ALICE",
    runtime_id: "runtime-uuid",
    model_ref: "qwen3.6-27b",
    host_id: "Mac mini M5X",
    ...overrides,
  };
}

describe("ProjectParticipants", () => {
  it("renders the empty state when no data is provided", () => {
    render(<ProjectParticipants />);
    expect(screen.getByText("暂无参与者数据。")).toBeInTheDocument();
  });

  it("renders actor type, employee id, and available runtime dimensions", () => {
    const data: ProjectParticipantsData = {
      source: "workforce_base_runtime",
      pending_project_scope: true,
      participants: [makeParticipant()],
    };
    render(<ProjectParticipants data={data} />);

    expect(screen.getByText("注册员工")).toBeInTheDocument();
    expect(screen.getByTitle("员工编号")).toHaveTextContent("DE-ALICE");
    expect(screen.getByText("runtime-uuid")).toBeInTheDocument();
    expect(screen.getByText("qwen3.6-27b")).toBeInTheDocument();
    expect(screen.getByText("Mac mini M5X")).toBeInTheDocument();
  });

  it("marks the pending backend aggregation notice", () => {
    render(
      <ProjectParticipants
        data={{
          source: "workforce_base_runtime",
          pending_project_scope: true,
          participants: [makeParticipant()],
        }}
      />,
    );
    expect(screen.getByText(/待后端聚合端点部署后接通/)).toBeInTheDocument();
  });

  it("never renders an employee id for external agents", () => {
    render(
      <ProjectParticipants
        data={{
          source: "work_entry_participants",
          pending_project_scope: false,
          participants: [
            makeParticipant({
              actor_type: "external_agent",
              actor_id: "EXT-42",
              employee_id: undefined,
            }),
          ],
        }}
      />,
    );
    expect(screen.getByText("外部智能体")).toBeInTheDocument();
    expect(screen.getByText("EXT-42")).toBeInTheDocument();
    expect(screen.queryByText(/^DE-/)).toBeNull();
  });

  it("renders carrier/host/session/base/model from the project-scoped read model", () => {
    render(
      <ProjectParticipants
        data={{
          source: "work_entry_participants",
          project_id: "proj-1",
          pending_project_scope: false,
          participants: [
            makeParticipant({
              actor_type: "external_agent",
              actor_id: "EXT-dgx-003",
              employee_id: undefined,
              carrier_id: "hermes",
              runtime_id: "dgx-hermes",
              model_ref: "local-27b",
              base_id: "底座基地",
              host_id: "spark-b398",
              session_id: "dgx-s3",
            }),
          ],
        }}
      />,
    );
    expect(screen.getByText("hermes")).toBeInTheDocument();
    expect(screen.getByText("dgx-hermes")).toBeInTheDocument();
    expect(screen.getByText("local-27b")).toBeInTheDocument();
    expect(screen.getByText("底座基地")).toBeInTheDocument();
    expect(screen.getByText("spark-b398")).toBeInTheDocument();
    expect(screen.getByText("dgx-s3")).toBeInTheDocument();
    // The project-scoped source must not show the stale "待后端" notice.
    expect(screen.queryByText(/待后端聚合端点部署后接通/)).toBeNull();
    expect(screen.getByText(/项目级参与者聚合/)).toBeInTheDocument();
  });
});
