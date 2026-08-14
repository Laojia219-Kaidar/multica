"use client";

import { useCallback, useMemo, useState } from "react";
import {
  Background,
  Controls,
  ReactFlow,
  type Connection,
  type Edge,
  type Node,
  type OnConnect,
  type OnNodesChange,
  type OnEdgesChange,
  applyEdgeChanges,
  applyNodeChanges,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Plus, Save, Trash2, X } from "lucide-react";
import { validateWorkflowGraph } from "@multica/core/workflow";
import type { WorkflowDefinitionDraft, WorkflowNode, WorkflowNodeData, WorkflowNodeKind, WorkflowGraph } from "@multica/core/workflow";

const NODE_LABELS: Record<WorkflowNodeKind, string> = {
  agent_task: "Agent 任务",
  human_task: "人工任务",
  approval: "审批",
  decision: "决策",
};

const INITIAL_DATA: WorkflowNodeData = {
  label: "新节点",
  binding: { mode: "role_pool", role: "内容生产" },
  risk: "standard",
  evidenceRequired: true,
};

function toFlowNode(node: WorkflowNode): Node<WorkflowNodeData> {
  return { id: node.id, type: "default", position: node.position, data: node.data };
}

function toDomainGraph(nodes: Node<WorkflowNodeData>[], edges: Edge[]): WorkflowGraph {
  return {
    nodes: nodes.map((node) => {
      const data = node.data as WorkflowNodeData & { kind?: WorkflowNodeKind };
      return { id: node.id, type: data.kind ?? "agent_task", position: node.position, data };
    }),
    edges: edges.map((edge) => ({ id: edge.id, source: edge.source, target: edge.target })),
  };
}

export interface WorkflowDesignerProps {
  definition: WorkflowDefinitionDraft;
  onChange?: (definition: WorkflowDefinitionDraft) => void;
  onPublish?: (definition: WorkflowDefinitionDraft) => void;
  readOnly?: boolean;
}

