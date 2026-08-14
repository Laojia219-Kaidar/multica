import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WorkflowDesigner } from "./workflow-designer";

vi.mock("@xyflow/react", () => ({
  Background: () => <div />,
  Controls: () => <div />,
  ReactFlow: ({ nodes, onNodeClick, edges, onEdgeClick }: { nodes: Array<{ id: string; data: { label: string } }>; edges: Array<{ id: string; source: string; target: string }>; onNodeClick?: (_event: unknown, node: { id: string }) => void; onEdgeClick?: (_event: unknown, edge: { id: string; source: string; target: string }) => void }) => <div>{nodes.map((node) => <button type="button" key={node.id} onClick={(event) => onNodeClick?.(event, node)}>{node.data.label}</button>)}{edges.map((edge) => <button type="button" key={edge.id} onClick={(event) => onEdgeClick?.(event, edge)}>edge:{edge.id}</button>)}</div>,
  applyEdgeChanges: (_changes: unknown[], edges: unknown[]) => edges,
  applyNodeChanges: (_changes: unknown[], nodes: unknown[]) => nodes,
}));

const definition = {
  id: "content.wechat",
  name: "公众号内容生产",
  version: 1,
  projectId: "wechat",
  graph: {
    nodes: [
      { id: "draft", type: "agent_task" as const, position: { x: 0, y: 0 }, data: { label: "初稿", binding: { mode: "role_pool" as const, role: "内容生产" } } },
      { id: "review", type: "approval" as const, position: { x: 120, y: 0 }, data: { label: "审核" } },
    ],
    edges: [{ id: "draft-review", source: "draft", target: "review", condition: "完成初稿" }],
  },
};

describe("WorkflowDesigner", () => {
  it("preserves node kinds, bindings and edge conditions in the emitted domain graph", () => {
    const onChange = vi.fn();
    render(<WorkflowDesigner definition={definition} onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: /^Agent 任务$/ }));
    const emitted = onChange.mock.calls.at(-1)?.[0];
    expect(emitted.graph.nodes.at(-1).type).toBe("agent_task");
    expect(emitted.graph.nodes.at(-1).data.binding).toEqual({ mode: "role_pool", role: "内容生产" });
    fireEvent.click(screen.getByRole("button", { name: "Agent 任务: 初稿" }));
    fireEvent.change(screen.getByLabelText("执行绑定"), { target: { value: "fixed_employee" } });
    fireEvent.change(screen.getByLabelText("数字员工 ID"), { target: { value: "employee-content-001" } });
    expect(onChange.mock.calls.at(-1)?.[0].graph.nodes.find((node: { id: string }) => node.id === "draft").data.binding).toEqual({ mode: "fixed_employee", employeeId: "employee-content-001" });
    fireEvent.click(screen.getByRole("button", { name: "edge:draft-review" }));
    fireEvent.change(screen.getByLabelText("分支条件"), { target: { value: "审核通过" } });
    expect(onChange.mock.calls.at(-1)?.[0].graph.edges[0].condition).toBe("审核通过");
  });
});
