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

type triggerRecoveryVerifierFixture struct {
	calls int
	err   error
}

type triggerAuthorityIdentityProviderFixture struct {
	identity  AuthorityReviewDispatchIdentity
	err       error
	calls     int
	workspace pgtype.UUID
	project   pgtype.UUID
	issue     pgtype.UUID
	candidate AuthorityReviewDispatchCandidate
}

func (f *triggerAuthorityIdentityProviderFixture) ResolveReviewDispatchIdentity(
	_ context.Context,
	workspaceID, projectID, issueID pgtype.UUID,
	candidate AuthorityReviewDispatchCandidate,
) (AuthorityReviewDispatchIdentity, error) {
	f.calls++
	f.workspace, f.project, f.issue, f.candidate = workspaceID, projectID, issueID, candidate
	return f.identity, f.err
}

func (f *triggerRecoveryVerifierFixture) VerifyReviewOrphanRecoveryPrecondition(_ context.Context, _ ReviewOrphanRecoveryPrecondition) error {
	f.calls++
	return f.err
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

func TestContinuousDispatchGenericTriggerFailsClosedForReviewItemEvenWhenAuthorityModeIsComposed(t *testing.T) {
	workspaceID, projectID, issueID := dispatchReceiptUUID(64), dispatchReceiptUUID(65), dispatchReceiptUUID(66)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(67), dispatchReceiptUUID(68))
	item.Status = "in_review"
	item.DispatchIdentity.Stage = "review"
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
		ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
	}}}
	dispatcher := &triggerDispatcherFixture{}
	authorizer := &authorityReviewDispatchAuthorizerFake{}
	provider := &triggerAuthorityIdentityProviderFixture{}
	trigger := NewContinuousDispatchTriggerService(inspector, dispatcher).WithAuthorityReviewDispatchGate(
		NewAuthorityReviewDispatchGate(authorizer, dispatcher), provider,
	)

	_, err := trigger.DispatchIssue(context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(69), "generic handoff")
	if !errors.Is(err, ErrContinuousDispatchSourceGap) {
		t.Fatalf("DispatchIssue error = %v, want source gap", err)
	}
	if provider.calls != 0 || authorizer.calls != 0 || len(dispatcher.requests) != 0 {
		t.Fatalf("resolver/authority/dispatcher calls = %d/%d/%d, want 0/0/0", provider.calls, authorizer.calls, len(dispatcher.requests))
	}
}

func TestContinuousDispatchReviewTriggerAuthorityModeRunsResolverGateBeforeDispatcher(t *testing.T) {
	workspaceID := parseDispatchUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c010")
	projectID := dispatchReceiptUUID(69)
	issueID := parseDispatchUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c011")
	sourceTaskID, sourceCommentID := dispatchReceiptUUID(71), dispatchReceiptUUID(72)
	agentID := parseDispatchUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c001")
	runtimeID := dispatchReceiptUUID(74)
	item := triggerShadowItem(workspaceID, issueID, agentID, runtimeID)
	item.DispatchIdentity.Stage = "review"
	item.Status = "in_review"
	item.SourceRef = continuousDispatchReviewCommentRef(sourceCommentID)
	item.SourceTaskID = shadowUUIDString(sourceTaskID)
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
		ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
	}}}
	dispatcher := &triggerDispatcherFixture{receipt: dispatchReceiptFixture(75)}
	identity := AuthorityReviewDispatchIdentity{
		TenantID:           "tenant-hivecosm-1",
		WorkOrderSourceRef: "hive://hivecosm/delivery/project/project-1/work-order/wo-1",
		EmployeeID:         item.NextAction.Selected.EmployeeID,
		IdentityBindingID:  "binding-review-trigger",
		AgentID:            shadowUUIDString(agentID),
		AssignmentID:       "assignment-review-trigger",
	}
	lookup, err := identity.lookup()
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &authorityReviewDispatchAuthorizerFake{response: authorityReviewDispatchGateResponse(lookup, ContinuousDispatchRequest{Identity: item.DispatchIdentity})}
	provider := &triggerAuthorityIdentityProviderFixture{identity: identity}
	gate := NewAuthorityReviewDispatchGate(authorizer, dispatcher)
	trigger := NewContinuousDispatchTriggerService(inspector, dispatcher).WithAuthorityReviewDispatchGate(gate, provider)

	result, err := trigger.DispatchReviewIssue(context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(76), item.SourceRef, sourceTaskID)
	if err != nil {
		t.Fatalf("DispatchReviewIssue: %v", err)
	}
	if result.Receipt.TaskID != dispatcher.receipt.TaskID || provider.calls != 1 || authorizer.calls != 1 || len(dispatcher.requests) != 1 {
		t.Fatalf("receipt/provider/authority/dispatcher = %#v/%d/%d/%d, want receipt/1/1/1", result.Receipt, provider.calls, authorizer.calls, len(dispatcher.requests))
	}
	if provider.workspace != workspaceID || provider.project != projectID || provider.issue != issueID {
		t.Fatalf("resolver scope = %s/%s/%s, want %s/%s/%s", provider.workspace, provider.project, provider.issue, workspaceID, projectID, issueID)
	}
	if provider.candidate.request.Route.LocalAgentID != agentID || !provider.candidate.request.requireInReview {
		t.Fatalf("resolver candidate = %+v, want server-built review candidate", provider.candidate.request)
	}
}

