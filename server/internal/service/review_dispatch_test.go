package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

func reviewDispatchUUID(n byte) pgtype.UUID {
	var id pgtype.UUID
	id.Valid = true
	id.Bytes[15] = n
	return id
}

func TestReviewDispatchPreviewFiltersAndRequiresSourceTask(t *testing.T) {
	workspaceID, projectID := reviewDispatchUUID(1), reviewDispatchUUID(2)
	issueReady := reviewDispatchUUID(3)
	issueMissingSource := reviewDispatchUUID(4)
	issueNotReview := reviewDispatchUUID(5)
	sourceCommentID := reviewDispatchUUID(6)
	sourceTaskID := reviewDispatchUUID(7)
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   shadowUUIDString(workspaceID), ProjectID: shadowUUIDString(projectID), Total: 3,
		Items: []ContinuousDispatchShadowItem{
			{IssueID: shadowUUIDString(issueReady), IssueTitle: "ready", Status: "in_review", SourceRef: continuousDispatchReviewCommentRef(sourceCommentID), SourceTaskID: shadowUUIDString(sourceTaskID), NextAction: continuousdispatch.NextAction{
				State:    continuousdispatch.StateReady,
				Selected: &continuousdispatch.CandidateDecision{EmployeeID: "EMP-1", Eligible: true},
			}},
			{IssueID: shadowUUIDString(issueMissingSource), IssueTitle: "missing source", Status: "in_review", NextAction: continuousdispatch.NextAction{
				State:    continuousdispatch.StateReady,
				Selected: &continuousdispatch.CandidateDecision{EmployeeID: "EMP-2", Eligible: true},
			}},
			{IssueID: shadowUUIDString(issueNotReview), IssueTitle: "implementation", Status: "in_progress", NextAction: continuousdispatch.NextAction{State: continuousdispatch.StateReady}},
		},
	}}}
	preview, err := NewReviewDispatchBatchService(inspector, nil).PreviewProject(context.Background(), workspaceID, projectID, 10, 0)
	if err != nil {
		t.Fatalf("PreviewProject: %v", err)
	}
	if preview.Eligible != 1 || preview.Skipped != 1 || len(preview.Items) != 2 {
		t.Fatalf("preview = %+v, want one eligible and one skipped review", preview)
	}
	if preview.Items[1].State != continuousdispatch.StateBlocked ||
		len(preview.Items[1].Reasons) != 1 || preview.Items[1].Reasons[0] != continuousdispatch.ReasonReviewSourceTaskMissing {
		t.Fatalf("missing-source item = %+v, want source-task block", preview.Items[1])
	}
}

func TestReviewDispatchBatchReplansAndCarriesProvenance(t *testing.T) {
	workspaceID, projectID, issueID := reviewDispatchUUID(11), reviewDispatchUUID(12), reviewDispatchUUID(13)
	sourceTaskID := reviewDispatchUUID(14)
	sourceCommentID := reviewDispatchUUID(19)
	agentID, runtimeID, actorID := reviewDispatchUUID(15), reviewDispatchUUID(16), reviewDispatchUUID(17)
	item := triggerShadowItem(workspaceID, issueID, agentID, runtimeID)
	item.Status = "in_review"
	item.SourceRef = continuousDispatchReviewCommentRef(sourceCommentID)
	item.SourceTaskID = shadowUUIDString(sourceTaskID)
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
		ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
	}}}
	dispatcher := &triggerDispatcherFixture{receipt: dispatchReceiptFixture(18)}
	result, err := NewReviewDispatchBatchService(inspector, NewContinuousDispatchTriggerService(inspector, dispatcher)).DispatchProject(
		context.Background(), workspaceID, projectID, actorID, 1, 0,
	)
	if err != nil {
		t.Fatalf("DispatchProject: %v", err)
	}
	if len(result.Receipts) != 1 || len(dispatcher.requests) != 1 {
		t.Fatalf("result = %+v requests=%d, want one receipt/request", result, len(dispatcher.requests))
	}
	if got := dispatcher.requests[0].HandoffNote; got != "review_dispatch source_ref="+continuousDispatchReviewCommentRef(sourceCommentID)+" source_issue_id="+shadowUUIDString(issueID)+" source_task_id="+shadowUUIDString(sourceTaskID)+" initiator_source="+continuousDispatchReviewInitiatorSourceV1 {
		t.Fatalf("handoff note = %q, want stable source provenance", got)
	}
	provenance := dispatcher.requests[0].reviewProvenance
	if provenance == nil || provenance.SourceRef != continuousDispatchReviewCommentRef(sourceCommentID) ||
		provenance.SourceIssueID != shadowUUIDString(issueID) || provenance.SourceTaskID != shadowUUIDString(sourceTaskID) ||
		provenance.InitiatorSource != continuousDispatchReviewInitiatorSourceV1 {
		t.Fatalf("review provenance = %+v, want server-built structured lineage", provenance)
	}
	if dispatcher.requests[0].Route.LocalAgentID != agentID || dispatcher.requests[0].Route.RuntimeID != runtimeID {
		t.Fatalf("route = %+v, want server-selected route", dispatcher.requests[0].Route)
	}
}

