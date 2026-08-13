export const COMPANY_OPS_WORK_CONTEXT_SCHEMA_VERSION =
  "hivecrew.owner-work-context.v1" as const;

export const COMPANY_OPS_ASSIGNMENT_DISPATCH_SCHEMA_VERSION =
  "hivecrew.assignment-dispatch.v1" as const;

export const COMPANY_OPS_ARTIFACT_REVIEW_SCHEMA_VERSION =
  "hivecrew.artifact-review.v1" as const;

export const COMPANY_OPS_FORMAL_ARTIFACT_PROMOTION_SCHEMA_VERSION =
  "hivecrew.formal-artifact-promotion.v1" as const;

export const COMPANY_OPS_OUTCOME_CENTER_SCHEMA_VERSION =
  "hivecrew.outcome-center.v1" as const;

export const COMPANY_OPS_ORGANIZATION_SCHEMA_VERSION =
  "hivecrew.organization.v1" as const;

export interface CompanyOpsWorkContextRequest {
  work_order_source_ref: string;
  employee_id: string;
  identity_binding_id: string;
  agent_id: string;
  session_id: string;
}

export interface CompanyOpsAuthoritySnapshot {
  kind: string;
  source_ref: string;
  revision: string;
  content_digest: string;
  freshness: string;
  display_name?: string;
}

export interface CompanyOpsEmployeeAuthority {
  employee_id: string;
  authority: CompanyOpsAuthoritySnapshot;
}

export interface CompanyOpsIdentityBindingAuthority {
  identity_binding_id: string;
  employee_ref: string;
  agent_ref: string;
  active: boolean;
  authority: CompanyOpsAuthoritySnapshot;
}

export interface CompanyOpsAgentAuthority {
  id: string;
  name: string;
  status: string;
  runtime_mode: string;
  model?: string;
  authority: CompanyOpsAuthoritySnapshot;
}

export interface CompanyOpsSessionAuthority {
  id: string;
  agent_id: string;
  status: string;
}

export interface CompanyOpsIssueProjection {
  id: string;
  number: number;
  title: string;
  status: string;
  assignee_id?: string | null;
}

export interface CompanyOpsOwnerWorkContext {
  schema_version: typeof COMPANY_OPS_WORK_CONTEXT_SCHEMA_VERSION;
  request: CompanyOpsWorkContextRequest;
  work_order: CompanyOpsAuthoritySnapshot;
  employee: CompanyOpsEmployeeAuthority;
  identity_binding: CompanyOpsIdentityBindingAuthority;
  agent: CompanyOpsAgentAuthority;
  session: CompanyOpsSessionAuthority;
  issue: CompanyOpsIssueProjection | null;
  projection_state: "not_projected" | "projected";
  observed_at: string;
  outcome?: CompanyOpsArtifactOutcome;
}

export interface CompanyOpsArtifactOutcome {
  command_id: string;
  issue_id: string;
  initial_task_id: string;
  current_task_id: string;
  execution_state: "awaiting_claim" | "running" | "completed" | "failed" | "cancelled";
  artifact?: {
    id: string;
    revision: number;
    durable_object_ref: string;
    digest: string;
    status: "submitted" | "changes_requested" | "approved" | "promotion_requested" | "promotion_succeeded" | "promotion_failed" | "authority_readback_confirmed";
    formal_visible: boolean;
    formal_artifact_ref?: string;
  };
}

export interface CompanyOpsAssignmentCommand
  extends CompanyOpsWorkContextRequest {
  command_id: string;
  handoff_note: string;
}

export interface CompanyOpsAssignmentDispatchReceipt {
  schema_version: typeof COMPANY_OPS_ASSIGNMENT_DISPATCH_SCHEMA_VERSION;
  command_id: string;
  issue_id: string;
  initial_task_id: string;
  execution_receipt: {
    state: "awaiting_claim";
    task_id: string;
  };
  created_at?: string;
}

export interface CompanyOpsArtifactReviewCommand
  extends CompanyOpsWorkContextRequest {
  review_id: string;
  candidate_id: string;
  decision: "changes_requested" | "approved";
  feedback: string;
}

export interface CompanyOpsArtifactReviewReceipt {
  schema_version: typeof COMPANY_OPS_ARTIFACT_REVIEW_SCHEMA_VERSION;
  review_id: string;
  event_id: string;
  sequence: number;
  decision: "changes_requested" | "approved";
  candidate_id: string;
  rework_task_id?: string;
}

