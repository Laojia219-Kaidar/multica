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

export const WorkflowInstanceSchema = z
  .object({
    id: z.string(),
    definition_id: z.string(),
    definition_version: z.number().int().positive(),
    context: ContextRefSchema,
    stage_index: z.number().int().nonnegative(),
    status: InstanceStatusSchema,
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

export type RiskTier = z.infer<typeof RiskTierSchema>;
export type InstanceStatus = z.infer<typeof InstanceStatusSchema>;
export type WorkflowDefinition = z.infer<typeof WorkflowDefinitionSchema>;
export type WorkflowInstance = z.infer<typeof WorkflowInstanceSchema>;
export type WorkflowEvent = z.infer<typeof WorkflowEventSchema>;
export type ControlReceipt = z.infer<typeof ControlReceiptSchema>;
