export type OperatingProgram = {
  id: string;
  name: string;
  description?: string;
  projectIds: string[];
};

export type OperatingProject = {
  id: string;
  programId: string;
  formalProjectId: string;
  name: string;
  platform?: string;
  status?: "active" | "paused" | "archived";
};

export type WorkflowNodeKind = "agent_task" | "human_task" | "approval" | "decision";

export type WorkflowNodeStatus =
  | "not_started"
  | "ready"
  | "running"
  | "waiting_approval"
  | "blocked"
  | "failed"
  | "passed"
  | "stopped"
  | "skipped";

export type WorkflowAgentBinding =
  | { mode: "fixed_employee"; employeeId: string; employeeName?: string }
  | { mode: "role_pool"; role: string; capability?: string }
  | { mode: "project_default" }
  | { mode: "human" };

export type WorkflowNodeData = {
  label: string;
  description?: string;
  binding?: WorkflowAgentBinding;
  inputSchema?: string;
  outputSchema?: string;
  risk?: "fast" | "standard" | "owner";
  evidenceRequired?: boolean;
};

export type WorkflowNode = {
  id: string;
  type: WorkflowNodeKind;
  position: { x: number; y: number };
  data: WorkflowNodeData;
};

export type WorkflowEdge = {
  id: string;
  source: string;
  target: string;
  condition?: string;
};

export type WorkflowGraph = {
  nodes: WorkflowNode[];
  edges: WorkflowEdge[];
};

export type WorkflowDefinitionDraft = {
  id: string;
  name: string;
  version: number;
  projectId?: string;
  graph: WorkflowGraph;
};

export type RuntimeNode = {
  nodeId: string;
  status: WorkflowNodeStatus;
  employeeId?: string;
  employeeName?: string;
  taskId?: string;
  runId?: string;
  evidenceIds?: string[];
  error?: string;
};

export type WorkflowRuntime = {
  instanceId: string;
  definitionId: string;
  version: number;
  status: WorkflowNodeStatus;
  nodes: RuntimeNode[];
};

export type ArtifactSummary = {
  id: string;
  version: number;
  title: string;
  status: "draft" | "candidate" | "in_review" | "accepted" | "superseded" | "archived";
  locationCount?: number;
};