export interface CompanyOpsArtifactPromotionCommand
  extends CompanyOpsWorkContextRequest {
  promotion_id: string;
  candidate_id: string;
}

export type CompanyOpsArtifactPromotionLifecycleStatus =
  | "promotion_requested"
  | "promotion_succeeded"
  | "promotion_failed"
  | "authority_readback_confirmed";

export interface CompanyOpsArtifactPromotionReceipt {
  schema_version: typeof COMPANY_OPS_FORMAL_ARTIFACT_PROMOTION_SCHEMA_VERSION;
  promotion_id: string;
  candidate_id: string;
  lifecycle_status: CompanyOpsArtifactPromotionLifecycleStatus;
  formal_visible: boolean;
  formal_artifact_ref?: string;
  write_performed: boolean;
  event_id: string;
  sequence: number;
}

// ---------------------------------------------------------------------------
// Outcome Center — hivecrew.outcome-center.v1
//
// The Outcome Center is the owner-facing discovery/operating surface over the
// company-ops execution graph. Every Outcome's summary `id` is the canonical
// command_id the assignment writer returned. The list endpoint returns
// summary rows under `items`; the detail endpoint returns the full record with
// summary, versions, events and runs. The review/promotion writers consume
// selectors derived from the summary's WorkOrder / Employee / IdentityBinding
// / ExecutionTarget authorities plus an explicitly selected session_id — never
// a guessed one.
// ---------------------------------------------------------------------------

export interface CompanyOpsOutcomeListParams {
  q?: string;
  status?: string;
  agent_id?: string;
  project_id?: string;
  formal_visible?: boolean;
  limit?: number;
  offset?: number;
  /** Opaque keyset cursor returned by a previous page's next_cursor. */
  cursor?: string;
}

export interface CompanyOpsOutcomeIssueRef {
  id: string;
  number: number;
  identifier: string;
  title: string;
  status: string;
  project_id?: string;
}

export interface CompanyOpsOutcomeWorkOrderRef {
  source_ref: string;
  revision: string;
  digest: string;
}

export interface CompanyOpsOutcomeEmployeeRef {
  source_ref: string;
  id: string;
}

export interface CompanyOpsOutcomeIdentityBindingRef {
  source_ref: string;
  id: string;
}

export interface CompanyOpsOutcomeExecutionTarget {
  local_agent_id: string;
  agent_ref: string;
  agent_revision: string;
  agent_digest: string;
}

export interface CompanyOpsOutcomeAgentDisplay {
  name: string;
  model?: string;
  status: string;
}

export interface CompanyOpsOutcomeActiveArtifact {
  id: string;
  revision: number;
  durable_object_ref: string;
  digest: string;
  content_type?: string;
  status: string;
  formal_visible: boolean;
  formal_artifact_ref?: string;
}

/** Compact row returned by the list endpoint and embedded in the detail. */
export interface CompanyOpsOutcomeSummary {
  id: string;
  issue: CompanyOpsOutcomeIssueRef | null;
  work_order: CompanyOpsOutcomeWorkOrderRef;
  employee: CompanyOpsOutcomeEmployeeRef;
  identity_binding: CompanyOpsOutcomeIdentityBindingRef;
  execution_target: CompanyOpsOutcomeExecutionTarget;
  current_agent_display: CompanyOpsOutcomeAgentDisplay;
  initial_task_id: string;
  current_task_id: string;
  execution_state: string;
  active_artifact?: CompanyOpsOutcomeActiveArtifact;
  version_count: number;
  latest_event_at?: string;
}

export interface CompanyOpsOutcomeVersion {
  id: string;
  revision: number;
  supersedes_id?: string;
  durable_object_ref: string;
  digest: string;
  content_type?: string;
}

export interface CompanyOpsOutcomeEvent {
  id: string;
  sequence: number;
  type: string;
  candidate_id: string;
  candidate_revision: number;
  formal_artifact_ref?: string;
}

export interface CompanyOpsOutcomeRun {
  task_id: string;
  status: string;
  completed_at?: string;
  output_digest?: string;
  terminal_error?: string;
}

