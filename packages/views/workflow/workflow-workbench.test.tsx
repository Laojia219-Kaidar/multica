import { describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { WorkflowWorkbench } from "./workflow-workbench";
import type { WorkflowDefinition, WorkflowInstance } from "@multica/core/api/workflow";
import type { WorkflowReceiptView, WorkflowRuntime } from "@multica/core/workflow";

const def: WorkflowDefinition = {
  id: "hivecrew.project-lifecycle",
  version: 1,
  risk: "standard",
  stages: [{ name: "operate", sla_seconds: 604800 }, { name: "review_repair" }],
};

const inst: WorkflowInstance = {
  id: "plc-1",
  definition_id: "hivecrew.project-lifecycle",
  definition_version: 1,
  context: { project_id: "PRJ-1" },
  stage_index: 1,
  status: "running",
};

const runtime: WorkflowRuntime = {
  instanceId: inst.id,
  definitionId: inst.definition_id,
  version: inst.definition_version,
  status: "running",
  nodes: [{ nodeId: "approval", status: "waiting_approval", taskId: "task-7", runId: "run-7" }],
};

const receipt: WorkflowReceiptView = {
  id: "event-7",
  instanceId: inst.id,
  kind: "event",
  status: "observed",
  label: "阶段进入审批",
  sourceRef: "workflow:event:7",
  actor: "system",
  occurredAt: "2026-08-14T12:00:00Z",
  idempotencyKey: "idem-7",
};

describe("WorkflowWorkbench", () => {
  it("renders overview, instance, and template", () => {
    render(<WorkflowWorkbench instances={[inst]} definitions={[def]} />);
    expect(screen.getByTestId("workflow-workbench")).toBeDefined();
    expect(screen.getByText("实例 1")).toBeDefined();
    expect(screen.getByText(/工作流实例/)).toBeDefined();
    expect(screen.getByText(/流程模板/)).toBeDefined();
    expect(screen.getByText("operate → review_repair")).toBeDefined();
    expect(screen.getByText("阶段：review_repair · 标准")).toBeDefined();
  });

  it("shows empty states", () => {
    render(<WorkflowWorkbench instances={[]} definitions={[]} />);
    expect(screen.getByText("暂无工作流实例")).toBeDefined();
    expect(screen.getByText("暂无流程模板")).toBeDefined();
  });

  it("shows stage progress, approval state, and read-only execution receipts", () => {
    render(<WorkflowWorkbench instances={[inst]} definitions={[def]} runtimes={[runtime]} receipts={[receipt]} />);
    expect(screen.getByTestId("workflow-stage-progress-plc-1")).toHaveTextContent("当前阶段");
    expect(screen.getByTestId("workflow-stage-progress-plc-1")).toHaveTextContent("等待审批");
    expect(screen.getByTestId("workflow-receipt-event-7")).toHaveTextContent("workflow:event:7");
    expect(screen.getByTestId("workflow-receipt-event-7")).toHaveTextContent("只读回执");
  });

  it("does not infer historical stage completion without a server node receipt", () => {
    render(<WorkflowWorkbench instances={[inst]} definitions={[def]} />);
    expect(screen.getByTestId("workflow-stage-progress-plc-1")).toHaveTextContent("阶段状态未回读");
    expect(screen.getByTestId("workflow-stage-progress-plc-1")).not.toHaveTextContent("已完成");
  });

  it("keeps loading and error states distinct from an empty response", () => {
    render(
      <WorkflowWorkbench
        instances={[]}
        definitions={[]}
        instancesState="loading"
        definitionsState="error"
        definitionsError="定义接口不可用"
      />,
    );
    expect(screen.getByText("正在加载工作流实例…")).toBeDefined();
    expect(screen.getByText("工作流模板加载失败：定义接口不可用")).toBeDefined();
    expect(screen.queryByText("暂无工作流实例")).toBeNull();
    expect(screen.queryByText("暂无流程模板")).toBeNull();
  });

  it("only exposes a create request when the integration callback is supplied", () => {
    const onCreateInstance = vi.fn();
    render(<WorkflowWorkbench instances={[]} definitions={[def]} onCreateInstance={onCreateInstance} />);
    fireEvent.click(screen.getByRole("button", { name: "请求创建实例" }));
    expect(onCreateInstance).toHaveBeenCalledWith(def);

    cleanup();
    render(<WorkflowWorkbench instances={[]} definitions={[def]} />);
    expect(screen.getByText("后端创建实例接口尚未接入")).toBeDefined();
    expect(screen.queryByRole("button", { name: "请求创建实例" })).toBeNull();
  });
});