func TestContinuousDispatchReviewTriggerAuthorityModeFailsClosedWithoutResolver(t *testing.T) {
	workspaceID, projectID, issueID := dispatchReceiptUUID(78), dispatchReceiptUUID(79), dispatchReceiptUUID(80)
	sourceTaskID, sourceCommentID := dispatchReceiptUUID(81), dispatchReceiptUUID(82)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(83), dispatchReceiptUUID(84))
	item.DispatchIdentity.Stage = "review"
	item.Status = "in_review"
	item.SourceRef = continuousDispatchReviewCommentRef(sourceCommentID)
	item.SourceTaskID = shadowUUIDString(sourceTaskID)
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
		ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
	}}}
	dispatcher := &triggerDispatcherFixture{}
	authorizer := &authorityReviewDispatchAuthorizerFake{}
	gate := NewAuthorityReviewDispatchGate(authorizer, dispatcher)
	trigger := NewContinuousDispatchTriggerService(inspector, dispatcher).WithAuthorityReviewDispatchGate(gate, nil)

	_, err := trigger.DispatchReviewIssue(context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(85), item.SourceRef, sourceTaskID)
	if !errors.Is(err, ErrAuthorityReviewDispatchSourceGap) {
		t.Fatalf("DispatchReviewIssue error = %v, want authority source gap", err)
	}
	if authorizer.calls != 0 || len(dispatcher.requests) != 0 {
		t.Fatalf("authority/dispatcher calls = %d/%d, want 0/0", authorizer.calls, len(dispatcher.requests))
	}
}

func TestContinuousDispatchReviewTriggerAuthorityModeFailsClosedOnResolverError(t *testing.T) {
	workspaceID, projectID, issueID := dispatchReceiptUUID(86), dispatchReceiptUUID(87), dispatchReceiptUUID(88)
	sourceTaskID, sourceCommentID := dispatchReceiptUUID(89), dispatchReceiptUUID(90)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(91), dispatchReceiptUUID(92))
	item.DispatchIdentity.Stage = "review"
	item.Status = "in_review"
	item.SourceRef = continuousDispatchReviewCommentRef(sourceCommentID)
	item.SourceTaskID = shadowUUIDString(sourceTaskID)
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {
		SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID),
		ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1,
	}}}
	dispatcher := &triggerDispatcherFixture{}
	authorizer := &authorityReviewDispatchAuthorizerFake{}
	provider := &triggerAuthorityIdentityProviderFixture{err: errors.New("authority unavailable")}
	gate := NewAuthorityReviewDispatchGate(authorizer, dispatcher)
	trigger := NewContinuousDispatchTriggerService(inspector, dispatcher).WithAuthorityReviewDispatchGate(gate, provider)

	_, err := trigger.DispatchReviewIssue(context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(93), item.SourceRef, sourceTaskID)
	if !errors.Is(err, ErrAuthorityReviewDispatchSourceGap) {
		t.Fatalf("DispatchReviewIssue error = %v, want authority source gap", err)
	}
	if provider.calls != 1 || authorizer.calls != 0 || len(dispatcher.requests) != 0 {
		t.Fatalf("resolver/authority/dispatcher calls = %d/%d/%d, want 1/0/0", provider.calls, authorizer.calls, len(dispatcher.requests))
	}
}

func recoveryTriggerPrecondition(workspaceID, projectID, issueID, repairTaskID, repairCommentID pgtype.UUID) ReviewOrphanRecoveryPrecondition {
	return ReviewOrphanRecoveryPrecondition{
		WorkspaceID: shadowUUIDString(workspaceID), ProjectID: shadowUUIDString(projectID), IssueID: shadowUUIDString(issueID),
		IssueStatus: "in_review", IssueStage: "review", ReviewState: continuousdispatch.ReviewStateReviseRequested,
		CandidateRevision: "candidate-trigger", Generation: "generation-trigger-1", RepairTaskID: shadowUUIDString(repairTaskID), RepairTaskAgentID: shadowUUIDString(dispatchReceiptUUID(221)),
		RepairComment:  ReviewOrphanRepairComment{SourceTaskID: shadowUUIDString(repairTaskID), AuthorID: shadowUUIDString(dispatchReceiptUUID(221)), WorkspaceID: shadowUUIDString(workspaceID), IssueID: shadowUUIDString(issueID)},
		RepairEvidence: ReviewOrphanRepairEvidence{Kind: continuousdispatch.TaskKindRepair, ContextRef: "issue-context:trigger", EvidenceRef: "receipt:trigger"},
		SourceRef:      continuousDispatchReviewCommentRef(repairCommentID),
	}
}