func TestReviewDispatchPreviewBoundsBatch(t *testing.T) {
	service := NewReviewDispatchBatchService(&triggerInspectorFixture{}, nil)
	if _, err := service.PreviewProject(context.Background(), reviewDispatchUUID(20), reviewDispatchUUID(21), 26, 0); err == nil {
		t.Fatal("limit 26 accepted; want bounded batch rejection")
	}
}

func TestReviewDispatchPreviewScansMixedGeneralPages(t *testing.T) {
	workspaceID, projectID := reviewDispatchUUID(30), reviewDispatchUUID(31)
	firstReview := triggerShadowItem(workspaceID, reviewDispatchUUID(32), reviewDispatchUUID(33), reviewDispatchUUID(34))
	firstReview.Status = "in_review"
	firstReview.SourceRef = continuousDispatchReviewCommentRef(reviewDispatchUUID(51))
	firstReview.SourceTaskID = shadowUUIDString(reviewDispatchUUID(52))
	firstReview.NextAction.State = continuousdispatch.StateReady
	secondReview := triggerShadowItem(workspaceID, reviewDispatchUUID(35), reviewDispatchUUID(36), reviewDispatchUUID(37))
	secondReview.Status = "in_review"
	secondReview.SourceRef = continuousDispatchReviewCommentRef(reviewDispatchUUID(53))
	secondReview.SourceTaskID = shadowUUIDString(reviewDispatchUUID(54))
	secondReview.NextAction.State = continuousdispatch.StateFallback
	nonReview := triggerShadowItem(workspaceID, reviewDispatchUUID(38), reviewDispatchUUID(39), reviewDispatchUUID(40))
	nonReview.Status = "in_progress"
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{
		0: {
			SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
			ProjectID: shadowUUIDString(projectID), Total: 3,
			Items: []ContinuousDispatchShadowItem{nonReview, firstReview},
		},
		200: {
			SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
			ProjectID: shadowUUIDString(projectID), Total: 3,
			Items: []ContinuousDispatchShadowItem{secondReview},
		},
	}}
	preview, err := NewReviewDispatchBatchService(inspector, nil).PreviewProject(context.Background(), workspaceID, projectID, 1, 0)
	if err != nil {
		t.Fatalf("PreviewProject: %v", err)
	}
	if preview.Total != 2 || preview.Eligible != 1 || len(preview.Items) != 1 {
		t.Fatalf("preview = %+v, want total=2 and first review page", preview)
	}
	if preview.Items[0].IssueID != firstReview.IssueID {
		t.Fatalf("preview item = %+v, want first review issue", preview.Items[0])
	}
	if len(inspector.offsets) != 2 || inspector.offsets[0] != 0 || inspector.offsets[1] != 200 {
		t.Fatalf("scan offsets = %v, want complete scan across general pages", inspector.offsets)
	}
}

func TestReviewDispatchPreviewDispatchesOnlyReviewItemsAcrossPages(t *testing.T) {
	workspaceID, projectID, actorID := reviewDispatchUUID(41), reviewDispatchUUID(42), reviewDispatchUUID(43)
	review := triggerShadowItem(workspaceID, reviewDispatchUUID(44), reviewDispatchUUID(45), reviewDispatchUUID(46))
	review.Status = "in_review"
	review.SourceRef = continuousDispatchReviewCommentRef(reviewDispatchUUID(55))
	review.SourceTaskID = shadowUUIDString(reviewDispatchUUID(56))
	review.NextAction.State = continuousdispatch.StateReady
	nonReview := triggerShadowItem(workspaceID, reviewDispatchUUID(47), reviewDispatchUUID(48), reviewDispatchUUID(49))
	nonReview.Status = "in_progress"
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{
		0: {
			SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
			ProjectID: shadowUUIDString(projectID), Total: 2,
			Items: []ContinuousDispatchShadowItem{nonReview},
		},
		200: {
			SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
			ProjectID: shadowUUIDString(projectID), Total: 2,
			Items: []ContinuousDispatchShadowItem{review},
		},
	}}
	dispatcher := &triggerDispatcherFixture{receipt: dispatchReceiptFixture(50)}
	result, err := NewReviewDispatchBatchService(inspector, NewContinuousDispatchTriggerService(inspector, dispatcher)).DispatchProject(
		context.Background(), workspaceID, projectID, actorID, 1, 0,
	)
	if err != nil {
		t.Fatalf("DispatchProject: %v", err)
	}
	if len(result.Receipts) != 1 || len(dispatcher.requests) != 1 {
		t.Fatalf("result = %+v requests=%d, want one review dispatch", result, len(dispatcher.requests))
	}
	if dispatcher.requests[0].Identity.IssueID != review.IssueID {
		t.Fatalf("dispatched issue = %s, want review issue %s", dispatcher.requests[0].Identity.IssueID, review.IssueID)
	}
}
