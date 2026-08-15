import { z } from "zod";

// Wire contract for the W4 "工作现场" (work wall) snapshot. Mirrors the Go
// DTO in server/internal/liveactivity (EmployeeLiveActivityV1). `.strict()`
// is intentional: unknown keys are rejected, matching the LIVE-WORKSITE
// "strict wire" rule (unknown key => reject).

export const PresenceStateSchema = z.enum([
  "offline",
  "idle",
  "queued",
  "working",
  "waiting",
  "blocked",
  "recently_completed",
  "unknown",
]);

export const WorkStageSchema = z.enum([
  "planning",
  "research",
  "coding",
  "testing",
  "reviewing",
  "repairing",
  "integrating",
  "operating",
  "reporting",
  "none",
  "unknown",
]);

export const FreshnessStateSchema = z.enum([
  "fresh",
  "stale",
  "missing",
  "conflict",
]);

// Closed LIVE-WORKSITE activity event protocol (至少支持集合). RecentEvent.kind
// is intentionally OPEN (activity.*/workflow.* bridge kinds are also allowed).
export const ActivityEventKindSchema = z.enum([
  "task.queued",
  "task.dispatched",
  "run.started",
  "run.heartbeat",
  "tool.started",
  "tool.completed",
  "command.started",
  "command.completed",
  "test.started",
  "test.result",
  "artifact.created",
  "review.requested",
  "review.verdict",
  "repair.requested",
  "run.waiting",
  "run.blocked",
  "run.completed",
  "run.failed",
  "runtime.offline",
]);

export type ActivityEventKind = z.infer<typeof ActivityEventKindSchema>;

export const RecentEventSchema = z.object({
  event_id: z.string(),
  kind: z.string(),
  safe_summary: z.string(),
  occurred_at: z.string(), // RFC3339
  source_ref: z.string().optional(),
});

export const EmployeeLiveActivityV1Schema = z
  .object({
    schema_version: z.string(),
    workspace_id: z.string(),
    employee_id: z.string(),
    agent_id: z.string(),
    display_name: z.string(),
    avatar_url: z.string().optional(),
    department_id: z.string().optional(),
    department_name: z.string().optional(),
    position_name: z.string().optional(),

    project_id: z.string().optional(),
    project_title: z.string().optional(),
    workflow_instance_id: z.string().optional(),
    workflow_title: z.string().optional(),
    issue_id: z.string().optional(),
    issue_identifier: z.string().optional(),
    issue_title: z.string().optional(),
    task_id: z.string().optional(),
    run_id: z.string().optional(),

    presence_state: PresenceStateSchema,
    work_stage: WorkStageSchema,
    activity_kind: z.string().optional(),
    activity_summary: z.string().optional(),
    recent_events: z.array(RecentEventSchema),

    base_id: z.string().optional(),
    base_name: z.string().optional(),
    runtime_id: z.string().optional(),
    runtime_provider: z.string().optional(),
    model_name: z.string().optional(),

    queued_at: z.string().optional(),
    started_at: z.string().optional(),
    last_heartbeat_at: z.string().optional(),
    last_event_at: z.string().optional(),
    completed_at: z.string().optional(),

    token_usage: z.number().int().optional(),
    cost_amount: z.number().optional(),
    blocked_reason: z.string().optional(),
    next_action: z.string().optional(),

    source_refs: z.array(z.string()),
    observed_at: z.string(),
    freshness_state: FreshnessStateSchema,
  })
  .strict();

export type RecentEvent = z.infer<typeof RecentEventSchema>;
export type EmployeeLiveActivityV1 = z.infer<typeof EmployeeLiveActivityV1Schema>;
export type PresenceState = z.infer<typeof PresenceStateSchema>;
export type WorkStage = z.infer<typeof WorkStageSchema>;

export function parseWorkWallSnapshot(input: unknown): EmployeeLiveActivityV1[] {
  return z.array(EmployeeLiveActivityV1Schema).parse(input);
}

// Terminal presence: read-only projection of live host terminal panes,
// upserted by the host-side collector (10s heartbeat, 15min freshness).
export const TerminalPaneSchema = z
  .object({
    host: z.string(),
    session_name: z.string(),
    window_index: z.number().int(),
    pane_index: z.number().int(),
    current_command: z.string(),
    agent_hint: z.string(),
    tail_text: z.string(),
    heartbeat_at: z.string(),
  })
  .strict();

export type TerminalPane = z.infer<typeof TerminalPaneSchema>;
