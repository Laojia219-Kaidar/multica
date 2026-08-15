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

func reviewDispatchShadowItem(workspaceID, issueID, sourceCommentID, sourceTaskID pgtype.UUID) ContinuousDispatchShadowItem {
	return ContinuousDispatchShadowItem{
		IssueID: shadowUUIDString(issueID), Status: "in_review",
		SourceRef: continuousDispatchReviewCommentRef(sourceCommentID), SourceTaskID: shadowUUIDString(sourceTaskID),
		DispatchIdentity: continuousdispatch.DispatchIdentity{
			WorkspaceID: shadowUUIDString(workspaceID), IssueID: shadowUUIDString(issueID), Stage: "review",
			CandidateRevision: "candidate-review", Generation: "generation-review",
		},
		NextAction: continuousdispatch.NextAction{
			State:    continuousdispatch.StateReady,
			Selected: &continuousdispatch.CandidateDecision{EmployeeID: "EMP-REVIEW", ActiveWIP: 0, MaxWIP: 2, Eligible: true},
		},
	}
}

func reviewDispatchPage(workspaceID, projectID pgtype.UUID, items ...ContinuousDispatchShadowItem) *ContinuousDispatchShadowResult {
	return &ContinuousDispatchShadowResult{
		SchemaVersion: ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   shadowUUIDString(workspaceID), ProjectID: shadowUUIDString(projectID),
		Total: len(items), Items: items, Sources: ContinuousDispatchShadowSources{Tasks: true, WriteLease: true, WIP: true},
	}
}

func TestReviewDispatchPreviewRequiresSourceLineageAndAuthority(t *testing.T) {
	workspaceID, projectID := reviewDispatchUUID(1), reviewDispatchUUID(2)
	ready := reviewDispatchShadowItem(workspaceID, reviewDispatchUUID(3), reviewDispatchUUID(6), reviewDispatchUUID(7))
	missingSource := reviewDispatchShadowItem(workspaceID, reviewDispatchUUID(4), reviewDispatchUUID(8), reviewDispatchUUID(9))
	missingSource.SourceRef, missingSource.SourceTaskID = "", ""
	nonReview := reviewDispatchShadowItem(workspaceID, reviewDispatchUUID(5), reviewDispatchUUID(10), reviewDispatchUUID(11))
	nonReview.Status = "in_progress"
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: reviewDispatchPage(workspaceID, projectID, ready, missingSource, nonReview)}}
	preview, err := NewReviewDispatchBatchService(inspector, nil).PreviewProject(context.Background(), workspaceID, projectID, 10, 0)
	if err != nil {
		t.Fatalf("PreviewProject: %v", err)
	}
	if preview.Eligible != 0 || preview.Skipped != 2 || len(preview.Items) != 2 {
		t.Fatalf("preview = %+v, want two skipped review items", preview)
	}
	if preview.Items[0].Reasons[0] != continuousdispatch.Reason("authority_evidence_missing") {
		t.Fatalf("authority-gap item = %+v, want authority_evidence_missing", preview.Items[0])
	}
	if preview.Items[1].Reasons[0] != continuousdispatch.Reason("source_lineage_evidence_missing") {
		t.Fatalf("source-gap item = %+v, want source_lineage_evidence_missing", preview.Items[1])
	}
}

