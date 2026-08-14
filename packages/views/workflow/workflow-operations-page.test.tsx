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
  definitionDrafts: [{ id: "content.wechat", name: "公众号内容生产", version: 1, projectId: "wechat", graph: { nodes: [{ id: "draft", type: "agent_task" as const, position: { x: 0, y: 0 }, data: { label: "初稿" } }], edges: [] } }],
};

describe("WorkflowOperationsPage", () => {
  it("navigates L4 and exposes the graph designer and inspector", () => {
    render(<WorkflowOperationsPage {...props} />);
    fireEvent.click(screen.getByTestId("workflow-project-wechat"));
    fireEvent.click(screen.getByRole("tab", { name: "工作流" }));
    expect(screen.getByTestId("workflow-designer")).toBeDefined();
    expect(screen.getByTestId("workflow-node-inspector")).toHaveTextContent("选择节点查看配置");
    fireEvent.click(screen.getByRole("button", { name: /初稿/ }));
    expect(screen.getByTestId("workflow-node-inspector")).toHaveTextContent("节点 Inspector");
  });

  it("shows the honest empty project state before selection", () => {
    render(<WorkflowOperationsPage {...props} />);
    expect(screen.getByText("请选择一个运营科目或项目")).toBeDefined();
    expect(screen.getByText(/流程动作只存在于工作流图中/)).toBeDefined();
  });
});
