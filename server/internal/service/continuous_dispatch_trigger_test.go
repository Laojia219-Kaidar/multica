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