func TestContinuousDispatchRecoveryTriggerRechecksImmutablePreconditionBeforeDispatch(t *testing.T) {
	workspaceID, projectID, issueID := dispatchReceiptUUID(210), dispatchReceiptUUID(211), dispatchReceiptUUID(212)
	repairTaskID, commentID := dispatchReceiptUUID(213), dispatchReceiptUUID(214)
	precondition := recoveryTriggerPrecondition(workspaceID, projectID, issueID, repairTaskID, commentID)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(215), dispatchReceiptUUID(216))
	item.Status, item.SourceRef, item.SourceTaskID, item.DispatchIdentity.Stage = "in_review", precondition.SourceRef, precondition.RepairTaskID, "review"

	for _, tc := range []struct {
		name   string
		mutate func(*ContinuousDispatchShadowItem)
	}{
		{"status", func(i *ContinuousDispatchShadowItem) { i.Status = "in_progress" }},
		{"stage", func(i *ContinuousDispatchShadowItem) { i.DispatchIdentity.Stage = "implementation" }},
		{"revision", func(i *ContinuousDispatchShadowItem) { i.DispatchIdentity.CandidateRevision = "candidate-other" }},
		{"generation", func(i *ContinuousDispatchShadowItem) { i.DispatchIdentity.Generation = "generation-trigger-2" }},
		{"source task", func(i *ContinuousDispatchShadowItem) { i.SourceTaskID = shadowUUIDString(dispatchReceiptUUID(219)) }},
		{"source ref", func(i *ContinuousDispatchShadowItem) {
			i.SourceRef = continuousDispatchReviewCommentRef(dispatchReceiptUUID(220))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drifted := item
			tc.mutate(&drifted)
			inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID), ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{drifted}, Total: 1}}}
			dispatcher := &triggerDispatcherFixture{}
			verifier := &triggerRecoveryVerifierFixture{}
			_, err := NewContinuousDispatchTriggerService(inspector, dispatcher).WithReviewOrphanRecoveryPreconditionVerifier(verifier).DispatchReviewIssueWithRecoveryPrecondition(context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(217), precondition)
			if !errors.Is(err, ErrContinuousDispatchIssueDrift) || len(dispatcher.requests) != 0 {
				t.Fatalf("err=%v dispatches=%d", err, len(dispatcher.requests))
			}
		})
	}
}

func TestContinuousDispatchRecoveryTriggerRequiresCanonicalVerifier(t *testing.T) {
	workspaceID, projectID, issueID := dispatchReceiptUUID(230), dispatchReceiptUUID(231), dispatchReceiptUUID(232)
	repairTaskID, commentID := dispatchReceiptUUID(233), dispatchReceiptUUID(234)
	precondition := recoveryTriggerPrecondition(workspaceID, projectID, issueID, repairTaskID, commentID)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(235), dispatchReceiptUUID(236))
	item.Status, item.SourceRef, item.SourceTaskID, item.DispatchIdentity.Stage = "in_review", precondition.SourceRef, precondition.RepairTaskID, "review"
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID), ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1}}}
	dispatcher := &triggerDispatcherFixture{}
	if _, err := NewContinuousDispatchTriggerService(inspector, dispatcher).DispatchReviewIssueWithRecoveryPrecondition(context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(237), precondition); !errors.Is(err, ErrContinuousDispatchSourceGap) || len(dispatcher.requests) != 0 {
		t.Fatalf("missing verifier err=%v dispatches=%d", err, len(dispatcher.requests))
	}
}

func TestContinuousDispatchRecoveryTriggerRejectsVerifierReportedReviewStateDrift(t *testing.T) {
	workspaceID, projectID, issueID := dispatchReceiptUUID(240), dispatchReceiptUUID(241), dispatchReceiptUUID(242)
	repairTaskID, commentID := dispatchReceiptUUID(243), dispatchReceiptUUID(244)
	precondition := recoveryTriggerPrecondition(workspaceID, projectID, issueID, repairTaskID, commentID)
	item := triggerShadowItem(workspaceID, issueID, dispatchReceiptUUID(245), dispatchReceiptUUID(246))
	item.Status, item.SourceRef, item.SourceTaskID, item.DispatchIdentity.Stage = "in_review", precondition.SourceRef, precondition.RepairTaskID, "review"
	inspector := &triggerInspectorFixture{pages: map[int]*ContinuousDispatchShadowResult{0: {SchemaVersion: ContinuousDispatchShadowSchemaV1, WorkspaceID: shadowUUIDString(workspaceID), ProjectID: shadowUUIDString(projectID), Items: []ContinuousDispatchShadowItem{item}, Total: 1}}}
	dispatcher := &triggerDispatcherFixture{}
	verifier := &triggerRecoveryVerifierFixture{err: errors.New("review_state drifted after adapter read")}
	_, err := NewContinuousDispatchTriggerService(inspector, dispatcher).WithReviewOrphanRecoveryPreconditionVerifier(verifier).DispatchReviewIssueWithRecoveryPrecondition(context.Background(), workspaceID, projectID, issueID, dispatchReceiptUUID(247), precondition)
	if !errors.Is(err, ErrContinuousDispatchIssueDrift) || verifier.calls != 1 || len(dispatcher.requests) != 0 {
		t.Fatalf("err=%v verifier=%d dispatches=%d", err, verifier.calls, len(dispatcher.requests))
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
