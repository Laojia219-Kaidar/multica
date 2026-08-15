// Universal Work Registration Kernel — frontend wire types.
//
// Mirrors the Phase-1 backend contracts in server/internal/workentry/*.go and
// the WORK-ACTOR-CONTRACT.md §2/§4 field shapes. This file only carries
// read-model + request/response types; the project operating-center panels
// live in packages/views/projects/components and the query/mutation layer in
// packages/core/work-entry/.
//
// NOTE: `WorkInboxItem` intentionally uses the Go default JSON field names
// (`ID` / `WorkspaceID` / `WorkRef`) because server/internal/workentry/
// store.go's InboxItem struct carries no `json:"..."` tags — encoding/json
// serializes exported struct fields verbatim. Keep these keys in sync with
// the backend until a contract version bump renames them.

/** Closed five-value actor enumeration (WORK-ACTOR-CONTRACT §2). */
export type WorkActorType =
  | "registered_employee"
  | "external_agent"
  | "human_operator"
  | "automation_service"
  | "observed_unclaimed_actor";

/**
 * Unclaimed work inbox entry returned by GET /api/work/reconcile. These are
 * observed-but-unattributed development actions awaiting attach/ignore. They
 * never advance any project ledger until attached (VC-05).
 */
export interface WorkInboxItem {
  ID: string;
  WorkspaceID: string;
  WorkRef: string;
}

/** POST /api/work/attach — link an unclaimed entry to an existing project/issue. */
export interface WorkAttachRequest {
  inbox_id: string;
  project_id?: string;
  issue_id?: string;
}

export interface WorkAttachResult {
  linked: boolean;
  work_ref?: string;
}

/** POST /api/work/ignore — mark an unclaimed entry as ignored. */
export interface WorkIgnoreRequest {
  inbox_id: string;
  reason?: string;
}

export interface WorkIgnoreResult {
  ignored: boolean;
}

/** GET /api/work/status — read-only status projection for one work_ref. */
export interface WorkStatusResult {
  work_ref: string;
  found: boolean;
  project_id?: string;
  issue_id?: string;
  task_id?: string;
  resolution_decision?: "created" | "continued" | "classification_required";
}

// ---------------------------------------------------------------------------
// CompanyOps workforce-base-runtime read model (GET /api/company-ops/
// workforce-base-runtime). One row per employee: the strict Employee → Agent →
// Runtime → Base (physical machine) join. This is the currently-deployed
// read model the participant panel reuses while the project-scoped aggregation
// endpoint is pending.
// ---------------------------------------------------------------------------

export const WORKFORCE_BASE_RUNTIME_SCHEMA_VERSION =
  "hivecrew.workforce-base-runtime.v1" as const;

export interface WorkforceBaseRuntimeRow {
  employee_id: string;
  workforce_agent_id: string;
  hivecrew_agent_id?: string;
  runtime_id?: string;
  base_machine_title?: string;
  agent_status?: string;
  runtime_status?: string;
  model?: string;
}

export interface WorkforceBaseRuntimeResponse {
  schema_version: typeof WORKFORCE_BASE_RUNTIME_SCHEMA_VERSION;
  workspace_id: string;
  authority: unknown;
  items: WorkforceBaseRuntimeRow[];
}

// ---------------------------------------------------------------------------
// Project participant / executor read model (VC-04).
//
// The target shape renders actor_type / employee_id / carrier / runtime /
// model / base / host / session / next_action for one project. The full
// project-scoped aggregation endpoint is NOT deployed yet; the participant
// panel currently derives the employee subset from the workforce-base-runtime
// read model and marks the remaining dimensions as pending.
// ---------------------------------------------------------------------------

export interface WorkParticipant {
  actor_type: WorkActorType;
  actor_id: string;
  /** Present only for registered_employee; external agents never show a DE-*. */
  employee_id?: string;
  carrier_id?: string;
  runtime_id?: string;
  model_ref?: string;
  base_id?: string;
  host_id?: string;
  session_id?: string;
  next_action?: string;
}

export type ProjectParticipantsSource =
  | "workforce_base_runtime"
  | "work_entry_participants";

export interface ProjectParticipantsData {
  source: ProjectParticipantsSource;
  /** True while only the employee-centric workforce read model is available. */
  pending_project_scope: boolean;
  participants: WorkParticipant[];
}
