package service

import (
	"context"
	"errors"
	"testing"
	"time"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
)

type authorityReviewDispatchAuthorizerFake struct {
	response companyopsapi.DispatchAuthorizationResponse
	err      error
	calls    int
	lookup   companyopsapi.DispatchAuthorizationLookup
}

func (f *authorityReviewDispatchAuthorizerFake) Resolve(
	_ context.Context,
	lookup companyopsapi.DispatchAuthorizationLookup,
) (companyopsapi.DispatchAuthorizationResponse, error) {
	f.calls++
	f.lookup = lookup
	return f.response, f.err
}

type authorityReviewDispatchDispatcherFake struct {
	receipt ContinuousDispatchReceipt
	err     error
	calls   int
	request ContinuousDispatchRequest
}

func (f *authorityReviewDispatchDispatcherFake) Dispatch(
	_ context.Context,
	request ContinuousDispatchRequest,
) (ContinuousDispatchReceipt, error) {
	f.calls++
	f.request = request
	return f.receipt, f.err
}

func authorityReviewDispatchFixture(seed byte) (AuthorityReviewDispatchIdentity, AuthorityReviewDispatchCandidate) {
	request, _ := continuousDispatchRequestFixture(seed)
	request.Identity.WorkspaceID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c010"
	request.Identity.IssueID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c011"
	request.Route.LocalAgentID = parseDispatchUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c001")
	request.Identity.Stage = "review"
	request.Route.EmployeeRef = continuousDispatchEmployeeRefPrefix + "EMP-REVIEW"
	request.requireInReview = true
	request.reviewProvenance = &ContinuousDispatchReviewProvenance{
		// dispatchReceiptUUID deliberately produces byte fixtures, not canonical
		// RFC UUIDs. Authority provenance must use the same canonical IDs that a
		// real Comment and Task carry.
		SourceRef:       continuousDispatchReviewCommentRef(parseDispatchUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c015")),
		SourceIssueID:   request.Identity.IssueID,
		SourceTaskID:    "01972f7e-7e8d-77ef-a13d-1b0ce3e9c016",
		InitiatorSource: continuousDispatchReviewInitiatorSourceV1,
	}
	identity := AuthorityReviewDispatchIdentity{
		TenantID:           "tenant-hivecosm-1",
		WorkOrderSourceRef: "hive://hivecosm/delivery/project/project-1/work-order/wo-1",
		EmployeeID:         "EMP-REVIEW",
		IdentityBindingID:  "binding-1",
		AgentID:            shadowUUIDString(request.Route.LocalAgentID),
		AssignmentID:       "assignment-1",
	}
	return identity, authorityReviewDispatchCandidateFromServer(request)
}

func authorityReviewDispatchStringPtr(value string) *string { return &value }

func authorityReviewDispatchGateResponse(lookup companyopsapi.DispatchAuthorizationLookup, request ContinuousDispatchRequest) companyopsapi.DispatchAuthorizationResponse {
	now := time.Now().UTC()
	observed := now.Add(-time.Minute).Format(time.RFC3339Nano)
	generated := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	expires := now.Add(5 * time.Minute).Format(time.RFC3339Nano)
	revision := "revision:dispatch-review-1"
	workspaceID, issueID := request.Identity.WorkspaceID, request.Identity.IssueID
	workflowID, goalID, workOrderID := "workflow-review-1", "goal-review-1", "wo-1"
	scopeRef := "hive://scope/" + lookup.TenantID + "/" + workspaceID + "/" + workflowID + "/" + goalID + "/" + workOrderID
	issueRef := "hive://issues/project-1/" + workOrderID + "/" + issueID
	assignmentRef := "hive://assignments/" + lookup.ExecutionIdentity.AssignmentID
	bindingRef := "hive://identity-bindings/" + lookup.ExecutionIdentity.IdentityBindingID
	custodyRef := "hive://custody/" + workOrderID + "/" + lookup.ExecutionIdentity.AssignmentID
	authorizationRef, ownerDecisionRef := "hive://authorizations/auth-review-1", "hive://owner-decisions/decision-review-1"
	workflowRef, goalRef := "hive://workflows/"+workflowID, "hive://goals/"+goalID
	scope := companyopsapi.DispatchAuthorizationScope{State: "OBSERVED", TenantID: authorityReviewDispatchStringPtr(lookup.TenantID), WorkspaceID: authorityReviewDispatchStringPtr(workspaceID), WorkflowID: authorityReviewDispatchStringPtr(workflowID), GoalID: authorityReviewDispatchStringPtr(goalID), WorkOrderID: authorityReviewDispatchStringPtr(workOrderID), SourceRef: authorityReviewDispatchStringPtr(scopeRef), SourceRevision: authorityReviewDispatchStringPtr(revision), ObservedAt: observed, SourceGeneratedAt: authorityReviewDispatchStringPtr(generated), Freshness: "current", ExpiresAt: authorityReviewDispatchStringPtr(expires)}
	issue := companyopsapi.DispatchAuthorizationIssueLinkage{State: "OBSERVED", IssueID: authorityReviewDispatchStringPtr(issueID), ProjectID: authorityReviewDispatchStringPtr("project-1"), WorkOrderID: authorityReviewDispatchStringPtr(workOrderID), SourceRef: authorityReviewDispatchStringPtr(issueRef), SourceRevision: authorityReviewDispatchStringPtr(revision), ObservedAt: observed, SourceGeneratedAt: authorityReviewDispatchStringPtr(generated), Freshness: "current", ExpiresAt: authorityReviewDispatchStringPtr(expires)}
	response := companyopsapi.DispatchAuthorizationResponse{
		SchemaVersion:     companyopsapi.HiveCosmDispatchAuthorizationSchema,
		OK:                true,
		ReadOnly:          true,
		TenantID:          lookup.TenantID,
		Request:           lookup,
		ExecutionIdentity: lookup.ExecutionIdentity,
		Scope:             scope,
		IssueLinkage:      issue,
	}
	response.Evidence = &companyopsapi.DispatchAuthorizationEvidence{Scope: scope, IssueLinkage: issue}
	e := response.Evidence
	e.WorkOrder.State, e.WorkOrder.SourceRef, e.WorkOrder.SourceRevision = "OBSERVED", authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.WorkOrderSourceRef), authorityReviewDispatchStringPtr(revision)
	e.WorkOrder.WorkOrderID, e.WorkOrder.ProjectID = authorityReviewDispatchStringPtr(workOrderID), authorityReviewDispatchStringPtr("project-1")
	e.WorkOrder.ObservedAt, e.WorkOrder.SourceGeneratedAt, e.WorkOrder.Freshness, e.WorkOrder.ExpiresAt = observed, authorityReviewDispatchStringPtr(generated), "current", authorityReviewDispatchStringPtr(expires)
	e.Assignment.State, e.Assignment.SourceRef, e.Assignment.SourceRevision = "OBSERVED", authorityReviewDispatchStringPtr(assignmentRef), authorityReviewDispatchStringPtr(revision)
	e.Assignment.WorkOrderID, e.Assignment.AssignmentID, e.Assignment.EmployeeID, e.Assignment.AgentID = authorityReviewDispatchStringPtr(workOrderID), authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.AssignmentID), authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.EmployeeID), authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.AgentID)
	e.Assignment.ObservedAt, e.Assignment.SourceGeneratedAt, e.Assignment.Freshness, e.Assignment.ExpiresAt = observed, authorityReviewDispatchStringPtr(generated), "current", authorityReviewDispatchStringPtr(expires)
	e.IdentityBinding.State, e.IdentityBinding.SourceRef, e.IdentityBinding.SourceRevision = "OBSERVED", authorityReviewDispatchStringPtr(bindingRef), authorityReviewDispatchStringPtr(revision)
	e.IdentityBinding.IdentityBindingID, e.IdentityBinding.EmployeeID, e.IdentityBinding.AgentID, e.IdentityBinding.Active = authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.IdentityBindingID), authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.EmployeeID), authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.AgentID), true
	e.IdentityBinding.ObservedAt, e.IdentityBinding.SourceGeneratedAt, e.IdentityBinding.Freshness, e.IdentityBinding.ExpiresAt = observed, authorityReviewDispatchStringPtr(generated), "current", authorityReviewDispatchStringPtr(expires)
	e.Custody.State, e.Custody.SourceRef, e.Custody.SourceRevision = "OBSERVED", authorityReviewDispatchStringPtr(custodyRef), authorityReviewDispatchStringPtr(revision)
	e.Custody.WorkOrderID, e.Custody.AssignmentID, e.Custody.EmployeeID, e.Custody.AgentID = authorityReviewDispatchStringPtr(workOrderID), authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.AssignmentID), authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.EmployeeID), authorityReviewDispatchStringPtr(lookup.ExecutionIdentity.AgentID)
	e.Custody.Gaps, e.Custody.Conflicts = []string{}, []string{}
	e.Custody.ObservedAt, e.Custody.SourceGeneratedAt, e.Custody.Freshness, e.Custody.ExpiresAt = observed, authorityReviewDispatchStringPtr(generated), "current", authorityReviewDispatchStringPtr(expires)
	e.ContinuousWorkflowAuthorization.State, e.ContinuousWorkflowAuthorization.Scope = "AUTHORIZED", authorityReviewDispatchStringPtr("event_reconcile")
	e.ContinuousWorkflowAuthorization.WorkflowID, e.ContinuousWorkflowAuthorization.GoalID, e.ContinuousWorkflowAuthorization.WorkOrderID = authorityReviewDispatchStringPtr(workflowID), authorityReviewDispatchStringPtr(goalID), authorityReviewDispatchStringPtr(workOrderID)
	e.ContinuousWorkflowAuthorization.OwnerDecisionRef, e.ContinuousWorkflowAuthorization.SourceRef, e.ContinuousWorkflowAuthorization.SourceRevision = authorityReviewDispatchStringPtr(ownerDecisionRef), authorityReviewDispatchStringPtr(authorizationRef), authorityReviewDispatchStringPtr(revision)
	e.ContinuousWorkflowAuthorization.ObservedAt, e.ContinuousWorkflowAuthorization.SourceGeneratedAt, e.ContinuousWorkflowAuthorization.Freshness, e.ContinuousWorkflowAuthorization.ExpiresAt = observed, authorityReviewDispatchStringPtr(generated), "current", authorityReviewDispatchStringPtr(expires)
	e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.State, e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.OwnerDecisionRef, e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.SourceRef, e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.SourceRevision = "OBSERVED", authorityReviewDispatchStringPtr(ownerDecisionRef), authorityReviewDispatchStringPtr(ownerDecisionRef), authorityReviewDispatchStringPtr(revision)
	e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.ObservedAt, e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.SourceGeneratedAt, e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.Freshness, e.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.ExpiresAt = observed, authorityReviewDispatchStringPtr(generated), "current", authorityReviewDispatchStringPtr(expires)
	e.WorkflowAuthority.State, e.WorkflowAuthority.WorkflowID, e.WorkflowAuthority.SourceRef, e.WorkflowAuthority.SourceRevision = "OBSERVED", authorityReviewDispatchStringPtr(workflowID), authorityReviewDispatchStringPtr(workflowRef), authorityReviewDispatchStringPtr(revision)
	e.WorkflowAuthority.ObservedAt, e.WorkflowAuthority.SourceGeneratedAt, e.WorkflowAuthority.Freshness, e.WorkflowAuthority.ExpiresAt = observed, authorityReviewDispatchStringPtr(generated), "current", authorityReviewDispatchStringPtr(expires)
	e.GoalAuthority.State, e.GoalAuthority.GoalID, e.GoalAuthority.SourceRef, e.GoalAuthority.SourceRevision = "OBSERVED", authorityReviewDispatchStringPtr(goalID), authorityReviewDispatchStringPtr(goalRef), authorityReviewDispatchStringPtr(revision)
	e.GoalAuthority.ObservedAt, e.GoalAuthority.SourceGeneratedAt, e.GoalAuthority.Freshness, e.GoalAuthority.ExpiresAt = observed, authorityReviewDispatchStringPtr(generated), "current", authorityReviewDispatchStringPtr(expires)
	provenance := request.reviewProvenance
	e.ReviewDispatch = companyopsapi.DispatchReviewDispatchEvidence{
		State: "OBSERVED",
		Records: []companyopsapi.DispatchReviewDispatchEvidenceRecord{{
			ReviewDispatchID: "review-dispatch-1",
			SourceRef:        authorityReviewDispatchStringPtr("hive://review-dispatches/review-dispatch-1"),
			SourceRevision:   authorityReviewDispatchStringPtr(revision),
			ObservedAt:       observed, SourceGeneratedAt: authorityReviewDispatchStringPtr(generated),
			Freshness: "current", ExpiresAt: authorityReviewDispatchStringPtr(expires),
			WorkspaceID: workspaceID, IssueID: issueID, Stage: "review",
			CandidateRevision: request.Identity.CandidateRevision, Generation: request.Identity.Generation,
			SourceCommentRef: provenance.SourceRef, SourceIssueID: provenance.SourceIssueID,
			SourceTaskID: provenance.SourceTaskID, InitiatorSource: provenance.InitiatorSource,
		}},
	}
	sourceRefs := []string{scopeRef, issueRef, lookup.ExecutionIdentity.WorkOrderSourceRef, assignmentRef, bindingRef, custodyRef, authorizationRef, ownerDecisionRef, workflowRef, goalRef}
	response.Authorization.EventReconcile = companyopsapi.DispatchAuthorizationDecision{Eligible: true, Reason: "eligible:all_required_authority_evidence_current", SourceRefs: sourceRefs, SourceRevisions: []string{revision}, ObservedAt: observed, Freshness: "current", ExpiresAt: authorityReviewDispatchStringPtr(expires)}
	response.Authorization.RecoveryOnly = companyopsapi.DispatchAuthorizationDecision{Eligible: false, Reason: "blocked:authorization_scope_mismatch", SourceRefs: sourceRefs, SourceRevisions: []string{revision}, ObservedAt: observed, Freshness: "current", ExpiresAt: authorityReviewDispatchStringPtr(expires)}
	return response
}

