import { describe, expect, it } from "vitest";
import { validateWorkflowGraph } from "./graph-validation";

describe("validateWorkflowGraph", () => {
  it("accepts a directed acyclic graph with a start and end", () => {
    expect(validateWorkflowGraph({
      nodes: [
        { id: "start", type: "agent_task", position: { x: 0, y: 0 }, data: { label: "开始" } },
        { id: "end", type: "approval", position: { x: 100, y: 0 }, data: { label: "审批" } },
      ],
      edges: [{ id: "start-end", source: "start", target: "end" }],
    })).toEqual([]);
  });

  it("reports empty graphs and cycles", () => {
    expect(validateWorkflowGraph({ nodes: [], edges: [] })[0]?.code).toBe("empty_graph");
    expect(validateWorkflowGraph({
      nodes: [
        { id: "a", type: "agent_task", position: { x: 0, y: 0 }, data: { label: "A" } },
        { id: "b", type: "decision", position: { x: 100, y: 0 }, data: { label: "B" } },
      ],
      edges: [{ id: "a-b", source: "a", target: "b" }, { id: "b-a", source: "b", target: "a" }],
    }).some((issue) => issue.code === "cycle")).toBe(true);
  });
});
