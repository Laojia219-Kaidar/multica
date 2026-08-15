package continuousdispatch

import "strings"

// ReviewOrphanRecoveryState is the fail-closed outcome of evaluating a
// completed repair that has no confirmed open review task. It is a pure
// decision value; it does not claim that a Task or receipt was written.
type ReviewOrphanRecoveryState string

const (
	ReviewOrphanReady            ReviewOrphanRecoveryState = "ready"
	ReviewOrphanDeferredCapacity ReviewOrphanRecoveryState = "deferred_capacity"
	ReviewOrphanDeferredRepair   ReviewOrphanRecoveryState = "deferred_repair"
	ReviewOrphanAlreadyConfirmed ReviewOrphanRecoveryState = "already_confirmed"
	ReviewOrphanBlocked          ReviewOrphanRecoveryState = "blocked"
)

const (
	ReviewStateReviseRequested = "revise_requested"
	TaskKindRepair             = "repair"
	TaskKindReview             = "review"
	TaskStatusCompleted        = "completed"
	TaskStatusQueued           = "queued"
	TaskStatusDispatched       = "dispatched"
	TaskStatusRunning          = "running"
	TaskStatusWaitingLocal     = "waiting_local_directory"
)

// ReviewOrphanIdentity is the immutable identity that must remain unchanged
// from the repaired candidate through its re-review. A missing field is an
// evidence gap, not an invitation to infer from an Issue title or Task order.
type ReviewOrphanIdentity struct {
	WorkspaceID       string
	IssueID           string
	CandidateRevision string
	Generation        string
}

func (i ReviewOrphanIdentity) Complete() bool {
	return identifiersCanonical(i.WorkspaceID, i.IssueID, i.CandidateRevision, i.Generation)
}

// ReviewOrphanTask is the minimum read model required by the recovery
// decision. The future database adapter must populate it from canonical Task
// rows; this package deliberately does not know how to write or query them.
type ReviewOrphanTask struct {
	ID                string
	Kind              string
	Status            string
	WorkspaceID       string
	IssueID           string
	CandidateRevision string
	Generation        string
	AgentID           string
	TargetTaskID      string
}

// ReviewOpenTaskEvidence is a postcondition readback. Created/reused is not
// enough: a caller may mark an orphan processed only after this exact open
// review Task is read back from the canonical store.
type ReviewOpenTaskEvidence struct {
	Known      bool
	Found      bool
	Task       ReviewOrphanTask
	ReviewerID string
}

// ReviewOrphanRecoveryInput is intentionally a snapshot, not a persistence
// command. Capacity is considered available only when its source is known,
// reconciled, and active WIP is strictly below the configured limit.
type ReviewOrphanRecoveryInput struct {
	ReviewState        string
	RepairTask         ReviewOrphanTask
	OpenReview         ReviewOpenTaskEvidence
	Identity           ReviewOrphanIdentity
	CapacityKnown      bool
	CapacityReconciled bool
	ActiveReviewWIP    int
	MaxReviewWIP       int
	ReviewerID         string
}

// ReviewOrphanRecoveryDecision is the only output of EvaluateReviewOrphan.
// Retryable means a later reconciliation may evaluate the same canonical row
// again; Processed is true only when the open-review postcondition is proven.
type ReviewOrphanRecoveryDecision struct {
	State          ReviewOrphanRecoveryState
	Retryable      bool
	Processed      bool
	ReviewerID     string
	ReviewTargetID string
	Reasons        []string
}