func authorityReviewDirectProjectGateResponse(lookup companyopsapi.DispatchAuthorizationLookup, request ContinuousDispatchRequest) companyopsapi.DispatchAuthorizationResponse {
	r := authorityReviewDispatchGateResponse(lookup, request)
	e := r.Evidence
	eventID, recoveryID := "direct-review-event", "direct-review-recovery"
	eventRef := "hive://hivecosm/direct-dispatch-authorizations/" + eventID
	recoveryRef := "hive://hivecosm/direct-dispatch-authorizations/" + recoveryID
	eventRevision, recoveryRevision := "revision:"+eventID+":1", "revision:"+recoveryID+":1"
	observed, generated, expires := e.Scope.ObservedAt, *e.Scope.SourceGeneratedAt, *e.Scope.ExpiresAt
	authorizationKind := "direct_project"
	e.Scope.AuthorizationKind, e.Scope.WorkflowID, e.Scope.GoalID = &authorizationKind, nil, nil
	e.Scope.SourceRef, e.Scope.SourceRevision = &eventRef, &eventRevision
	e.IssueLinkage.SourceRef, e.IssueLinkage.SourceRevision = &eventRef, &eventRevision
	e.WorkOrder.SourceRevision = &eventRevision
	e.Assignment.SourceRef, e.Assignment.SourceRevision = &eventRef, &eventRevision
	e.IdentityBinding.SourceRef, e.IdentityBinding.SourceRevision = &eventRef, &eventRevision
	e.Custody.SourceRef, e.Custody.SourceRevision = &eventRef, &eventRevision
	e.ContinuousWorkflowAuthorization.State, e.ContinuousWorkflowAuthorization.Scope = "SOURCE_UNAVAILABLE", nil
	e.ContinuousWorkflowAuthorization.WorkflowID, e.ContinuousWorkflowAuthorization.GoalID = nil, nil
	e.ContinuousWorkflowAuthorization.WorkOrderID, e.ContinuousWorkflowAuthorization.OwnerDecisionRef = nil, nil
	e.ContinuousWorkflowAuthorization.SourceRef, e.ContinuousWorkflowAuthorization.SourceRevision = nil, nil
	e.ContinuousWorkflowAuthorization.SourceGeneratedAt, e.ContinuousWorkflowAuthorization.ExpiresAt = nil, nil
	e.ContinuousWorkflowAuthorization.Freshness = "unknown"
	e.WorkflowAuthority.State, e.WorkflowAuthority.WorkflowID, e.WorkflowAuthority.SourceRef, e.WorkflowAuthority.SourceRevision = "SOURCE_UNAVAILABLE", nil, nil, nil
	e.WorkflowAuthority.SourceGeneratedAt, e.WorkflowAuthority.ExpiresAt, e.WorkflowAuthority.Freshness = nil, nil, "unknown"
	e.GoalAuthority.State, e.GoalAuthority.GoalID, e.GoalAuthority.SourceRef, e.GoalAuthority.SourceRevision = "SOURCE_UNAVAILABLE", nil, nil, nil
	e.GoalAuthority.SourceGeneratedAt, e.GoalAuthority.ExpiresAt, e.GoalAuthority.Freshness = nil, nil, "unknown"
	e.DirectProjectAuthorization = companyopsapi.DispatchDirectProjectAuthorizationEvidence{State: "OBSERVED", Records: []companyopsapi.DispatchDirectProjectAuthorizationRecord{
		{DispatchScope: "event_reconcile", DirectDispatchAuthorizationID: eventID, OwnerDecisionRef: "hive://owner-decisions/direct-review-event", ProjectID: *e.IssueLinkage.ProjectID, WorkOrderID: *e.Scope.WorkOrderID, IssueID: *e.IssueLinkage.IssueID, WorkspaceID: *e.Scope.WorkspaceID, SourceRef: eventRef, SourceRevision: eventRevision, ObservedAt: observed, SourceGeneratedAt: generated, Freshness: "current", ExpiresAt: expires},
		{DispatchScope: "recovery_only", DirectDispatchAuthorizationID: recoveryID, OwnerDecisionRef: "hive://owner-decisions/direct-review-recovery", ProjectID: *e.IssueLinkage.ProjectID, WorkOrderID: *e.Scope.WorkOrderID, IssueID: *e.IssueLinkage.IssueID, WorkspaceID: *e.Scope.WorkspaceID, SourceRef: recoveryRef, SourceRevision: recoveryRevision, ObservedAt: observed, SourceGeneratedAt: generated, Freshness: "current", ExpiresAt: expires},
	}}
	e.ReviewDispatch = companyopsapi.DispatchReviewDispatchEvidence{State: "SOURCE_UNAVAILABLE", Records: []companyopsapi.DispatchReviewDispatchEvidenceRecord{}}
	r.Scope, r.IssueLinkage = e.Scope, e.IssueLinkage
	r.Authorization.EventReconcile = companyopsapi.DispatchAuthorizationDecision{Eligible: true, Reason: "eligible:all_required_direct_project_authority_evidence_current", SourceRefs: []string{eventRef, lookup.ExecutionIdentity.WorkOrderSourceRef}, SourceRevisions: []string{eventRevision}, ObservedAt: observed, Freshness: "current", ExpiresAt: &expires}
	r.Authorization.RecoveryOnly = companyopsapi.DispatchAuthorizationDecision{Eligible: true, Reason: "eligible:all_required_direct_project_authority_evidence_current", SourceRefs: []string{eventRef, lookup.ExecutionIdentity.WorkOrderSourceRef, recoveryRef}, SourceRevisions: []string{eventRevision, recoveryRevision}, ObservedAt: observed, Freshness: "current", ExpiresAt: &expires}
	return r
}

