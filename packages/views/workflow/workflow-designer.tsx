"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Background,
  Controls,
  ReactFlow,
  type Connection,
  type Edge,
  type Node,
  type OnConnect,
  type OnEdgesChange,
  type OnNodesChange,
  applyEdgeChanges,
  applyNodeChanges,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Plus, Save, Trash2, X } from "lucide-react";
import { validateWorkflowGraph } from "@multica/core/workflow";
import type {
  WorkflowAgentBinding,
  WorkflowDefinitionDraft,
  WorkflowEdge,
  WorkflowGraph,
  WorkflowNode,
  WorkflowNodeData,
  WorkflowNodeKind,
} from "@multica/core/workflow";

type FlowNodeData = WorkflowNodeData & { kind: WorkflowNodeKind };
type FlowEdgeData = { condition?: string };

const NODE_LABELS: Record<WorkflowNodeKind, string> = {
  agent_task: "Agent 任务",
  human_task: "人工任务",
  approval: "审批",
  decision: "决策",
};

function bindingForKind(kind: WorkflowNodeKind): WorkflowAgentBinding | undefined {
  switch (kind) {
    case "agent_task":
      return { mode: "role_pool", role: "内容生产" };
    case "human_task":
      return { mode: "human" };
    default:
      return undefined;
  }
}

function dataForKind(kind: WorkflowNodeKind, label = NODE_LABELS[kind]): FlowNodeData {
  return {
    kind,
    label,
    binding: bindingForKind(kind),
    risk: "standard",
    evidenceRequired: true,
  };
}

function toFlowNode(node: WorkflowNode): Node<FlowNodeData> {
  return {
    id: node.id,
    type: "default",
    position: node.position,
    data: { ...node.data, kind: node.type },
  };
}

function toFlowEdge(edge: WorkflowEdge): Edge<FlowEdgeData> {
  return {
    id: edge.id,
    source: edge.source,
    target: edge.target,
    data: edge.condition ? { condition: edge.condition } : undefined,
  };
}

function toDomainGraph(nodes: Node<FlowNodeData>[], edges: Edge<FlowEdgeData>[]): WorkflowGraph {
  return {
    nodes: nodes.map((node) => ({
      id: node.id,
      type: node.data.kind,
      position: node.position,
      data: { ...node.data, kind: undefined },
    })),
    edges: edges.map((edge) => ({
      id: edge.id,
      source: edge.source,
      target: edge.target,
      condition: edge.data?.condition || undefined,
    })),
  };
}

export interface WorkflowDesignerProps {
  definition: WorkflowDefinitionDraft;
  onChange?: (definition: WorkflowDefinitionDraft) => void;
  onPublish?: (definition: WorkflowDefinitionDraft) => void;
  readOnly?: boolean;
}

