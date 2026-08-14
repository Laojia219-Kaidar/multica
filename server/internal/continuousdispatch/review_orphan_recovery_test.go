package continuousdispatch

import "testing"

func orphanIdentity() ReviewOrphanIdentity {
	return ReviewOrphanIdentity{WorkspaceID: "ws-1", IssueID: "issue-1", CandidateRevision: "rev-7", Generation: "gen-3"}
}

func completedRepair() ReviewOrphanTask {
	identity := orphanIdentity()
	return ReviewOrphanTask{ID: "repair-1", Kind: TaskKindRepair, Status: TaskStatusCompleted,
		WorkspaceID: identity.WorkspaceID, IssueID: identity.IssueID,
		CandidateRevision: identity.CandidateRevision, Generation: identity.Generation, AgentID: "writer-1"}
}

func availableRecoveryInput() ReviewOrphanRecoveryInput {
	return ReviewOrphanRecoveryInput{
		ReviewState: ReviewStateReviseRequested, RepairTask: completedRepair(), Identity: orphanIdentity(),
		OpenReview: ReviewOpenTaskEvidence{Known: true}, CapacityKnown: true, CapacityReconciled: true,
		ActiveReviewWIP: 0, MaxReviewWIP: 1, ReviewerID: "reviewer-1",
	}
}

func TestEvaluateReviewOrphanReadyAfterCapacityFrees(t *testing.T) {
	decision := EvaluateReviewOrphan(availableRecoveryInput())
	if decision.State != ReviewOrphanReady || !decision.Retryable || decision.Processed {
		t.Fatalf("decision = %+v, want retryable ready but not processed", decision)
	}
	if decision.ReviewTargetID != "repair-1" || decision.ReviewerID != "reviewer-1" {
		t.Fatalf("decision = %+v, want repair target and independent reviewer", decision)
	}
}

func TestEvaluateReviewOrphanCapacityFullRemainsRetryableAndUnprocessed(t *testing.T) {
	in := availableRecoveryInput()
	in.ActiveReviewWIP = 1
	decision := EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanDeferredCapacity || !decision.Retryable || decision.Processed {
		t.Fatalf("decision = %+v, want retryable capacity defer", decision)
	}
}

func TestEvaluateReviewOrphanRepairInFlightRemainsRetryable(t *testing.T) {
	in := availableRecoveryInput()
	in.RepairTask.Status = "running"
	decision := EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanDeferredRepair || !decision.Retryable || decision.Processed {
		t.Fatalf("decision = %+v, want retryable repair defer", decision)
	}
}

func TestEvaluateReviewOrphanRequiresOpenReviewReadbackBeforeProcessed(t *testing.T) {
	in := availableRecoveryInput()
	in.OpenReview.Known = false
	decision := EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanBlocked || decision.Processed || decision.Retryable {
		t.Fatalf("decision = %+v, want blocked unknown readback", decision)
	}

	in = availableRecoveryInput()
	in.OpenReview.Found = true
	in.OpenReview.ReviewerID = "reviewer-1"
	in.OpenReview.Task = ReviewOrphanTask{
		ID: "review-1", Kind: TaskKindReview, Status: TaskStatusQueued,
		WorkspaceID: "ws-1", IssueID: "issue-1", CandidateRevision: "rev-7", Generation: "gen-3",
		TargetTaskID: "repair-1",
	}
	decision = EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanAlreadyConfirmed || !decision.Processed || decision.Retryable {
		t.Fatalf("decision = %+v, want processed only after exact readback", decision)
	}
}

func TestEvaluateReviewOrphanAcceptsEveryOpenReviewStatus(t *testing.T) {
	for _, status := range []string{TaskStatusQueued, TaskStatusDispatched, TaskStatusRunning, TaskStatusWaitingLocal} {
		in := availableRecoveryInput()
		in.OpenReview.Found = true
		in.OpenReview.ReviewerID = "reviewer-1"
		in.OpenReview.Task = ReviewOrphanTask{
			ID: "review-1", Kind: TaskKindReview, Status: status,
			WorkspaceID: "ws-1", IssueID: "issue-1", CandidateRevision: "rev-7", Generation: "gen-3",
			TargetTaskID: "repair-1",
		}
		decision := EvaluateReviewOrphan(in)
		if decision.State != ReviewOrphanAlreadyConfirmed || !decision.Processed {
			t.Fatalf("status %q decision = %+v, want confirmed processed", status, decision)
		}
	}
}

func TestEvaluateReviewOrphanDoesNotTrustReviewReadbackWithoutValidRepair(t *testing.T) {
	in := availableRecoveryInput()
	in.RepairTask.Generation = "stale-gen"
	in.OpenReview.Found = true
	in.OpenReview.ReviewerID = "reviewer-1"
	in.OpenReview.Task = ReviewOrphanTask{
		ID: "review-1", Kind: TaskKindReview, Status: TaskStatusQueued,
		WorkspaceID: "ws-1", IssueID: "issue-1", CandidateRevision: "rev-7", Generation: "gen-3",
		TargetTaskID: "repair-1",
	}
	decision := EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanBlocked || decision.Processed || decision.Reasons[0] != "open_review_task_readback_invalid" {
		t.Fatalf("invalid repair readback decision = %+v, want blocked", decision)
	}
}

func TestEvaluateReviewOrphanRejectsAuthorAsReviewerAndIdentityDrift(t *testing.T) {
	in := availableRecoveryInput()
	in.ReviewerID = in.RepairTask.AgentID
	decision := EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanBlocked || decision.Processed || decision.Reasons[0] != "reviewer_is_repair_author" {
		t.Fatalf("author review decision = %+v, want blocked", decision)
	}

	in = availableRecoveryInput()
	in.RepairTask.Generation = "stale-gen"
	decision = EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanBlocked || decision.Processed || decision.Reasons[0] != "completed_repair_identity_mismatch" {
		t.Fatalf("identity drift decision = %+v, want blocked", decision)
	}
}