export interface CompanyOpsOutcomeDetail {
  schema_version: typeof COMPANY_OPS_OUTCOME_CENTER_SCHEMA_VERSION;
  summary: CompanyOpsOutcomeSummary;
  versions: CompanyOpsOutcomeVersion[];
  events: CompanyOpsOutcomeEvent[];
  runs: CompanyOpsOutcomeRun[];
}

export interface CompanyOpsOutcomeListResponse {
  schema_version: typeof COMPANY_OPS_OUTCOME_CENTER_SCHEMA_VERSION;
  items: CompanyOpsOutcomeSummary[];
  total: number;
  limit: number;
  offset: number;
  /** Opaque keyset token for the next (older) page; null on the last page. */
  next_cursor?: string | null;
  /** True when more rows exist beyond this page. Optional so older servers and
   *  cached mocks without the field still parse. */
  has_more?: boolean;
}

// ---------------------------------------------------------------------------
// Organization & Employees roster/dossier — hivecrew.organization.v1
//
// Company-identity projection consumed by the Organization entry (three-level
// surface: Organization tree → Employees roster → Employee dossier). The
// authority wire is strictly three-namespace: `employee_id` is an opaque
// company employee id (DE-*), `workforce_agent_id` is the workforce agent id
// from the authority projection (KT-*/EXT-*), and `hivecrew_agent_id` is the
// canonical UUID of the local execution agent — never derived from each other.
//
// Every roster row / dossier carries a `binding_state` computed by the
// backend across the authority IdentityBinding and the local agent
// registry. Only `available` may enable Agent settings / Chat / assignment
// links; every other state is fail-closed (non-operable + explainable).
//
// The org tree is read-only: Department → Position → Appointment, all backed
// by an authority snapshot. It never borrows Member/Squad/Agent collections.
// ---------------------------------------------------------------------------

export type CompanyOpsBindingState =
  | "available"
  | "none"
  | "inactive_only"
  | "multiple_active_conflict"
  | "local_agent_missing_or_invalid"
  | "source_gap";

export interface CompanyOpsDepartmentAppointment {
  appointment_id: string;
  employee_id: string;
  authority: CompanyOpsAuthoritySnapshot;
}

export interface CompanyOpsDepartmentPosition {
  position_id: string;
  title: string;
  appointments: CompanyOpsDepartmentAppointment[];
  authority: CompanyOpsAuthoritySnapshot;
}

export interface CompanyOpsDepartmentNode {
  department_id: string;
  name: string;
  positions: CompanyOpsDepartmentPosition[];
  authority: CompanyOpsAuthoritySnapshot;
}

export interface CompanyOpsOrganization {
  schema_version: typeof COMPANY_OPS_ORGANIZATION_SCHEMA_VERSION;
  departments: CompanyOpsDepartmentNode[];
  observed_at: string;
}

export interface CompanyOpsRosterParams {
  q?: string;
  status?: string;
  limit?: number;
  offset?: number;
}

export interface CompanyOpsRosterItem {
  employee_id: string;
  display_name?: string;
  department_ref?: string;
  position_ref?: string;
  workforce_agent_id: string | null;
  hivecrew_agent_id: string | null;
  binding_state: CompanyOpsBindingState;
  authority: CompanyOpsAuthoritySnapshot;
}

export interface CompanyOpsRosterResponse {
  schema_version: typeof COMPANY_OPS_ORGANIZATION_SCHEMA_VERSION;
  items: CompanyOpsRosterItem[];
  total: number;
  limit: number;
  offset: number;
}

export interface CompanyOpsEmployeeBinding {
  identity_binding_id: string;
  employee_ref: string;
  workforce_agent_id: string;
  agent_ref: string;
  active: boolean;
  authority: CompanyOpsAuthoritySnapshot;
}

export interface CompanyOpsEmployeeLocalAgent {
  id: string;
  name: string;
  status: string;
  runtime_mode: string;
  model?: string;
  authority: CompanyOpsAuthoritySnapshot;
}

export interface CompanyOpsEmployeeDossier {
  schema_version: typeof COMPANY_OPS_ORGANIZATION_SCHEMA_VERSION;
  employee_id: string;
  display_name?: string;
  department_ref?: string;
  position_ref?: string;
  workforce_agent_id: string | null;
  hivecrew_agent_id: string | null;
  bindings: CompanyOpsEmployeeBinding[];
  binding_state: CompanyOpsBindingState;
  local_agent: CompanyOpsEmployeeLocalAgent | null;
  authority: CompanyOpsAuthoritySnapshot;
}