export function WorkflowDesigner({ definition, onChange, onPublish, readOnly = false }: WorkflowDesignerProps) {
  const [nodes, setNodes] = useState<Node<FlowNodeData>[]>(() => definition.graph.nodes.map(toFlowNode));
  const [edges, setEdges] = useState<Edge<FlowEdgeData>[]>(() => definition.graph.edges.map(toFlowEdge));
  const [selectedId, setSelectedId] = useState<string>();
  const [selectedEdgeId, setSelectedEdgeId] = useState<string>();
  const [issues, setIssues] = useState(() => validateWorkflowGraph(definition.graph));
  const definitionIdentity = `${definition.id}:${definition.version}`;
  const selected = nodes.find((node) => node.id === selectedId);
  const selectedEdge = edges.find((edge) => edge.id === selectedEdgeId);

  useEffect(() => {
    setNodes(definition.graph.nodes.map(toFlowNode));
    setEdges(definition.graph.edges.map(toFlowEdge));
    setSelectedId(undefined);
    setSelectedEdgeId(undefined);
    setIssues(validateWorkflowGraph(definition.graph));
  }, [definitionIdentity]); // Reset only when selecting a different durable draft/version.

  const emit = useCallback((nextNodes: Node<FlowNodeData>[], nextEdges: Edge<FlowEdgeData>[]) => {
    const graph = toDomainGraph(nextNodes, nextEdges);
    setIssues(validateWorkflowGraph(graph));
    onChange?.({ ...definition, graph });
  }, [definition, onChange]);

  const onNodesChange: OnNodesChange<Node<FlowNodeData>> = useCallback((changes) => {
    setNodes((current) => {
      const next = applyNodeChanges(changes, current);
      emit(next, edges);
      return next;
    });
  }, [edges, emit]);

  const onEdgesChange: OnEdgesChange = useCallback((changes) => {
    setEdges((current) => {
      const next = applyEdgeChanges(changes, current) as Edge<FlowEdgeData>[];
      emit(nodes, next);
      return next;
    });
  }, [emit, nodes]);

  const onConnect: OnConnect = useCallback((connection: Connection) => {
    if (!connection.source || !connection.target) return;
    setEdges((current) => {
      const next = [...current, {
        ...connection,
        id: `${connection.source}-${connection.target}-${Date.now()}`,
        data: { condition: "" },
      } as Edge<FlowEdgeData>];
      emit(nodes, next);
      return next;
    });
  }, [emit, nodes]);

  const addNode = (kind: WorkflowNodeKind) => {
    const id = `${kind}-${nodes.length + 1}`;
    const next = [...nodes, {
      id,
      type: "default",
      position: { x: 80 + nodes.length * 50, y: 80 + nodes.length * 30 },
      data: dataForKind(kind),
    }];
    setNodes(next);
    setSelectedId(id);
    setSelectedEdgeId(undefined);
    emit(next, edges);
  };

  const connectSelected = () => {
    if (!selectedId || nodes.length < 2) return;
    const target = nodes.find((node) => node.id !== selectedId && !edges.some((edge) => edge.source === selectedId && edge.target === node.id));
    if (target) onConnect({ source: selectedId, target: target.id, sourceHandle: null, targetHandle: null });
  };

  const removeSelected = () => {
    if (selectedId) {
      const nextNodes = nodes.filter((node) => node.id !== selectedId);
      const nextEdges = edges.filter((edge) => edge.source !== selectedId && edge.target !== selectedId);
      setNodes(nextNodes);
      setEdges(nextEdges);
      setSelectedId(undefined);
      emit(nextNodes, nextEdges);
      return;
    }
    if (selectedEdgeId) {
      const nextEdges = edges.filter((edge) => edge.id !== selectedEdgeId);
      setEdges(nextEdges);
      setSelectedEdgeId(undefined);
      emit(nodes, nextEdges);
    }
  };

  const updateSelected = (patch: Partial<WorkflowNodeData> & Partial<FlowNodeData>) => {
    if (!selectedId) return;
    const next = nodes.map((node) => node.id === selectedId ? { ...node, data: { ...node.data, ...patch } } : node);
    setNodes(next);
    emit(next, edges);
  };

  const updateSelectedKind = (kind: WorkflowNodeKind) => {
    if (!selectedId) return;
    const next = nodes.map((node) => node.id === selectedId ? {
      ...node,
      data: { ...node.data, kind, binding: bindingForKind(kind) },
    } : node);
    setNodes(next);
    emit(next, edges);
  };

  const updateSelectedEdge = (condition: string) => {
    if (!selectedEdgeId) return;
    const next = edges.map((edge) => edge.id === selectedEdgeId ? { ...edge, data: { ...edge.data, condition } } : edge);
    setEdges(next);
    emit(nodes, next);
  };

  const domainGraph = useMemo(() => toDomainGraph(nodes, edges), [edges, nodes]);
  const defaultNodeStyle = { borderRadius: 8, border: "1px solid hsl(var(--border))", padding: 8, background: "hsl(var(--card))", color: "hsl(var(--foreground))", fontSize: 12 };
  const flowNodes = nodes.map((node) => ({ ...node, style: defaultNodeStyle, data: { ...node.data, label: `${NODE_LABELS[node.data.kind]}: ${node.data.label}` } }));

  return (
    <section className="flex min-h-[520px] flex-col rounded-lg border bg-card" data-testid="workflow-designer">
      <header className="flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
        <div>
          <h3 className="text-sm font-semibold">工作流设计器</h3>
          <p className="text-xs text-muted-foreground">{definition.name} · 草稿 v{definition.version}</p>
        </div>
        {!readOnly ? (
          <div className="flex flex-wrap gap-1.5">
            {(Object.keys(NODE_LABELS) as WorkflowNodeKind[]).map((kind) => <button type="button" key={kind} onClick={() => addNode(kind)} className="inline-flex items-center gap-1 rounded border px-2 py-1 text-xs hover:bg-accent"><Plus className="h-3.5 w-3.5" />{NODE_LABELS[kind]}</button>)}
            <button type="button" onClick={connectSelected} disabled={!selectedId || nodes.length < 2} className="rounded border px-2 py-1 text-xs hover:bg-accent disabled:opacity-50">连接到下一个</button>
            <button type="button" onClick={removeSelected} disabled={!selectedId && !selectedEdgeId} className="inline-flex items-center gap-1 rounded border px-2 py-1 text-xs hover:bg-accent disabled:opacity-50"><Trash2 className="h-3.5 w-3.5" />删除</button>
            <button type="button" onClick={() => onPublish?.({ ...definition, graph: domainGraph })} disabled={issues.length > 0} className="inline-flex items-center gap-1 rounded border px-2 py-1 text-xs hover:bg-accent disabled:opacity-50"><Save className="h-3.5 w-3.5" />发布版本</button>
          </div>
        ) : null}
      </header>
      <div className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_260px]">
        <div className="min-h-[420px]" data-testid="workflow-graph-canvas">
          <ReactFlow
            nodes={flowNodes}
            edges={edges}
            onNodesChange={readOnly ? undefined : onNodesChange}
            onEdgesChange={readOnly ? undefined : onEdgesChange}
            onConnect={readOnly ? undefined : onConnect}
            onNodeClick={(_, node) => { setSelectedId(node.id); setSelectedEdgeId(undefined); }}
            onEdgeClick={(_, edge) => { setSelectedEdgeId(edge.id); setSelectedId(undefined); }}
            fitView
            nodesDraggable={!readOnly}
            nodesConnectable={!readOnly}
            elementsSelectable
          >
            <Background />
            <Controls />
          </ReactFlow>
        </div>
        <aside className="border-l p-3" data-testid="workflow-node-inspector">
          {selected ? <NodeInspector node={selected} readOnly={readOnly} onClose={() => setSelectedId(undefined)} onChange={updateSelected} onChangeKind={updateSelectedKind} /> : selectedEdge ? <EdgeInspector edge={selectedEdge} readOnly={readOnly} onClose={() => setSelectedEdgeId(undefined)} onChange={updateSelectedEdge} /> : <p className="text-xs text-muted-foreground">选择节点或连线查看配置</p>}
          {issues.length > 0 ? <div className="mt-5 rounded-md border border-destructive/40 bg-destructive/5 p-2" data-testid="workflow-validation-errors"><p className="text-xs font-semibold text-destructive">流程校验未通过</p><ul className="mt-1 list-disc pl-4 text-[11px] text-destructive">{issues.map((issue) => <li key={`${issue.code}-${issue.message}`}>{issue.message}</li>)}</ul></div> : <p className="mt-5 text-xs text-emerald-600">流程结构有效，可发布</p>}
        </aside>
      </div>
    </section>
  );
}