func TestAuthorityReviewDispatchGateForwardsOnlyExactAuthorizedCandidate(t *testing.T) {
	identity, candidate := authorityReviewDispatchFixture(210)
	lookup, err := identity.lookup()
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &authorityReviewDispatchAuthorizerFake{response: authorityReviewDispatchGateResponse(lookup, candidate.request)}
	wantReceipt := dispatchReceiptFixture(217)
	dispatcher := &authorityReviewDispatchDispatcherFake{receipt: wantReceipt}

	got, err := NewAuthorityReviewDispatchGate(authorizer, dispatcher).DispatchReview(context.Background(), identity, candidate)
	if err != nil {
		t.Fatalf("DispatchReview: %v", err)
	}
	if got != wantReceipt || authorizer.calls != 1 || dispatcher.calls != 1 {
		t.Fatalf("receipt/calls = %#v/%d/%d, want exact receipt/1/1", got, authorizer.calls, dispatcher.calls)
	}
	if authorizer.lookup != lookup || dispatcher.request != candidate.request {
		t.Fatal("gate changed an Authority selector or server-determined candidate")
	}
}

func TestAuthorityReviewDispatchGateUsesDirectProjectAuthorityWithoutCopyingLocalLineage(t *testing.T) {
	identity, candidate := authorityReviewDispatchFixture(211)
	lookup, err := identity.lookup()
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &authorityReviewDispatchAuthorizerFake{response: authorityReviewDirectProjectGateResponse(lookup, candidate.request)}
	dispatcher := &authorityReviewDispatchDispatcherFake{receipt: dispatchReceiptFixture(218)}
	if _, err := NewAuthorityReviewDispatchGate(authorizer, dispatcher).DispatchReview(context.Background(), identity, candidate); err != nil {
		t.Fatalf("DispatchReview direct-project: %v", err)
	}
	if authorizer.calls != 1 || dispatcher.calls != 1 {
		t.Fatalf("calls = %d/%d, want 1/1", authorizer.calls, dispatcher.calls)
	}
}

