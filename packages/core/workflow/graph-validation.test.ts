import { describe, expect, it } from "vitest";
import { validateWorkflowGraph } from "./graph-validation";

describe("validateWorkflowGraph", () => {
  it("accepts a directed acyclic graph with a start and end", () => {
    expect(validateWorkflowGraph({
      nodes: [
        { id: "start", type: "agent_task", position: { x: 0, y: 0 }, data: { label: "开始", binding: { mode: "role_pool", role: "内容生产" } } },
        { id: "end", type: "approval", position: { x: 100, y: 0 }, data: { label: "审批" } },
      ],
      edges: [{ id: "start-end", source: "start", target: "end" }],
    })).toEqual([]);
  });

  it("reports empty graphs and cycles", () => {
    expect(validateWorkflowGraph({ nodes: [], edges: [] })[0]?.code).toBe("empty_graph");
    expect(validateWorkflowGraph({
      nodes: [
        { id: "a", type: "agent_task", position: { x: 0, y: 0 }, data: { label: "A", binding: { mode: "role_pool", role: "内容生产" } } },
        { id: "b", type: "decision", position: { x: 100, y: 0 }, data: { label: "B" } },
      ],
      edges: [{ id: "a-b", source: "a", target: "b" }, { id: "b-a", source: "b", target: "a" }],
    }).some((issue) => issue.code === "cycle")).toBe(true);
  });

  it("rejects an unbound fixed employee and duplicate node identity", () => {
    const issues = validateWorkflowGraph({
      nodes: [
        { id: "agent", type: "agent_task", position: { x: 0, y: 0 }, data: { label: "撰稿", binding: { mode: "fixed_employee", employeeId: "" } } },
        { id: "agent", type: "approval", position: { x: 100, y: 0 }, data: { label: "审核" } },
      ],
      edges: [],
    });
    expect(issues.map((issue) => issue.code)).toContain("invalid_binding");
    expect(issues.map((issue) => issue.code)).toContain("duplicate_node");
  });
});
