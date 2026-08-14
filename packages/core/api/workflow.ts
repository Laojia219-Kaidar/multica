import { z } from "zod";

// Wire contract for the workflow kernel (server/internal/workflow). Strict
// wire: unknown keys rejected. This pins the public DTO shape + closed enums;
// the engine itself never writes Task/Run/Project/Outcome state.

export const RiskTierSchema = z.enum(["fast", "standard", "owner"]);
export const InstanceStatusSchema = z.enum([
  "running",
  "paused",
  "stopped",
  "completed",
  "failed",
]);

export const WorkflowStageSchema = z.object({
  name: z.string(),
  sla_seconds: z.number().int().nonnegative().optional(),
});

export const WorkflowNodeKindSchema = z.enum([
  "agent_task",
  "human_task",
  "approval",
  "decision",
]);

export const WorkflowGraphPositionSchema = z.object({
  x: z.number(),
  y: z.number(),
}).strict();

export const PublishedWorkflowAgentBindingSchema = z.object({
  mode: z.enum(["fixed_employee", "capability_pool", "role_pool", "project_default", "human"]),
  employee_id: z.string().optional(),
  capabilities: z.array(z.string()).optional(),
  role: z.string().optional(),
  capability: z.string().optional(),
}).strict();

export const PublishedWorkflowGraphNodeSchema = z.object({
  id: z.string().min(1),
  kind: WorkflowNodeKindSchema,
  name: z.string().min(1),
  agent_binding: PublishedWorkflowAgentBindingSchema.optional(),
  position: WorkflowGraphPositionSchema.optional(),
}).strict();

export const PublishedWorkflowGraphEdgeSchema = z.object({
  id: z.string().min(1),
  from: z.string().min(1),
  to: z.string().min(1),
  when: z.string().optional(),
}).strict();

export const PublishedWorkflowGraphSchema = z.object({
  nodes: z.array(PublishedWorkflowGraphNodeSchema).min(1),
  edges: z.array(PublishedWorkflowGraphEdgeSchema),
}).strict();

export const PublishedWorkflowStageSchema = z.object({
  name: z.string().min(1),
  sla_ns: z.number().int().nonnegative().optional(),
}).strict();

export const PublishedWorkflowDefinitionVersionSchema = z.object({
  definition_id: z.string().min(1),
  workspace_id: z.string().uuid(),
  project_id: z.string(),
  version: z.number().int().positive(),
  risk: RiskTierSchema,
  stages: z.array(PublishedWorkflowStageSchema),
  graph: PublishedWorkflowGraphSchema,
  digest: z.string().startsWith("sha256:"),
  created_at: z.string().datetime(),
  published_at: z.string().datetime(),
}).strict();

export const PublishWorkflowDefinitionVersionReceiptSchema = z.object({
  idempotency_key: z.string().min(1),
  changed: z.boolean(),
  accepted: z.boolean(),
}).strict();

export const PublishWorkflowDefinitionVersionResponseSchema = z.object({
  version: PublishedWorkflowDefinitionVersionSchema,
  receipt: PublishWorkflowDefinitionVersionReceiptSchema,
}).strict();

export const WorkflowDefinitionSchema = z
  .object({
    id: z.string(),
    version: z.number().int().positive(),
    risk: RiskTierSchema,
    stages: z.array(WorkflowStageSchema).min(1),
  })
  .strict();

export const ContextRefSchema = z.object({
  project_id: z.string().optional(),
  issue_id: z.string().optional(),
  outcome_id: z.string().optional(),
});

export const ControlReceiptSchema = z
  .object({
    command: z.string(),
    instance_id: z.string(),
    idempotency_key: z.string(),
    accepted: z.boolean(),
    changed: z.boolean(),
    reason: z.string(),
  })
  .strict();

export const WorkflowInstanceSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string().uuid().optional(),
    definition_id: z.string(),
    definition_version: z.number().int().positive(),
    context: ContextRefSchema,
    stage_index: z.number().int().nonnegative(),
    status: InstanceStatusSchema,
    receipt: ControlReceiptSchema.optional(),
  })
  .strict();

export const WorkflowEventSchema = z
  .object({
    sequence: z.number().int().nonnegative(),
    instance_id: z.string(),
    kind: z.string(),
    source_ref: z.string(),
    actor: z.string(),
    occurred_at: z.string(),
    observed_at: z.string(),
    idempotency_key: z.string(),
  })
  .strict();

export type RiskTier = z.infer<typeof RiskTierSchema>;
export type InstanceStatus = z.infer<typeof InstanceStatusSchema>;
export type WorkflowDefinition = z.infer<typeof WorkflowDefinitionSchema>;
export type PublishedWorkflowDefinitionVersion = z.infer<typeof PublishedWorkflowDefinitionVersionSchema>;
export type PublishedWorkflowGraph = z.infer<typeof PublishedWorkflowGraphSchema>;
export type PublishWorkflowDefinitionVersionResponse = z.infer<typeof PublishWorkflowDefinitionVersionResponseSchema>;
export type WorkflowInstance = z.infer<typeof WorkflowInstanceSchema>;
export type WorkflowEvent = z.infer<typeof WorkflowEventSchema>;
export type ControlReceipt = z.infer<typeof ControlReceiptSchema>;