func TestAuthorityReviewDispatchGateFailsClosedBeforeDispatcher(t *testing.T) {
	identity, candidate := authorityReviewDispatchFixture(220)
	lookup, err := identity.lookup()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		gate          func(*authorityReviewDispatchAuthorizerFake, *authorityReviewDispatchDispatcherFake) *AuthorityReviewDispatchGate
		identity      AuthorityReviewDispatchIdentity
		candidate     AuthorityReviewDispatchCandidate
		response      companyopsapi.DispatchAuthorizationResponse
		authorizerErr error
		want          error
		wantAuthCalls int
	}{
		{
			name: "nil authorizer", gate: func(_ *authorityReviewDispatchAuthorizerFake, d *authorityReviewDispatchDispatcherFake) *AuthorityReviewDispatchGate {
				return NewAuthorityReviewDispatchGate(nil, d)
			}, identity: identity, candidate: candidate, want: ErrAuthorityReviewDispatchSourceGap,
		},
		{
			name: "nil dispatcher", gate: func(a *authorityReviewDispatchAuthorizerFake, _ *authorityReviewDispatchDispatcherFake) *AuthorityReviewDispatchGate {
				return NewAuthorityReviewDispatchGate(a, nil)
			}, identity: identity, candidate: candidate, response: authorityReviewDispatchGateResponse(lookup, candidate.request), want: ErrAuthorityReviewDispatchSourceGap,
		},
		{
			name: "incomplete five selector identity", gate: func(a *authorityReviewDispatchAuthorizerFake, d *authorityReviewDispatchDispatcherFake) *AuthorityReviewDispatchGate {
				return NewAuthorityReviewDispatchGate(a, d)
			}, identity: AuthorityReviewDispatchIdentity{TenantID: identity.TenantID, WorkOrderSourceRef: identity.WorkOrderSourceRef, EmployeeID: identity.EmployeeID, IdentityBindingID: identity.IdentityBindingID, AgentID: identity.AgentID}, candidate: candidate, want: ErrAuthorityReviewDispatchSourceGap,
		},
		{
			name: "candidate employee mismatch", gate: func(a *authorityReviewDispatchAuthorizerFake, d *authorityReviewDispatchDispatcherFake) *AuthorityReviewDispatchGate {
				return NewAuthorityReviewDispatchGate(a, d)
			}, identity: AuthorityReviewDispatchIdentity{TenantID: identity.TenantID, WorkOrderSourceRef: identity.WorkOrderSourceRef, EmployeeID: "EMP-OTHER", IdentityBindingID: identity.IdentityBindingID, AgentID: identity.AgentID, AssignmentID: identity.AssignmentID}, candidate: candidate, want: ErrAuthorityReviewDispatchSourceGap,
		},
		{
			name: "authorizer error", gate: func(a *authorityReviewDispatchAuthorizerFake, d *authorityReviewDispatchDispatcherFake) *AuthorityReviewDispatchGate {
				return NewAuthorityReviewDispatchGate(a, d)
			}, identity: identity, candidate: candidate, authorizerErr: errors.New("authority unavailable"), want: ErrAuthorityReviewDispatchSourceGap, wantAuthCalls: 1,
		},
		{
			name: "explicit deny", gate: func(a *authorityReviewDispatchAuthorizerFake, d *authorityReviewDispatchDispatcherFake) *AuthorityReviewDispatchGate {
				return NewAuthorityReviewDispatchGate(a, d)
			}, identity: identity, candidate: candidate, response: func() companyopsapi.DispatchAuthorizationResponse {
				r := authorityReviewDispatchGateResponse(lookup, candidate.request)
				r.Authorization.EventReconcile.Eligible = false
				r.Authorization.EventReconcile.Reason = "blocked:authorization_scope_mismatch"
				r.Authorization.RecoveryOnly.Eligible = true
				r.Authorization.RecoveryOnly.Reason = "eligible:all_required_authority_evidence_current"
				r.Evidence.ContinuousWorkflowAuthorization.Scope = authorityReviewDispatchStringPtr("recovery_only")
				return r
			}(), want: ErrAuthorityReviewDispatchDenied, wantAuthCalls: 1,
		},
		{
			name: "authorizer response selector drift", gate: func(a *authorityReviewDispatchAuthorizerFake, d *authorityReviewDispatchDispatcherFake) *AuthorityReviewDispatchGate {
				return NewAuthorityReviewDispatchGate(a, d)
			}, identity: identity, candidate: candidate, response: func() companyopsapi.DispatchAuthorizationResponse {
				r := authorityReviewDispatchGateResponse(lookup, candidate.request)
				r.ExecutionIdentity.AgentID = shadowUUIDString(dispatchReceiptUUID(249))
				return r
			}(), want: ErrAuthorityReviewDispatchSourceGap, wantAuthCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer := &authorityReviewDispatchAuthorizerFake{response: test.response, err: test.authorizerErr}
			dispatcher := &authorityReviewDispatchDispatcherFake{}
			_, err := test.gate(authorizer, dispatcher).DispatchReview(context.Background(), test.identity, test.candidate)
			if !errors.Is(err, test.want) {
				t.Fatalf("DispatchReview error = %v, want %v", err, test.want)
			}
			if authorizer.calls != test.wantAuthCalls || dispatcher.calls != 0 {
				t.Fatalf("authorizer/dispatcher calls = %d/%d, want %d/0", authorizer.calls, dispatcher.calls, test.wantAuthCalls)
			}
		})
	}
}

