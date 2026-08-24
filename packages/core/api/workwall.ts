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

// A2 "CEO 工作现场快照协议 (hivecrew.workwall.a2-snapshot.v1)
// Frozen backend contract: GET /api/work-wall/a2/snapshot
// Strict zod: unknown keys rejected, enums exact.

export const A2PaneKindSchema = z.enum([
  "terminal",
  "event_console",
]);

export type A2PaneKind = z.infer<typeof A2PaneKindSchema>;

export const A2PaneSchema = z
  .object({
    schema_version: z.literal("hivecrew.workwall.a2-pane.v1"),
    pane_id: z.string(),
    employee_id: z.string(),
    session_id: z.string(),
    kind: A2PaneKindSchema,
    display_name: z.string(),
    position_name: z.string().optional(),
    department_name: z.string().optional(),
    avatar_url: z.string().optional(),
    presence_state: PresenceStateSchema,
    work_stage: WorkStageSchema,
    activity_summary: z.string().optional(),
    issue_identifier: z.string().optional(),
    issue_title: z.string().optional(),
    model_name: z.string().optional(),
    runtime_provider: z.string().optional(),
    tail_text: z.string(),
    last_event_at: z.string().optional(), // RFC3339
    observed_at: z.string(), // RFC3339
    freshness_state: FreshnessStateSchema,
  })
  .strict();

export type A2Pane = z.infer<typeof A2PaneSchema>;

export const A2SnapshotSchema = z
  .object({
    schema_version: z.literal("hivecrew.workwall.a2-snapshot.v1"),
    workspace_id: z.string(),
    cursor: z.string(),
    observed_at: z.string(), // RFC3339
    event_limit: z.number().int().nonnegative(),
    panes: z.array(A2PaneSchema),
  })
  .strict();

export type A2Snapshot = z.infer<typeof A2SnapshotSchema>;

export function parseA2WorkWallSnapshot(input: unknown): A2Snapshot {
  return A2SnapshotSchema.parse(input);
}

// Join A2 panes onto an employee roster (primary = employee list).
// For each employee, pick the newest pane (by observed_at desc).
// terminal and event_console are mutually exclusive per employee:
// when both exist, terminal wins (it is the richer signal).
// Returns { matched: pane joined by employee_id, unmatchedCount: panes with no employee match }.
export interface A2JoinedResult {
  matched: Map<string, A2Pane>;
  unmatchedCount: number;
}

export function joinA2PanesByEmployee(
  employees: Array<{ employee_id: string }>,
  panes: A2Pane[],
): A2JoinedResult {
  // Group panes by employee_id, keeping newest (by observed_at) per kind,
  // with terminal > event_console precedence.
  const byEmployee = new Map<string, A2Pane>();
  for (const pane of panes) {
    const existing = byEmployee.get(pane.employee_id);
    if (!existing) {
      byEmployee.set(pane.employee_id, pane);
      continue;
    }
    // terminal wins over event_console
    if (existing.kind === "event_console" && pane.kind === "terminal") {
      byEmployee.set(pane.employee_id, pane);
      continue;
    }
    if (existing.kind === pane.kind &&
        new Date(pane.observed_at) > new Date(existing.observed_at)) {
      byEmployee.set(pane.employee_id, pane);
    }
  }

  const matched = new Map<string, A2Pane>();
  let unmatchedCount = 0;

  for (const emp of employees) {
    const pane = byEmployee.get(emp.employee_id);
    if (pane) matched.set(emp.employee_id, pane);
  }

  const employeeIds = new Set(employees.map((e) => e.employee_id));
  for (const pane of panes) {
    if (!employeeIds.has(pane.employee_id)) unmatchedCount++;
  }

  return { matched, unmatchedCount };
}