function NodeInspector({ node, readOnly, onClose, onChange, onChangeKind }: { node: Node<FlowNodeData>; readOnly: boolean; onClose: () => void; onChange: (patch: Partial<FlowNodeData>) => void; onChangeKind: (kind: WorkflowNodeKind) => void }) {
  const binding = node.data.binding;
  const changeBinding = (next: WorkflowAgentBinding | undefined) => onChange({ binding: next });
  return <div className="space-y-3">
    <div className="flex items-center justify-between"><h4 className="text-xs font-semibold">节点 Inspector</h4><button type="button" aria-label="关闭节点 Inspector" onClick={onClose}><X className="h-4 w-4" /></button></div>
    <label className="block text-xs">名称<input aria-label="节点名称" disabled={readOnly} value={node.data.label} onChange={(event) => onChange({ label: event.target.value })} className="mt-1 w-full rounded border bg-background px-2 py-1 text-xs disabled:opacity-70" /></label>
    <label className="block text-xs">类型<select aria-label="节点类型" disabled={readOnly} value={node.data.kind} onChange={(event) => onChangeKind(event.target.value as WorkflowNodeKind)} className="mt-1 w-full rounded border bg-background px-2 py-1 text-xs disabled:opacity-70">{(Object.keys(NODE_LABELS) as WorkflowNodeKind[]).map((kind) => <option value={kind} key={kind}>{NODE_LABELS[kind]}</option>)}</select></label>
    <div className="text-xs text-muted-foreground">节点 ID：<span className="font-mono">{node.id}</span></div>
    {node.data.kind === "agent_task" ? <AgentBindingFields binding={binding} disabled={readOnly} onChange={changeBinding} /> : node.data.kind === "human_task" ? <p className="text-xs text-muted-foreground">人工任务只记录人工处理，不会隐式绑定任意数字员工。</p> : <p className="text-xs text-muted-foreground">{node.data.kind === "decision" ? "分支条件在连线 Inspector 中配置。" : "审批节点的批准人由执行接入层显式绑定。"}</p>}
  </div>;
}

