import { z } from "zod";
import {
  SHA256_DIGEST_PATTERN,
  WECHAT_CONTENT_APPROVAL_POLICIES,
  WECHAT_CONTENT_CHANNEL,
  WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION,
  WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES,
  WECHAT_CONTENT_LINEAGE_AUTHORITIES,
  WECHAT_CONTENT_NODE_KEYS,
  WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION,
  WECHAT_CONTENT_REVIEW_RULES,
  WECHAT_WORK_ORDER_SOURCE_REF_PATTERN,
  validateWechatContentNodePlan,
  validateWechatContentProductionRequest,
  wechatContentUtf8ByteLength,
} from "../workflow/content-node-contract";

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

/**
 * L3 operating subject persisted by the workflow organization registry.  It
 * only classifies existing formal Projects; it does not copy Project lifecycle
 * fields or own Outcome/Artifact data.
 */
export const WorkflowOperatingProgramSchema = z.object({
  id: z.string().uuid(),
  workspace_id: z.string().uuid(),
  name: z.string().min(1),
  description: z.string(),
  project_ids: z.array(z.string().uuid()),
  created_at: z.string().datetime(),
  updated_at: z.string().datetime(),
}).strict();

export const WorkflowOperatingProgramReceiptSchema = z.object({
  changed: z.boolean(),
  accepted: z.boolean(),
  replayed: z.boolean().optional(),
}).strict();

export const WorkflowOperatingProgramCommandResponseSchema = z.object({
  program: WorkflowOperatingProgramSchema,
  receipt: WorkflowOperatingProgramReceiptSchema,
}).strict();

export const WorkflowOperatingProgramProjectCommandResponseSchema = z.object({
  program_id: z.string().uuid(),
  project_id: z.string().uuid(),
  receipt: WorkflowOperatingProgramReceiptSchema,
}).strict();

export type RiskTier = z.infer<typeof RiskTierSchema>;
export type InstanceStatus = z.infer<typeof InstanceStatusSchema>;
export type WorkflowDefinition = z.infer<typeof WorkflowDefinitionSchema>;
export type PublishedWorkflowDefinitionVersion = z.infer<typeof PublishedWorkflowDefinitionVersionSchema>;
export type PublishedWorkflowGraph = z.infer<typeof PublishedWorkflowGraphSchema>;
export type PublishWorkflowDefinitionVersionResponse = z.infer<typeof PublishWorkflowDefinitionVersionResponseSchema>;
export type WorkflowInstance = z.infer<typeof WorkflowInstanceSchema>;
export type WorkflowEvent = z.infer<typeof WorkflowEventSchema>;
export type ControlReceipt = z.infer<typeof ControlReceiptSchema>;
export type WorkflowOperatingProgram = z.infer<typeof WorkflowOperatingProgramSchema>;
export type WorkflowOperatingProgramReceipt = z.infer<typeof WorkflowOperatingProgramReceiptSchema>;
export type WorkflowOperatingProgramCommandResponse = z.infer<typeof WorkflowOperatingProgramCommandResponseSchema>;
export type WorkflowOperatingProgramProjectCommandResponse = z.infer<typeof WorkflowOperatingProgramProjectCommandResponseSchema>;


// ---------------------------------------------------------------------------
// WeChat content production node contract (HIVECREW-WECHAT-REAL-OPERATIONS-V1 /
// WO-10, contract-freeze). Strict wire: unknown keys rejected; caller-supplied
// execution/artifact/outcome proof is never accepted (caller refs never prove
// authority — proof is server-issued Task/Run/execution_receipt/Candidate/
// Outcome only). These DTOs create no second Task/Run/Artifact/Outcome
// authority.
// ---------------------------------------------------------------------------

export const WechatContentAuthorityContextSchema = z
  .object({
    work_order_source_ref: z
      .string()
      .regex(
        WECHAT_WORK_ORDER_SOURCE_REF_PATTERN,
        "must be hive://hivecosm/delivery/project/{project}/work-order/{work-order}",
      ),
    employee_id: z.string().min(1),
    identity_binding_id: z.string().min(1),
    agent_id: z.string().uuid(),
    session_id: z.string().uuid(),
  })
  .strict();

export const WechatContentDefinitionBindingSchema = z
  .object({
    definition_id: z.string().min(1),
    version: z.number().int().positive(),
    digest: z.string().regex(SHA256_DIGEST_PATTERN, "must be sha256:{64 hex}"),
  })
  .strict();

export const WechatContentBriefSchema = z
  .object({
    subject: z.string().trim().min(1),
    objective: z.string().trim().min(1),
    audience: z.string().trim().min(1),
    source_refs: z.array(z.string().trim().min(1)).min(1),
    tone: z.string().trim().min(1),
    // RFC3339 with timezone; numeric offsets (+08:00) are legal, matching
    // the TS pure validator and Go time.RFC3339Nano parsing.
    deadline: z.iso.datetime({ offset: true }),
    approval_policy: z.enum(WECHAT_CONTENT_APPROVAL_POLICIES),
    // Exact work description delivered to the executing Agent. Mirrors the
    // existing CompanyOps assignment Handoff semantics: trimmed non-empty,
    // max 32 KiB UTF-8; the server computes input_digest from it.
    handoff_note: z
      .string()
      .refine((value) => value.trim().length > 0, {
        message: "handoff_note is required and must describe the work to dispatch",
      })
      .refine(
        (value) =>
          wechatContentUtf8ByteLength(value) <=
          WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES,
        {
          message: `handoff_note must be at most ${WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES} UTF-8 bytes`,
        },
      ),
  })
  .strict();

