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

/**
 * Read-only adapter for the workflow kernel's existing event/control receipt
 * payloads. This is deliberately a view contract: it does not own lifecycle
 * state and must be populated from the authoritative API response.
 */
export type WorkflowReceiptView = {
  id: string;
  instanceId: string;
  kind: "event" | "control";
  status: "accepted" | "rejected" | "observed";
  label: string;
  sourceRef?: string;
  actor?: string;
  occurredAt?: string;
  observedAt?: string;
  idempotencyKey?: string;
  reason?: string;
};

/** Explicit query state so an empty response is never used as an error state. */
export type WorkflowDataState = "loading" | "ready" | "error";

/**
 * Compatibility seam for the future "create instance from published DAG"
 * endpoint. The UI may render a request affordance only when its integration
 * callback is supplied; it never manufactures an execution or success receipt.
 */
export type WorkflowInstanceCreationState = "unavailable" | "ready" | "loading" | "error";

export type ArtifactSummary = {
  id: string;
  version: number;
  title: string;
  status: "draft" | "candidate" | "in_review" | "accepted" | "superseded" | "archived";
  locationCount?: number;
};
