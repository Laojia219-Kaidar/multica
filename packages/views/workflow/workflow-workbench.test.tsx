import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { WorkflowWorkbench } from "./workflow-workbench";
import type { WorkflowDefinition, WorkflowInstance } from "@multica/core/api/workflow";

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
});
