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
		AgentID: "reviewer-1", TargetTaskID: "repair-1",
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
			AgentID: "reviewer-1", TargetTaskID: "repair-1",
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
		AgentID: "reviewer-1", TargetTaskID: "repair-1",
	}
	decision := EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanBlocked || decision.Processed || decision.Reasons[0] != "open_review_task_readback_invalid" {
		t.Fatalf("invalid repair readback decision = %+v, want blocked", decision)
	}
}

func TestEvaluateReviewOrphanRejectsOpenReviewTaskWithoutExactReviewerBinding(t *testing.T) {
	valid := func() ReviewOrphanRecoveryInput {
		in := availableRecoveryInput()
		in.OpenReview.Found = true
		in.OpenReview.ReviewerID = "reviewer-1"
		in.OpenReview.Task = ReviewOrphanTask{
			ID: "review-1", Kind: TaskKindReview, Status: TaskStatusQueued,
			WorkspaceID: "ws-1", IssueID: "issue-1", CandidateRevision: "rev-7", Generation: "gen-3",
			AgentID: "reviewer-1", TargetTaskID: "repair-1",
		}
		return in
	}
	for _, mutate := range []struct {
		name  string
		apply func(*ReviewOrphanRecoveryInput)
	}{
		{"missing task agent", func(in *ReviewOrphanRecoveryInput) { in.OpenReview.Task.AgentID = "" }},
		{"task agent differs from evidence", func(in *ReviewOrphanRecoveryInput) { in.OpenReview.Task.AgentID = "other-reviewer" }},
		{"task agent is repair author", func(in *ReviewOrphanRecoveryInput) { in.OpenReview.Task.AgentID = "writer-1" }},
		{"evidence differs from selected reviewer", func(in *ReviewOrphanRecoveryInput) { in.OpenReview.ReviewerID = "other-reviewer" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			in := valid()
			mutate.apply(&in)
			decision := EvaluateReviewOrphan(in)
			if decision.State != ReviewOrphanBlocked || decision.Processed {
				t.Fatalf("decision = %+v, want unprocessed blocked", decision)
			}
		})
	}
}

func TestEvaluateReviewOrphanRejectsBlankAndPaddedReviewerIDs(t *testing.T) {
	for _, reviewerID := range []string{"", "  ", " reviewer-1 ", "\treviewer-1"} {
		in := availableRecoveryInput()
		in.ReviewerID = reviewerID
		decision := EvaluateReviewOrphan(in)
		if decision.State != ReviewOrphanBlocked || decision.Processed {
			t.Fatalf("reviewer %q decision = %+v, want unprocessed blocked", reviewerID, decision)
		}
	}

	in := availableRecoveryInput()
	in.OpenReview.Found = true
	in.OpenReview.ReviewerID = " reviewer-1 "
	in.OpenReview.Task = ReviewOrphanTask{
		ID: "review-1", Kind: TaskKindReview, Status: TaskStatusQueued,
		WorkspaceID: "ws-1", IssueID: "issue-1", CandidateRevision: "rev-7", Generation: "gen-3",
		AgentID: "reviewer-1", TargetTaskID: "repair-1",
	}
	decision := EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanBlocked || decision.Processed {
		t.Fatalf("padded evidence reviewer decision = %+v, want unprocessed blocked", decision)
	}
}

func TestEvaluateReviewOrphanNeverProcessesOpenTaskOutsideReviseRequested(t *testing.T) {
	in := availableRecoveryInput()
	in.ReviewState = "queued"
	in.OpenReview.Found = true
	in.OpenReview.ReviewerID = "reviewer-1"
	in.OpenReview.Task = ReviewOrphanTask{
		ID: "review-1", Kind: TaskKindReview, Status: TaskStatusQueued,
		WorkspaceID: "ws-1", IssueID: "issue-1", CandidateRevision: "rev-7", Generation: "gen-3",
		AgentID: "reviewer-1", TargetTaskID: "repair-1",
	}
	decision := EvaluateReviewOrphan(in)
	if decision.State != ReviewOrphanBlocked || decision.Processed || decision.Reasons[0] != "review_state_not_revise_requested" {
		t.Fatalf("decision = %+v, want unprocessed review-state block", decision)
	}
}

func TestEvaluateReviewOrphanRejectsPaddedCanonicalIdentityFields(t *testing.T) {
	valid := func() ReviewOrphanRecoveryInput {
		in := availableRecoveryInput()
		in.OpenReview.Found = true
		in.OpenReview.ReviewerID = "reviewer-1"
		in.OpenReview.Task = ReviewOrphanTask{
			ID: "review-1", Kind: TaskKindReview, Status: TaskStatusQueued,
			WorkspaceID: "ws-1", IssueID: "issue-1", CandidateRevision: "rev-7", Generation: "gen-3",
			AgentID: "reviewer-1", TargetTaskID: "repair-1",
		}
		return in
	}
	for _, mutate := range []struct {
		name  string
		apply func(*ReviewOrphanRecoveryInput)
	}{
		{"identity workspace", func(in *ReviewOrphanRecoveryInput) { in.Identity.WorkspaceID = " ws-1 " }},
		{"identity issue", func(in *ReviewOrphanRecoveryInput) { in.Identity.IssueID = " issue-1 " }},
		{"identity candidate revision", func(in *ReviewOrphanRecoveryInput) { in.Identity.CandidateRevision = " rev-7 " }},
		{"identity generation", func(in *ReviewOrphanRecoveryInput) { in.Identity.Generation = " gen-3 " }},
		{"task id", func(in *ReviewOrphanRecoveryInput) { in.OpenReview.Task.ID = " review-1 " }},
		{"task workspace", func(in *ReviewOrphanRecoveryInput) { in.OpenReview.Task.WorkspaceID = " ws-1 " }},
		{"task issue", func(in *ReviewOrphanRecoveryInput) { in.OpenReview.Task.IssueID = " issue-1 " }},
		{"task candidate revision", func(in *ReviewOrphanRecoveryInput) { in.OpenReview.Task.CandidateRevision = " rev-7 " }},
		{"task generation", func(in *ReviewOrphanRecoveryInput) { in.OpenReview.Task.Generation = " gen-3 " }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			in := valid()
			mutate.apply(&in)
			decision := EvaluateReviewOrphan(in)
			if decision.State != ReviewOrphanBlocked || decision.Processed {
				t.Fatalf("decision = %+v, want unprocessed blocked", decision)
			}
		})
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
