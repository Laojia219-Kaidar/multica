/**
 * Pipeline projection types + query (HIV-367 / P0-E).
 *
 * These mirror the server-side wire shape of GET /api/projects/{id}/pipeline
 * (server/internal/handler/pipeline.go). The projection is a single read-only
 * composition of Project + Issue + agent_task_queue + comment — no second
 * pipeline-status table, no new writers.
 *
 * The frontend uses it to render real processing state on the project board:
 *   - Column headers carry per-status breakdowns (running / queued / waiting /
 *     failed / terminal / no_task / unknown), satisfying contract §2.
 *   - Each card looks up its per-issue row to render the latest Task ID +
 *     status, run duration, wait/failure reason, last task-linked verdict/
 *     receipt, update time, and the explicit processing-state marker that
 *     contract §4 requires ("停滞/待重新派工" etc.).
 *   - CapabilityFlags gate canonical actions: any command the server does not
 *     actually wire today (HIV-355 dispatch-preview, HIV-357 project start)
 *     must render "能力待接入", never a fabricated local mutation (§6).
 */

/** Task classification — mirrors server pipelineTaskClass* constants. */
export type PipelineTaskClass =
  | "running"
  | "queued"
  | "waiting_local_directory"
  | "failed"
  | "terminal"
  | "no_task"
  | "unknown";

/**
 * Explicit per-card processing-state marker (contract §4). Stable wire strings
 * the i18n layer localizes — never silently coerce to "active".
 */
export type PipelineProcessingState =
  /** in_progress with no open Task — "停滞/待重新派工" */
  | "stale_awaiting_dispatch"
  /** in_review with no Review Task — "未进入审核执行" */
  | "review_not_started"
  /** blocked with no recovery Task — "阻塞未处理" */
  | "blocked_unhandled"
  /** Healthy: has an active or recently-terminal task; issue still in flight */
  | "active"
  /** Honest fallback — never silently fabricate a state. */
  | "unknown";

/** Per-status column header payload (contract §2). */
export interface ProjectPipelineColumn {
  status: string;
  total: number;
  running: number;
  queued: number;
  waiting: number;
  failed: number;
  terminal: number;
  /** terminal task with NO task-linked comment (no verdict/receipt writeback). */
  terminal_no_writeback: number;
  no_task: number;
  unknown: number;
}

/** Per-card pipeline payload (contract §3, §4). */
export interface ProjectPipelineIssue {
  issue_id: string;
  status: string;
  priority: string;
  title: string;
  assignee_type?: string;
  assignee_id?: string;
  updated_at: string;
  /** "" when the issue has no agent_task_queue row at all. */
  task_id?: string;
  task_status?: string;
  task_class: PipelineTaskClass;
  task_dispatched_at?: string | null;
  task_started_at?: string | null;
  task_completed_at?: string | null;
  /** Run duration in milliseconds; 0 when not derivable. */
  task_duration_ms?: number;
  failure_reason?: string;
  wait_reason?: string;
  /** Latest task-linked comment (comment.source_task_id IS NOT NULL). */
  latest_receipt_comment_id?: string;
  latest_receipt_comment_at?: string | null;
  latest_receipt_comment_snippet?: string;
  /** Stable, client-localizable label; "" when there is no canonical action. */
  next_system_action?: string;
  processing_state: PipelineProcessingState;
}

/** Capability flags — front-end MUST render "能力待接入" when false (§6). */
export interface ProjectPipelineCapabilityFlags {
  cancel_task: boolean;
  rerun_issue: boolean;
  update_status: boolean;
  /** HIV-355 — not yet merged in this mainline. */
  dispatch_preview: boolean;
  /** HIV-355 — not yet merged in this mainline. */
  dispatch: boolean;
  /** HIV-357 — not yet merged in this mainline. */
  project_start: boolean;
}

/** Wire shape of GET /api/projects/{id}/pipeline. */
export interface ProjectPipelineResponse {
  project_id: string;
  project_status: string;
  project_title: string;
  updated_at: string;
  columns: Record<string, ProjectPipelineColumn>;
  /** Flat map keyed by issue id so the board can join per-card data directly. */
  issues: Record<string, ProjectPipelineIssue>;
  capability_flags: ProjectPipelineCapabilityFlags;
}
