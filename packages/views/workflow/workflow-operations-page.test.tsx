import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WorkflowOperationsPage } from "./workflow-operations-page";

vi.mock("@xyflow/react", () => ({
  Background: () => <div data-testid="flow-background" />,
  Controls: () => <div data-testid="flow-controls" />,
  ReactFlow: ({ nodes, onNodeClick }: { nodes: Array<{ id: string; data: { label: string } }>; onNodeClick?: (_event: unknown, node: { id: string }) => void }) => <div data-testid="react-flow">{nodes.map((node) => <button type="button" key={node.id} onClick={(event) => onNodeClick?.(event, node)}>{node.data.label}</button>)}</div>,
  applyEdgeChanges: (_changes: unknown[], edges: unknown[]) => edges,
  applyNodeChanges: (_changes: unknown[], nodes: unknown[]) => nodes,
}));

const props = {
  programs: [{ id: "brand", name: "蜂巢创科品牌运营", projectIds: ["wechat"] }],
  projects: [{ id: "wechat", programId: "brand", formalProjectId: "PRJ-WECHAT", name: "微信公众号运营", platform: "公众号" }],
  definitionDrafts: [{ id: "content.wechat", name: "公众号内容生产", version: 1, projectId: "wechat", graph: { nodes: [{ id: "draft", type: "agent_task" as const, position: { x: 0, y: 0 }, data: { label: "初稿", binding: { mode: "role_pool" as const, role: "内容生产" } } }], edges: [] } }],
};

describe("WorkflowOperationsPage", () => {
  it("navigates L4 and exposes the graph designer and inspector", () => {
    render(<WorkflowOperationsPage {...props} />);
    fireEvent.click(screen.getByTestId("workflow-project-wechat"));
    fireEvent.click(screen.getByRole("tab", { name: "工作流" }));
    expect(screen.getByTestId("workflow-designer")).toBeDefined();
    expect(screen.getByTestId("workflow-node-inspector")).toHaveTextContent("选择节点或连线查看配置");
    fireEvent.click(screen.getByRole("button", { name: /初稿/ }));
    expect(screen.getByTestId("workflow-node-inspector")).toHaveTextContent("节点 Inspector");
  });

  it("shows the honest empty project state before selection", () => {
    render(<WorkflowOperationsPage {...props} />);
    expect(screen.getByText("请选择一个运营科目或项目")).toBeDefined();
    expect(screen.getByText(/流程动作只存在于工作流图中/)).toBeDefined();
  });

  it("lets one L4 project select multiple independent workflow definitions", () => {
    const onSelectDefinition = vi.fn();
    render(<WorkflowOperationsPage {...props} selectedDefinitionId="content.weibo" onSelectDefinition={onSelectDefinition} definitionDrafts={[
      ...props.definitionDrafts,
      { id: "content.weibo", name: "微博短内容生产", version: 2, projectId: "wechat", graph: { nodes: [{ id: "outline", type: "human_task", position: { x: 0, y: 0 }, data: { label: "选题" } }], edges: [] } },
    ]} />);
    fireEvent.click(screen.getByTestId("workflow-project-wechat"));
    fireEvent.click(screen.getByRole("tab", { name: "工作流" }));
    expect(screen.getByLabelText("选择项目工作流")).toHaveValue("content.weibo");
    fireEvent.change(screen.getByLabelText("选择项目工作流"), { target: { value: "content.wechat" } });
    expect(onSelectDefinition).toHaveBeenCalledWith("content.wechat");
  });

  it("reads project outcomes as the formal artifact source instead of inventing local results", () => {
    render(<WorkflowOperationsPage {...props} selection={{ kind: "project", id: "wechat" }} section="artifacts" outcomes={[{
      id: "a4d7525e-98ba-4aa2-8dc4-bf49c6bf5ed9",
      issue: null,
      work_order: { source_ref: "work-order:wechat", revision: "r1", digest: "sha256:work" },
      employee: { source_ref: "employee:writer", id: "writer" },
      identity_binding: { source_ref: "binding:writer", id: "writer-binding" },
      execution_target: { local_agent_id: "writer", agent_ref: "agent:writer", agent_revision: "r1", agent_digest: "sha256:agent" },
      current_agent_display: { name: "撰稿数字员工", status: "idle" },
      initial_task_id: "c4f9fbe4-c0fd-472b-b595-f2ea304b20b6",
      current_task_id: "c4f9fbe4-c0fd-472b-b595-f2ea304b20b6",
      execution_state: "completed",
      active_artifact: { id: "b7222a44-d93d-4d57-9f4c-fbf6ca594c74", revision: 1, durable_object_ref: "nas://candidate/draft.md", digest: "sha256:artifact", status: "submitted", formal_visible: false },
      version_count: 1,
    }]} outcomeHref={(id) => `/acme/outcomes?outcome=${id}`} />);
    expect(screen.getByText(/读取既有正式 Outcome Center/)).toBeDefined();
    expect(screen.getByText(/撰稿数字员工/)).toBeDefined();
    expect(screen.getByRole("link", { name: "在成果中心查看、审核与晋级" })).toHaveAttribute("href", "/acme/outcomes?outcome=a4d7525e-98ba-4aa2-8dc4-bf49c6bf5ed9");
  });
});