func TestAuthorityReviewDispatchGateRequiresEveryAuthoritySelectorBeforeRead(t *testing.T) {
	identity, candidate := authorityReviewDispatchFixture(250)
	for _, mutate := range []struct {
		name  string
		apply func(*AuthorityReviewDispatchIdentity)
	}{
		{name: "tenant", apply: func(i *AuthorityReviewDispatchIdentity) { i.TenantID = "" }},
		{name: "work order source ref", apply: func(i *AuthorityReviewDispatchIdentity) { i.WorkOrderSourceRef = "" }},
		{name: "employee", apply: func(i *AuthorityReviewDispatchIdentity) { i.EmployeeID = "" }},
		{name: "identity binding", apply: func(i *AuthorityReviewDispatchIdentity) { i.IdentityBindingID = "" }},
		{name: "agent", apply: func(i *AuthorityReviewDispatchIdentity) { i.AgentID = "" }},
		{name: "assignment", apply: func(i *AuthorityReviewDispatchIdentity) { i.AssignmentID = "" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			broken := identity
			mutate.apply(&broken)
			authorizer := &authorityReviewDispatchAuthorizerFake{}
			dispatcher := &authorityReviewDispatchDispatcherFake{}
			_, err := NewAuthorityReviewDispatchGate(authorizer, dispatcher).DispatchReview(context.Background(), broken, candidate)
			if !errors.Is(err, ErrAuthorityReviewDispatchSourceGap) {
				t.Fatalf("DispatchReview error = %v, want source gap", err)
			}
			if authorizer.calls != 0 || dispatcher.calls != 0 {
				t.Fatalf("authorizer/dispatcher calls = %d/%d, want 0/0", authorizer.calls, dispatcher.calls)
			}
		})
	}
}

