export type WorkConservingProjectionState = "ready" | "blocked" | "source_gap";

export interface WorkConservingAuthoritySnapshot {
  workspaceId: string;
  projectId: string;
  sourceRef: string;
  revision: string;
  observedAt: string;
  expiresAt: string;
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
