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
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   shadowUUIDString(workspaceID), ProjectID: shadowUUIDString(projectID), Total: 3,
		Items: []ContinuousDispatchShadowItem{
			{IssueID: shadowUUIDString(issueReady), IssueTitle: "ready", Status: "in_review", SourceTaskID: shadowUUIDString(reviewDispatchUUID(6)), NextAction: continuousdispatch.NextAction{
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
	agentID, runtimeID, actorID := reviewDispatchUUID(15), reviewDispatchUUID(16), reviewDispatchUUID(17)
	item := triggerShadowItem(workspaceID, issueID, agentID, runtimeID)
	item.Status = "in_review"
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
	if got := dispatcher.requests[0].HandoffNote; got != "review_dispatch source_issue_id="+shadowUUIDString(issueID)+" source_task_id="+shadowUUIDString(sourceTaskID) {
		t.Fatalf("handoff note = %q, want stable source provenance", got)
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