func TestAuthorityReviewDispatchGateRejectsAuthorityEvidenceGapsBeforeDispatcher(t *testing.T) {
	identity, candidate := authorityReviewDispatchFixture(160)
	lookup, err := identity.lookup()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*companyopsapi.DispatchAuthorizationResponse)
	}{
		{name: "missing evidence", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) { r.Evidence = nil }},
		{name: "scope evidence mismatch", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			r.Evidence.Scope.WorkspaceID = authorityReviewDispatchStringPtr("01972f7e-7e8d-77ef-a13d-1b0ce3e9c012")
		}},
		{name: "issue linkage mismatch", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			r.IssueLinkage.IssueID = authorityReviewDispatchStringPtr("01972f7e-7e8d-77ef-a13d-1b0ce3e9c012")
		}},
		{name: "expired scope evidence", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			expired := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
			r.Scope.ExpiresAt, r.Evidence.Scope.ExpiresAt = authorityReviewDispatchStringPtr(expired), authorityReviewDispatchStringPtr(expired)
		}},
		{name: "missing decision expiry", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) { r.Authorization.EventReconcile.ExpiresAt = nil }},
		{name: "missing evidence expiry", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) { r.Evidence.WorkOrder.ExpiresAt = nil }},
		{name: "malformed decision provenance", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			r.Authorization.EventReconcile.SourceRefs = []string{"hive://malformed"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := authorityReviewDispatchGateResponse(lookup, candidate.request)
			test.mutate(&response)
			authorizer := &authorityReviewDispatchAuthorizerFake{response: response}
			dispatcher := &authorityReviewDispatchDispatcherFake{}
			_, err := NewAuthorityReviewDispatchGate(authorizer, dispatcher).DispatchReview(context.Background(), identity, candidate)
			if !errors.Is(err, ErrAuthorityReviewDispatchSourceGap) {
				t.Fatalf("DispatchReview error = %v, want source gap", err)
			}
			if dispatcher.calls != 0 {
				t.Fatalf("dispatcher calls = %d, want 0", dispatcher.calls)
			}
		})
	}
}

