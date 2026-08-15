import type { WorkflowGraph } from "./types";

export type GraphValidationIssue = {
  code: "empty_graph" | "duplicate_node" | "invalid_node" | "invalid_binding" | "missing_start" | "missing_end" | "unknown_edge" | "cycle";
  message: string;
  nodeIds?: string[];
};

export function validateWorkflowGraph(graph: WorkflowGraph): GraphValidationIssue[] {
  if (graph.nodes.length === 0) {
    return [{ code: "empty_graph", message: "工作流至少需要一个节点" }];
  }

  const nodeIds = new Set<string>();
  const issues: GraphValidationIssue[] = [];
  for (const node of graph.nodes) {
    if (!node.id || nodeIds.has(node.id)) {
      issues.push({ code: "duplicate_node", message: `节点 ${node.id || "(缺失)"} 的 ID 不唯一` });
    }
    nodeIds.add(node.id);
    if (!node.data.label.trim()) {
      issues.push({ code: "invalid_node", message: `节点 ${node.id || "(缺失)"} 必须填写名称` });
    }
    if (node.type === "agent_task") {
      if (!node.data.binding) {
        issues.push({ code: "invalid_binding", message: `Agent 节点 ${node.id} 必须绑定正式员工、角色池或项目默认执行者` });
      } else if (node.data.binding.mode === "fixed_employee" && !node.data.binding.employeeId.trim()) {
        issues.push({ code: "invalid_binding", message: `固定员工节点 ${node.id} 必须填写正式 Employee ID` });
      } else if (node.data.binding.mode === "role_pool" && !node.data.binding.role.trim()) {
        issues.push({ code: "invalid_binding", message: `角色池节点 ${node.id} 必须填写执行角色` });
      }
    }
  }
  const incoming = new Map(graph.nodes.map((node) => [node.id, 0]));
  const outgoing = new Map(graph.nodes.map((node) => [node.id, 0]));

  for (const edge of graph.edges) {
    if (!nodeIds.has(edge.source) || !nodeIds.has(edge.target)) {
      issues.push({ code: "unknown_edge", message: `连线 ${edge.id} 引用了不存在的节点` });
      continue;
    }
    incoming.set(edge.target, (incoming.get(edge.target) ?? 0) + 1);
    outgoing.set(edge.source, (outgoing.get(edge.source) ?? 0) + 1);
  }

  const starts = graph.nodes.filter((node) => (incoming.get(node.id) ?? 0) === 0);
  const ends = graph.nodes.filter((node) => (outgoing.get(node.id) ?? 0) === 0);
  if (starts.length === 0) issues.push({ code: "missing_start", message: "工作流必须有入口节点" });
  if (ends.length === 0) issues.push({ code: "missing_end", message: "工作流必须有结束节点" });

  const visiting = new Set<string>();
  const visited = new Set<string>();
  const adjacency = new Map<string, string[]>();
  for (const edge of graph.edges) {
    if (!nodeIds.has(edge.source) || !nodeIds.has(edge.target)) continue;
    adjacency.set(edge.source, [...(adjacency.get(edge.source) ?? []), edge.target]);
  }

  const visit = (id: string): boolean => {
    if (visiting.has(id)) return true;
    if (visited.has(id)) return false;
    visiting.add(id);
    const hasCycle = (adjacency.get(id) ?? []).some(visit);
    visiting.delete(id);
    visited.add(id);
    return hasCycle;
  };

  const cycleStart = graph.nodes.find((node) => visit(node.id));
  if (cycleStart) issues.push({ code: "cycle", message: "工作流不能包含循环依赖", nodeIds: [cycleStart.id] });
  return issues;
}
