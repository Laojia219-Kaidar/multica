export type WorkConservingProjectionState = "ready" | "blocked" | "source_gap";

/**
 * Frozen, sanitized classification of the HiveCosm organization (workforce
 * authority) source, read ONLY from the top-level
 * `sources.organization_source_state` wire field. The value is a wire-stable
 * enum; it never carries a URL, tenant value, token source name, credential
 * reference, raw response, raw error or log content. `null` means the strict
 * parser did not observe a valid field (missing, unknown, malformed, or the
 * query itself failed): the UI must render only the existing generic
 * source-gap treatment and must never synthesize a specific state.
 */
export type OrganizationSourceState =
  | "base_missing"
  | "base_invalid"
  | "token_unavailable"
  | "tenant_missing"
  | "directory_constructor_error"
  | "directory_request_error"
  | "empty_authoritative_workforce"
  | "healthy";

export interface WorkConservingAuthoritySnapshot {
  workspaceId: string;
  projectId: string;
  sourceRef: string;
  revision: string;
  observedAt: string;
  expiresAt: string;
}

export interface ContinuousDispatchIdentity {
  workspaceId: string;
  issueId: string;
  stage: string;
  candidateRevision: string;
  generation: string;
}

export interface ContinuousDispatchReviewProvenance {
  sourceRef: string;
  sourceIssueId: string;
  sourceTaskId: string;
  initiatorSource: string;
}

export interface ContinuousDispatchReceipt {
  identity: ContinuousDispatchIdentity;
  taskId: string;
  employeeRef: string;
  localAgentId: string;
  runtimeId: string;
  model: string;
  accountRef: string;
  requestDigest: string;
  reviewProvenance?: ContinuousDispatchReviewProvenance;
}

export type WorkConservingDrainState = "ready" | "source_gap";
export type WorkConservingDrainOutcome =
  | "dispatched"
  | "already_terminal"
  | "blocked"
  | "conflict"
  | "source_gap";

export interface WorkConservingDrainIssueResult {
  issueId: string;
  goalId?: string;
  employeeId?: string;
  outcome: WorkConservingDrainOutcome;
  reason?: string;
  receiver?: string;
  wakeCondition?: string;
  receipt?: ContinuousDispatchReceipt;
  notAttempted?: boolean;
}

export interface WorkConservingDrainResult {
  state: WorkConservingDrainState;
  reasonCode?: string;
  projectionState?: WorkConservingProjectionState;
  goalId: string | null;
  authority: WorkConservingAuthoritySnapshot | null;
  batchSize: number;
  results: WorkConservingDrainIssueResult[];
  deferredSuggestions: number;
  dispatched: number;
  alreadyTerminal: number;
  blocked: number;
  conflicts: number;
  sourceGaps: number;
}

export interface WorkConservingSuggestion {
  issueId: string;
  goalId: string;
  employeeId: string;
  agentId: string;
  runtimeId: string;
  baseId?: string;
  score: number;
  fallbackReason?: string;
  receiver: string;
  wakeCondition: string;
}

export interface WorkConservingBlockedIssue {
  issueId: string;
  goalId: string;
  reasons: string[];
  receiver: string;
  wakeCondition: string;
  eligibleEmployeeCount: number;
}

export interface WorkConservingMismatch {
  openIssues: number;
  plannedIssues: number;
  blockedBacklog: number;
  healthyIdleEmployees: number;
  unmatchedHealthyIdleEmployees: number;
  executableBacklog: number;
  idleBacklogMismatch: number;
}

export interface WorkConservingProjection {
  schemaVersion: "hivecrew.work-conserving-projection/v1";
  state: WorkConservingProjectionState;
  reasonCode?: string;
  blocked: boolean;
  goalId: string | null;
  authority: WorkConservingAuthoritySnapshot | null;
  /**
   * Sanitized organization-source classification from the same response's
   * top-level sources block, or null when the strict parser degraded to the
   * generic source-gap state (missing/unknown/malformed/query error).
   */
  organizationSourceState: OrganizationSourceState | null;
  suggestions: WorkConservingSuggestion[];
  blockedBacklog: WorkConservingBlockedIssue[];
  mismatch: WorkConservingMismatch;
  total: number;
  limit: number;
  offset: number;
  noWrite: true;
}