func TestAuthorityReviewDispatchGateBindsCandidateToAuthorityIssueAndWorkspace(t *testing.T) {
	identity, candidate := authorityReviewDispatchFixture(170)
	lookup, err := identity.lookup()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*ContinuousDispatchRequest, *companyopsapi.DispatchAuthorizationResponse)
	}{
		{name: "candidate workspace mismatch", mutate: func(req *ContinuousDispatchRequest, _ *companyopsapi.DispatchAuthorizationResponse) {
			req.Identity.WorkspaceID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c012"
		}},
		{name: "candidate issue mismatch", mutate: func(req *ContinuousDispatchRequest, _ *companyopsapi.DispatchAuthorizationResponse) {
			req.Identity.IssueID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c012"
		}},
		{name: "review provenance issue mismatch", mutate: func(_ *ContinuousDispatchRequest, r *companyopsapi.DispatchAuthorizationResponse) {
			r.IssueLinkage.IssueID = authorityReviewDispatchStringPtr("01972f7e-7e8d-77ef-a13d-1b0ce3e9c012")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := candidate.request
			response := authorityReviewDispatchGateResponse(lookup, request)
			test.mutate(&request, &response)
			candidate := authorityReviewDispatchCandidateFromServer(request)
			authorizer := &authorityReviewDispatchAuthorizerFake{response: response}
			dispatcher := &authorityReviewDispatchDispatcherFake{}
			_, err := NewAuthorityReviewDispatchGate(authorizer, dispatcher).DispatchReview(context.Background(), identity, candidate)
			if !errors.Is(err, ErrAuthorityReviewDispatchSourceGap) {
				t.Fatalf("DispatchReview error = %v, want source gap", err)
			}
			if dispatcher.calls != 0 {
				t.Fatalf("dispatcher calls = %d, want 0", dispatcher.calls)
			}
		})
	}
}