function AgentBindingFields({ binding, disabled, onChange }: { binding?: WorkflowAgentBinding; disabled: boolean; onChange: (binding: WorkflowAgentBinding) => void }) {
  const mode = binding?.mode === "fixed_employee" || binding?.mode === "project_default" || binding?.mode === "role_pool" ? binding.mode : "role_pool";
  return <div className="space-y-2 rounded border bg-muted/20 p-2"><label className="block text-xs">执行绑定<select aria-label="执行绑定" disabled={disabled} value={mode} onChange={(event) => {
    const next = event.target.value as "fixed_employee" | "role_pool" | "project_default";
    onChange(next === "fixed_employee" ? { mode: next, employeeId: "" } : next === "role_pool" ? { mode: next, role: "内容生产" } : { mode: next });
  }} className="mt-1 w-full rounded border bg-background px-2 py-1 text-xs disabled:opacity-70"><option value="role_pool">角色/能力池</option><option value="fixed_employee">固定数字员工</option><option value="project_default">项目默认执行者</option></select></label>
    {mode === "fixed_employee" ? <label className="block text-xs">数字员工 ID<input aria-label="数字员工 ID" disabled={disabled} value={binding?.mode === "fixed_employee" ? binding.employeeId : ""} onChange={(event) => onChange({ mode: "fixed_employee", employeeId: event.target.value })} placeholder="正式 Employee ID" className="mt-1 w-full rounded border bg-background px-2 py-1 text-xs disabled:opacity-70" /></label> : null}
    {mode === "role_pool" ? <><label className="block text-xs">角色<input aria-label="执行角色" disabled={disabled} value={binding?.mode === "role_pool" ? binding.role : ""} onChange={(event) => onChange({ mode: "role_pool", role: event.target.value, capability: binding?.mode === "role_pool" ? binding.capability : undefined })} className="mt-1 w-full rounded border bg-background px-2 py-1 text-xs disabled:opacity-70" /></label><label className="block text-xs">能力（可选）<input aria-label="执行能力" disabled={disabled} value={binding?.mode === "role_pool" ? binding.capability ?? "" : ""} onChange={(event) => onChange({ mode: "role_pool", role: binding?.mode === "role_pool" ? binding.role : "", capability: event.target.value || undefined })} className="mt-1 w-full rounded border bg-background px-2 py-1 text-xs disabled:opacity-70" /></label></> : null}
  </div>;
}

function EdgeInspector({ edge, readOnly, onClose, onChange }: { edge: Edge<FlowEdgeData>; readOnly: boolean; onClose: () => void; onChange: (condition: string) => void }) {
  return <div className="space-y-3"><div className="flex items-center justify-between"><h4 className="text-xs font-semibold">连线 Inspector</h4><button type="button" aria-label="关闭连线 Inspector" onClick={onClose}><X className="h-4 w-4" /></button></div><p className="text-xs text-muted-foreground"><span className="font-mono">{edge.source}</span> → <span className="font-mono">{edge.target}</span></p><label className="block text-xs">分支条件<input aria-label="分支条件" disabled={readOnly} value={edge.data?.condition ?? ""} onChange={(event) => onChange(event.target.value)} placeholder="例如：审核通过" className="mt-1 w-full rounded border bg-background px-2 py-1 text-xs disabled:opacity-70" /></label></div>;
}