func TestReviewDispatchBatchDoesNotWriteWithoutAuthorityGate(t *testing.T) {
	workspaceID, projectID, issueID := reviewDispatchUUID(11), reviewDispatchUUID(12), reviewDispatchUUID(13)
	item := reviewDispatchShadowItem(workspaceID, issueID, reviewDispatchUUID(19), reviewDispatchUUID(14))
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: reviewDispatchPage(workspaceID, projectID, item)}}
	dispatcher := &triggerDispatcherFixture{receipt: dispatchReceiptFixture(18)}
	trigger := NewContinuousDispatchTriggerService(inspector, dispatcher)
	result, err := NewReviewDispatchBatchService(inspector, trigger).DispatchProject(
		context.Background(), workspaceID, projectID, reviewDispatchUUID(17), 1, 0,
	)
	if err != nil {
		t.Fatalf("DispatchProject: %v", err)
	}
	if len(result.Receipts) != 0 || len(dispatcher.requests) != 0 {
		t.Fatalf("result = %+v requests=%d, want no writes without Authority gate", result, len(dispatcher.requests))
	}
	if len(result.Preview.Items) != 1 || result.Preview.Items[0].Reasons[0] != continuousdispatch.Reason("authority_evidence_missing") {
		t.Fatalf("preview = %+v, want authority block", result.Preview)
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
	firstReview := reviewDispatchShadowItem(workspaceID, reviewDispatchUUID(32), reviewDispatchUUID(51), reviewDispatchUUID(52))
	secondReview := reviewDispatchShadowItem(workspaceID, reviewDispatchUUID(35), reviewDispatchUUID(53), reviewDispatchUUID(54))
	nonReview := reviewDispatchShadowItem(workspaceID, reviewDispatchUUID(38), reviewDispatchUUID(55), reviewDispatchUUID(56))
	nonReview.Status = "in_progress"
	firstPage := reviewDispatchPage(workspaceID, projectID, nonReview, firstReview)
	firstPage.Total = 3
	secondPage := reviewDispatchPage(workspaceID, projectID, secondReview)
	secondPage.Total = 3
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: firstPage, 200: secondPage}}
	preview, err := NewReviewDispatchBatchService(inspector, nil).PreviewProject(context.Background(), workspaceID, projectID, 1, 0)
	if err != nil {
		t.Fatalf("PreviewProject: %v", err)
	}
	if preview.Total != 2 || preview.Eligible != 0 || len(preview.Items) != 1 {
		t.Fatalf("preview = %+v, want total=2 and first review page blocked by Authority", preview)
	}
	if preview.Items[0].IssueID != firstReview.IssueID {
		t.Fatalf("preview item = %+v, want first review issue", preview.Items[0])
	}
	if len(inspector.offsets) != 2 || inspector.offsets[0] != 0 || inspector.offsets[1] != 200 {
		t.Fatalf("scan offsets = %v, want complete scan across general pages", inspector.offsets)
	}
}

func TestReviewDispatchDoesNotWriteReviewItemsAcrossPagesWithoutAuthority(t *testing.T) {
	workspaceID, projectID, actorID := reviewDispatchUUID(41), reviewDispatchUUID(42), reviewDispatchUUID(43)
	review := reviewDispatchShadowItem(workspaceID, reviewDispatchUUID(44), reviewDispatchUUID(55), reviewDispatchUUID(56))
	nonReview := reviewDispatchShadowItem(workspaceID, reviewDispatchUUID(47), reviewDispatchUUID(57), reviewDispatchUUID(58))
	nonReview.Status = "in_progress"
	firstPage := reviewDispatchPage(workspaceID, projectID, nonReview)
	firstPage.Total = 2
	secondPage := reviewDispatchPage(workspaceID, projectID, review)
	secondPage.Total = 2
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: firstPage, 200: secondPage}}
	dispatcher := &triggerDispatcherFixture{receipt: dispatchReceiptFixture(50)}
	result, err := NewReviewDispatchBatchService(inspector, NewContinuousDispatchTriggerService(inspector, dispatcher)).DispatchProject(
		context.Background(), workspaceID, projectID, actorID, 1, 0,
	)
	if err != nil {
		t.Fatalf("DispatchProject: %v", err)
	}
	if len(result.Receipts) != 0 || len(dispatcher.requests) != 0 {
		t.Fatalf("result = %+v requests=%d, want no review dispatch without Authority gate", result, len(dispatcher.requests))
	}
	if len(result.Preview.Items) != 1 || result.Preview.Items[0].Reasons[0] != continuousdispatch.Reason("authority_evidence_missing") {
		t.Fatalf("preview = %+v, want authority block", result.Preview)
	}
}