func TestAuthorityReviewDispatchGateRequiresOneExactAuthorityReviewProvenanceRecord(t *testing.T) {
	identity, candidate := authorityReviewDispatchFixture(180)
	lookup, err := identity.lookup()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*companyopsapi.DispatchAuthorizationResponse)
	}{
		{name: "unavailable", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			r.Evidence.ReviewDispatch = companyopsapi.DispatchReviewDispatchEvidence{State: "SOURCE_UNAVAILABLE", Records: []companyopsapi.DispatchReviewDispatchEvidenceRecord{}}
		}},
		{name: "candidate revision", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			r.Evidence.ReviewDispatch.Records[0].CandidateRevision = "candidate-other"
		}},
		{name: "generation", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			r.Evidence.ReviewDispatch.Records[0].Generation = "generation-other"
		}},
		{name: "comment ref", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			r.Evidence.ReviewDispatch.Records[0].SourceCommentRef = continuousDispatchReviewCommentRef(dispatchReceiptUUID(250))
		}},
		{name: "source task", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			r.Evidence.ReviewDispatch.Records[0].SourceTaskID = shadowUUIDString(dispatchReceiptUUID(251))
		}},
		{name: "duplicate exact record", mutate: func(r *companyopsapi.DispatchAuthorizationResponse) {
			r.Evidence.ReviewDispatch.Records = append(r.Evidence.ReviewDispatch.Records, r.Evidence.ReviewDispatch.Records[0])
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := authorityReviewDispatchGateResponse(lookup, candidate.request)
			tt.mutate(&response)
			authorizer := &authorityReviewDispatchAuthorizerFake{response: response}
			dispatcher := &authorityReviewDispatchDispatcherFake{}
			_, err := NewAuthorityReviewDispatchGate(authorizer, dispatcher).DispatchReview(context.Background(), identity, candidate)
			if !errors.Is(err, ErrAuthorityReviewDispatchSourceGap) || dispatcher.calls != 0 {
				t.Fatalf("DispatchReview error/calls = %v/%d, want source gap/0", err, dispatcher.calls)
			}
		})
	}
}
