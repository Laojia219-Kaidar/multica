export const COMPANY_OPS_WORK_CONTEXT_SCHEMA_VERSION =
  "hivecrew.owner-work-context.v1" as const;

export const COMPANY_OPS_ASSIGNMENT_DISPATCH_SCHEMA_VERSION =
  "hivecrew.assignment-dispatch.v1" as const;

export const COMPANY_OPS_ARTIFACT_REVIEW_SCHEMA_VERSION =
  "hivecrew.artifact-review.v1" as const;

export const COMPANY_OPS_FORMAL_ARTIFACT_PROMOTION_SCHEMA_VERSION =
  "hivecrew.formal-artifact-promotion.v1" as const;

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
