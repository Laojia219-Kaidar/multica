package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

type triggerInspectorFixture struct {
	pages   map[int]*ContinuousDispatchShadowResult
	err     error
	offsets []int
}

func (f *triggerInspectorFixture) InspectProject(
	_ context.Context,
	_, _ pgtype.UUID,
	_, offset int,
) (*ContinuousDispatchShadowResult, error) {
	f.offsets = append(f.offsets, offset)
	if f.err != nil {
		return nil, f.err
	}
	return f.pages[offset], nil
}

type triggerDispatcherFixture struct {
	requests []ContinuousDispatchRequest
	receipt  ContinuousDispatchReceipt
	err      error
}

func (f *triggerDispatcherFixture) Dispatch(
	_ context.Context,
	req ContinuousDispatchRequest,
) (ContinuousDispatchReceipt, error) {
	f.requests = append(f.requests, req)
	return f.receipt, f.err
}

func triggerShadowItem(workspaceID, issueID, agentID, runtimeID pgtype.UUID) ContinuousDispatchShadowItem {
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: shadowUUIDString(workspaceID), IssueID: shadowUUIDString(issueID),
		Stage: "implementation", CandidateRevision: "candidate-trigger", Generation: "generation-trigger-1",
	}
	return ContinuousDispatchShadowItem{
		IssueID: issueIDString(issueID), DispatchIdentity: identity,
		NextAction: continuousdispatch.NextAction{
			State: continuousdispatch.StateReady,
			Selected: &continuousdispatch.CandidateDecision{
				EmployeeID: "EMP-TRIGGER", AgentID: shadowUUIDString(agentID), RuntimeID: shadowUUIDString(runtimeID),
				Model: "glm-5.2", AccountRef: "glm-trigger-account", Eligible: true,
			},
		},
	}
}

func issueIDString(value pgtype.UUID) string { return shadowUUIDString(value) }

func TestContinuousDispatchTriggerRecomputesServerRouteBeforeDispatch(t *testing.T) {
	workspaceID := dispatchReceiptUUID(150)
	projectID := dispatchReceiptUUID(151)
	issueID := dispatchReceiptUUID(152)
	agentID := dispatchReceiptUUID(153)
	runtimeID := dispatchReceiptUUID(154)
	actorID := dispatchReceiptUUID(155)
	item := triggerShadowItem(workspaceID, issueID, agentID, runtimeID)
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{
		0: {
			SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
			ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
		},
	}}
	wantReceipt := dispatchReceiptFixture(160)
	dispatcher := &triggerDispatcherFixture{receipt: wantReceipt}
	result, err := NewContinuousDispatchTriggerService(inspector, dispatcher).DispatchIssue(
		context.Background(), workspaceID, projectID, issueID, actorID, "Exact trigger handoff",
	)
	if err != nil {
		t.Fatalf("DispatchIssue: %v", err)
	}
	if result.Receipt.TaskID != wantReceipt.TaskID || result.Action.State != continuousdispatch.StateReady {
		t.Fatalf("result = %+v, want committed receipt and ready action", result)
	}
	if len(dispatcher.requests) != 1 {
		t.Fatalf("dispatcher calls = %d, want 1", len(dispatcher.requests))
	}
	req := dispatcher.requests[0]
	if req.Identity != item.DispatchIdentity || req.Route.EmployeeRef != continuousDispatchEmployeeRefPrefix+"EMP-TRIGGER" ||
		req.Route.LocalAgentID != agentID || req.Route.RuntimeID != runtimeID || req.Route.Model != "glm-5.2" ||
		req.Route.AccountRef != "glm-trigger-account" || req.ActorUserID != actorID || req.HandoffNote != "Exact trigger handoff" {
		t.Fatalf("server-built dispatch request = %+v", req)
	}
}

func TestContinuousDispatchReviewTriggerRejectsSourceOrStatusDrift(t *testing.T) {
	workspaceID := dispatchReceiptUUID(156)
	projectID := dispatchReceiptUUID(157)
	issueID := dispatchReceiptUUID(158)
	sourceTaskID := dispatchReceiptUUID(159)
	sourceCommentID := dispatchReceiptUUID(165)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(160), dispatchReceiptUUID(161))
	item.Status = "in_review"
	item.SourceRef = continuousDispatchReviewCommentRef(sourceCommentID)
	item.SourceTaskID = shadowUUIDString(sourceTaskID)
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
		ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
	}}}
	dispatcher := &triggerDispatcherFixture{}
	trigger := NewContinuousDispatchTriggerService(inspector, dispatcher)
	if _, err := trigger.DispatchReviewIssue(context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(162), item.SourceRef, dispatchReceiptUUID(163)); !errors.Is(err, ErrContinuousDispatchIssueDrift) {
		t.Fatalf("source drift error = %v, want issue drift", err)
	}
	if len(dispatcher.requests) != 0 {
		t.Fatalf("source drift dispatched %d tasks", len(dispatcher.requests))
	}
	item.Status = "in_progress"
	inspector.pages[0].Items = []ContinuousDispatchShadowItem{item}
	if _, err := trigger.DispatchReviewIssue(context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(162), item.SourceRef, sourceTaskID); !errors.Is(err, ErrContinuousDispatchIssueDrift) {
		t.Fatalf("status drift error = %v, want issue drift", err)
	}
}