// EvaluateReviewOrphan decides whether a completed repair can be re-entered
// into independent review. It has no side effects and must remain suitable
// for use by a future ReviewCell/Drain adapter without inventing a database
// state or scheduler.
func EvaluateReviewOrphan(in ReviewOrphanRecoveryInput) ReviewOrphanRecoveryDecision {
	if in.ReviewState != ReviewStateReviseRequested {
		return blockedDecision("review_state_not_revise_requested")
	}
	if !in.Identity.Complete() {
		return blockedDecision("candidate_identity_incomplete")
	}
	if in.OpenReview.Known && in.OpenReview.Found {
		if validConfirmedOpenReview(in) {
			return ReviewOrphanRecoveryDecision{
				State:          ReviewOrphanAlreadyConfirmed,
				Processed:      true,
				ReviewerID:     in.OpenReview.ReviewerID,
				ReviewTargetID: in.OpenReview.Task.TargetTaskID,
				Reasons:        []string{"open_review_task_readback_confirmed"},
			}
		}
		return blockedDecision("open_review_task_readback_invalid")
	}
	if !in.OpenReview.Known {
		return blockedDecision("open_review_task_readback_unknown")
	}
	if !validCompletedRepair(in) {
		if in.RepairTask.Status != TaskStatusCompleted {
			return ReviewOrphanRecoveryDecision{
				State:     ReviewOrphanDeferredRepair,
				Retryable: true,
				Reasons:   []string{"repair_task_not_completed"},
			}
		}
		return blockedDecision("completed_repair_identity_mismatch")
	}
	reviewerID, reviewerOK := canonicalIdentifier(in.ReviewerID)
	if !reviewerOK {
		return blockedDecision("reviewer_identity_missing")
	}
	repairAuthorID, repairAuthorOK := canonicalIdentifier(in.RepairTask.AgentID)
	if !repairAuthorOK {
		return blockedDecision("completed_repair_identity_mismatch")
	}
	if reviewerID == repairAuthorID {
		return blockedDecision("reviewer_is_repair_author")
	}
	if !in.CapacityKnown || !in.CapacityReconciled {
		return ReviewOrphanRecoveryDecision{
			State:     ReviewOrphanDeferredCapacity,
			Retryable: true,
			Reasons:   []string{"review_capacity_evidence_unavailable"},
		}
	}
	if in.MaxReviewWIP <= 0 || in.ActiveReviewWIP < 0 || in.ActiveReviewWIP >= in.MaxReviewWIP {
		return ReviewOrphanRecoveryDecision{
			State:     ReviewOrphanDeferredCapacity,
			Retryable: true,
			Reasons:   []string{"review_capacity_exhausted"},
		}
	}
	return ReviewOrphanRecoveryDecision{
		State:          ReviewOrphanReady,
		Retryable:      true,
		ReviewerID:     reviewerID,
		ReviewTargetID: in.RepairTask.ID,
		Reasons:        []string{"completed_repair_ready_for_independent_review"},
	}
}

func validCompletedRepair(in ReviewOrphanRecoveryInput) bool {
	task := in.RepairTask
	return task.Kind == TaskKindRepair && task.Status == TaskStatusCompleted &&
		identifiersCanonical(task.ID, task.WorkspaceID, task.IssueID, task.CandidateRevision, task.Generation, task.AgentID) &&
		task.WorkspaceID == in.Identity.WorkspaceID &&
		task.IssueID == in.Identity.IssueID &&
		task.CandidateRevision == in.Identity.CandidateRevision &&
		task.Generation == in.Identity.Generation
}

func validConfirmedOpenReview(in ReviewOrphanRecoveryInput) bool {
	task := in.OpenReview.Task
	requestedReviewerID, requestedReviewerOK := canonicalIdentifier(in.ReviewerID)
	evidenceReviewerID, evidenceReviewerOK := canonicalIdentifier(in.OpenReview.ReviewerID)
	taskReviewerID, taskReviewerOK := canonicalIdentifier(task.AgentID)
	repairAuthorID, repairAuthorOK := canonicalIdentifier(in.RepairTask.AgentID)
	return validCompletedRepair(in) && requestedReviewerOK && evidenceReviewerOK && taskReviewerOK && repairAuthorOK &&
		requestedReviewerID == evidenceReviewerID && taskReviewerID == evidenceReviewerID && taskReviewerID != repairAuthorID &&
		task.Kind == TaskKindReview && identifiersCanonical(task.ID, task.WorkspaceID, task.IssueID, task.CandidateRevision, task.Generation) && isOpenReviewStatus(task.Status) &&
		task.WorkspaceID == in.Identity.WorkspaceID && task.IssueID == in.Identity.IssueID &&
		task.CandidateRevision == in.Identity.CandidateRevision && task.Generation == in.Identity.Generation &&
		task.TargetTaskID == in.RepairTask.ID
}

// canonicalIdentifier rejects missing and padded dispatch identities. These
// values are canonical task/authority identifiers, not display strings:
// silently trimming them would let a padded reviewer bypass identity checks.
func canonicalIdentifier(raw string) (string, bool) {
	canonical := strings.TrimSpace(raw)
	return canonical, canonical != "" && raw == canonical
}

func identifiersCanonical(values ...string) bool {
	for _, value := range values {
		if _, ok := canonicalIdentifier(value); !ok {
			return false
		}
	}
	return true
}

func isOpenReviewStatus(status string) bool {
	switch status {
	case TaskStatusQueued, TaskStatusDispatched, TaskStatusRunning, TaskStatusWaitingLocal:
		return true
	default:
		return false
	}
}

func blockedDecision(reason string) ReviewOrphanRecoveryDecision {
	return ReviewOrphanRecoveryDecision{State: ReviewOrphanBlocked, Reasons: []string{reason}}
}