export function WorkflowDesigner({ definition, onChange, onPublish, readOnly = false }: WorkflowDesignerProps) {
  const [nodes, setNodes] = useState<Node<WorkflowNodeData>[]>(() => definition.graph.nodes.map(toFlowNode));
  const [edges, setEdges] = useState<Edge[]>(() => definition.graph.edges);
  const [selectedId, setSelectedId] = useState<string>();
  const [issues, setIssues] = useState(() => validateWorkflowGraph(definition.graph));
  const selected = nodes.find((node) => node.id === selectedId);

  const emit = useCallback((nextNodes: Node<WorkflowNodeData>[], nextEdges: Edge[]) => {
    const graph = toDomainGraph(nextNodes, nextEdges);
    setIssues(validateWorkflowGraph(graph));
    onChange?.({ ...definition, graph });
  }, [definition, onChange]);

  const onNodesChange: OnNodesChange<Node<WorkflowNodeData>> = useCallback((changes) => {
    setNodes((current) => {
      const next = applyNodeChanges(changes, current);
      emit(next, edges);
      return next;
    });
  }, [edges, emit]);

  const onEdgesChange: OnEdgesChange = useCallback((changes) => {
    setEdges((current) => {
      const next = applyEdgeChanges(changes, current);
      emit(nodes, next);
      return next;
    });
  }, [emit, nodes]);

  const onConnect: OnConnect = useCallback((connection: Connection) => {
    if (!connection.source || !connection.target) return;
    setEdges((current) => {
      const next = [...current, { ...connection, id: `${connection.source}-${connection.target}-${Date.now()}` } as Edge];
      emit(nodes, next);
      return next;
    });
  }, [emit, nodes]);

  const addNode = (kind: WorkflowNodeKind = "agent_task") => {
    const id = `${kind}-${nodes.length + 1}`;
    const data = { ...INITIAL_DATA, kind, label: NODE_LABELS[kind] } as WorkflowNodeData & { kind: WorkflowNodeKind };
    const next = [...nodes, { id, type: "default", position: { x: 80 + nodes.length * 50, y: 80 + nodes.length * 30 }, data }];
    setNodes(next);
    setSelectedId(id);
    emit(next, edges);
  };

  const connectSelected = () => {
    if (!selectedId || nodes.length < 2) return;
    const target = nodes.find((node) => node.id !== selectedId && !edges.some((edge) => edge.source === selectedId && edge.target === node.id));
    if (target) onConnect({ source: selectedId, target: target.id, sourceHandle: null, targetHandle: null });
  };

  const removeSelected = () => {
    if (!selectedId) return;
    const nextNodes = nodes.filter((node) => node.id !== selectedId);
    const nextEdges = edges.filter((edge) => edge.source !== selectedId && edge.target !== selectedId);
    setNodes(nextNodes);
    setEdges(nextEdges);
    setSelectedId(undefined);
    emit(nextNodes, nextEdges);
  };

  const updateSelected = (patch: Partial<WorkflowNodeData>) => {
    if (!selectedId) return;
    const next = nodes.map((node) => node.id === selectedId ? { ...node, data: { ...node.data, ...patch } } : node);
    setNodes(next);
    emit(next, edges);
  };

  const domainGraph = useMemo(() => toDomainGraph(nodes, edges), [edges, nodes]);
  const defaultNodeStyle = { borderRadius: 8, border: "1px solid hsl(var(--border))", padding: 8, background: "hsl(var(--card))", color: "hsl(var(--foreground))", fontSize: 12 };
  const flowNodes = nodes.map((node) => ({ ...node, style: defaultNodeStyle, data: { ...node.data, label: `${NODE_LABELS[(node.data as WorkflowNodeData & { kind?: WorkflowNodeKind }).kind ?? "agent_task"]}: ${node.data.label}` } }));

  return (
    <section className="flex min-h-[520px] flex-col rounded-lg border bg-card" data-testid="workflow-designer">
      <header className="flex flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
        <div>
          <h3 className="text-sm font-semibold">工作流设计器</h3>
          <p className="text-xs text-muted-foreground">{definition.name} · 草稿 v{definition.version}</p>
        </div>
        {!readOnly ? (
          <div className="flex flex-wrap gap-1.5">
            <button type="button" onClick={() => addNode()} className="inline-flex items-center gap-1 rounded border px-2 py-1 text-xs hover:bg-accent"><Plus className="h-3.5 w-3.5" />添加节点</button>
            <button type="button" onClick={connectSelected} disabled={!selectedId || nodes.length < 2} className="rounded border px-2 py-1 text-xs hover:bg-accent disabled:opacity-50">连接到下一个</button>
            <button type="button" onClick={removeSelected} disabled={!selectedId} className="inline-flex items-center gap-1 rounded border px-2 py-1 text-xs hover:bg-accent disabled:opacity-50"><Trash2 className="h-3.5 w-3.5" />删除</button>
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
            onNodeClick={(_, node) => setSelectedId(node.id)}
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
          {selected ? (
            <div className="space-y-3">
              <div className="flex items-center justify-between"><h4 className="text-xs font-semibold">节点 Inspector</h4><button type="button" aria-label="关闭节点 Inspector" onClick={() => setSelectedId(undefined)}><X className="h-4 w-4" /></button></div>
              <label className="block text-xs">名称<input aria-label="节点名称" value={selected.data.label} onChange={(event) => updateSelected({ label: event.target.value })} className="mt-1 w-full rounded border bg-background px-2 py-1 text-xs" /></label>
              <div className="text-xs text-muted-foreground">类型：{NODE_LABELS[(selected.data as WorkflowNodeData & { kind?: WorkflowNodeKind }).kind ?? "agent_task"]}</div>
              <div className="text-xs text-muted-foreground">节点 ID：<span className="font-mono">{selected.id}</span></div>
              <div className="text-xs text-muted-foreground">绑定：{selected.data.binding?.mode === "role_pool" ? selected.data.binding.role : selected.data.binding?.mode ?? "未设置"}</div>
            </div>
          ) : <p className="text-xs text-muted-foreground">选择节点查看配置</p>}
          {issues.length > 0 ? <div className="mt-5 rounded-md border border-destructive/40 bg-destructive/5 p-2" data-testid="workflow-validation-errors"><p className="text-xs font-semibold text-destructive">流程校验未通过</p><ul className="mt-1 list-disc pl-4 text-[11px] text-destructive">{issues.map((issue) => <li key={`${issue.code}-${issue.message}`}>{issue.message}</li>)}</ul></div> : <p className="mt-5 text-xs text-emerald-600">流程结构有效，可发布</p>}
        </aside>
      </div>
    </section>
  );
}
