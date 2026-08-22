export type WorkConservingProjectionState = "ready" | "blocked" | "source_gap";

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
  suggestions: WorkConservingSuggestion[];
  blockedBacklog: WorkConservingBlockedIssue[];
  mismatch: WorkConservingMismatch;
  total: number;
  limit: number;
  offset: number;
  noWrite: true;
}