export const WechatContentProductionRequestSchema = z
  .object({
    schema_version: z.literal(WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION),
    channel: z.literal(WECHAT_CONTENT_CHANNEL),
    project_id: z.string().min(1),
    authority: WechatContentAuthorityContextSchema,
    definition: WechatContentDefinitionBindingSchema,
    brief: WechatContentBriefSchema,
    idempotency_key: z.string().min(1),
  })
  .strict()
  .superRefine((value, ctx) => {
    // Delegates the fail-closed semantic checks (notably cross-project
    // authority mismatch) to the single pure validator so wire and contract
    // never drift.
    const result = validateWechatContentProductionRequest(value);
    if (!result.ok) {
      for (const item of result.issues) {
        ctx.addIssue({
          code: "custom",
          path: item.path,
          message: item.message,
        });
      }
    }
  });

/**
 * Pure acknowledgment of a submitted request. It deliberately carries NO
 * task_id/run_id/execution_receipt/candidate/outcome: accepting a request is
 * never proof of execution.
 */
export const WechatContentProductionRequestReceiptSchema = z
  .object({
    schema_version: z.literal(
      "hivecrew.wechat-content-production-request-receipt.v1",
    ),
    request_id: z.string().min(1),
    idempotency_key: z.string().min(1),
    accepted: z.boolean(),
    replayed: z.boolean(),
    reason: z.string(),
  })
  .strict();

/**
 * One frozen lineage member. The authority is a per-member contract literal
 * (WECHAT_CONTENT_LINEAGE_AUTHORITIES), never a caller-chosen string.
 */
const wechatLineageMemberSchema = (authority: string) =>
  z
    .object({
      required: z.literal(true),
      authority: z.literal(authority),
    })
    .strict();

export const WechatContentNodeLineageShapeSchema = z
  .object({
    issue: wechatLineageMemberSchema(WECHAT_CONTENT_LINEAGE_AUTHORITIES.issue),
    assignment: wechatLineageMemberSchema(
      WECHAT_CONTENT_LINEAGE_AUTHORITIES.assignment,
    ),
    task: wechatLineageMemberSchema(WECHAT_CONTENT_LINEAGE_AUTHORITIES.task),
    run: wechatLineageMemberSchema(WECHAT_CONTENT_LINEAGE_AUTHORITIES.run),
    candidate: wechatLineageMemberSchema(
      WECHAT_CONTENT_LINEAGE_AUTHORITIES.candidate,
    ),
    outcome: wechatLineageMemberSchema(
      WECHAT_CONTENT_LINEAGE_AUTHORITIES.outcome,
    ),
  })
  .strict();

export const WechatContentNodeContractSchema = z
  .object({
    key: z.enum(WECHAT_CONTENT_NODE_KEYS),
    order: z.number().int().positive(),
    required_upstream: z.enum(WECHAT_CONTENT_NODE_KEYS).nullable(),
    artifact_kind: z.string().min(1),
    review_rule: z.enum(WECHAT_CONTENT_REVIEW_RULES),
    lineage: WechatContentNodeLineageShapeSchema,
  })
  .strict();

export const WechatContentProductionContractSchema = z
  .object({
    schema_version: z.literal(WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION),
    channel: z.literal(WECHAT_CONTENT_CHANNEL),
    nodes: z.tuple([
      WechatContentNodeContractSchema,
      WechatContentNodeContractSchema,
      WechatContentNodeContractSchema,
      WechatContentNodeContractSchema,
    ]),
  })
  .strict()
  .superRefine((value, ctx) => {
    // The four nodes are immutable: duplicate/unknown/missing/altered nodes
    // and broken prerequisites fail closed against the frozen table.
    const result = validateWechatContentNodePlan(value.nodes);
    if (!result.ok) {
      for (const item of result.issues) {
        ctx.addIssue({
          code: "custom",
          path: ["nodes", ...(item.path ?? [])],
          message: item.message,
        });
      }
    }
  });

export type WechatContentAuthorityContext = z.infer<
  typeof WechatContentAuthorityContextSchema
>;
export type WechatContentDefinitionBinding = z.infer<
  typeof WechatContentDefinitionBindingSchema
>;
export type WechatContentBrief = z.infer<typeof WechatContentBriefSchema>;
export type WechatContentProductionRequest = z.infer<
  typeof WechatContentProductionRequestSchema
>;
export type WechatContentProductionRequestReceipt = z.infer<
  typeof WechatContentProductionRequestReceiptSchema
>;
export type WechatContentNodeContract = z.infer<
  typeof WechatContentNodeContractSchema
>;
export type WechatContentProductionContract = z.infer<
  typeof WechatContentProductionContractSchema
>;