func TestContinuousDispatchReviewTriggerMarksAtomicReviewPrecondition(t *testing.T) {
	workspaceID, projectID, issueID := dispatchReceiptUUID(164), dispatchReceiptUUID(165), dispatchReceiptUUID(166)
	sourceTaskID := dispatchReceiptUUID(167)
	sourceCommentID := dispatchReceiptUUID(172)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(168), dispatchReceiptUUID(169))
	item.Status, item.SourceRef, item.SourceTaskID = "in_review", continuousDispatchReviewCommentRef(sourceCommentID), shadowUUIDString(sourceTaskID)
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
		ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
	}}}
	dispatcher := &triggerDispatcherFixture{receipt: dispatchReceiptFixture(170)}
	if _, err := NewContinuousDispatchTriggerService(inspector, dispatcher).DispatchReviewIssue(
		context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(171), item.SourceRef, sourceTaskID,
	); err != nil {
		t.Fatalf("DispatchReviewIssue: %v", err)
	}
	if len(dispatcher.requests) != 1 || !dispatcher.requests[0].requireInReview {
		t.Fatalf("review dispatch request = %+v, want atomic in_review precondition", dispatcher.requests)
	}
}

func TestContinuousDispatchTriggerRejectsBlockedOrMalformedShadow(t *testing.T) {
	workspaceID := dispatchReceiptUUID(170)
	projectID := dispatchReceiptUUID(171)
	issueID := dispatchReceiptUUID(172)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(173), dispatchReceiptUUID(174))
	item.NextAction.State = continuousdispatch.StateBlocked
	item.NextAction.Selected = nil
	dispatcher := &triggerDispatcherFixture{}
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
		ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
	}}}
	if _, err := NewContinuousDispatchTriggerService(inspector, dispatcher).DispatchIssue(
		context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(175), "",
	); !errors.Is(err, ErrContinuousDispatchNotReady) {
		t.Fatalf("blocked shadow error = %v, want ErrContinuousDispatchNotReady", err)
	}
	if len(dispatcher.requests) != 0 {
		t.Fatalf("blocked shadow dispatched %d requests", len(dispatcher.requests))
	}

	inspector.pages[0].SchemaVersion = "unknown"
	if _, err := NewContinuousDispatchTriggerService(inspector, dispatcher).DispatchIssue(
		context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(175), "",
	); !errors.Is(err, ErrContinuousDispatchSourceGap) {
		t.Fatalf("malformed shadow error = %v, want ErrContinuousDispatchSourceGap", err)
	}
}

func TestContinuousDispatchTriggerFindsIssueOnLaterPage(t *testing.T) {
	workspaceID := dispatchReceiptUUID(180)
	projectID := dispatchReceiptUUID(181)
	issueID := dispatchReceiptUUID(182)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(183), dispatchReceiptUUID(184))
	firstPageItems := make([]ContinuousDispatchShadowItem, continuousDispatchTriggerPageSize)
	for i := range firstPageItems {
		firstPageItems[i].IssueID = "not-the-target"
	}
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{
		0: {
			SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
			ProjectID: shadowUUIDString(projectID), Items: firstPageItems, Total: continuousDispatchTriggerPageSize + 1,
		},
		continuousDispatchTriggerPageSize: {
			SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
			ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: continuousDispatchTriggerPageSize + 1,
		},
	}}
	dispatcher := &triggerDispatcherFixture{receipt: dispatchReceiptFixture(190)}
	if _, err := NewContinuousDispatchTriggerService(inspector, dispatcher).DispatchIssue(
		context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(185), "",
	); err != nil {
		t.Fatalf("later-page DispatchIssue: %v", err)
	}
	if len(inspector.offsets) != 2 || inspector.offsets[0] != 0 || inspector.offsets[1] != continuousDispatchTriggerPageSize {
		t.Fatalf("inspector offsets = %v, want [0 %d]", inspector.offsets, continuousDispatchTriggerPageSize)
	}
}

var _ ContinuousDispatchProjectInspector = (*triggerInspectorFixture)(nil)
var _ ContinuousDispatchExactDispatcher = (*triggerDispatcherFixture)(nil)
