package service

import (
	"context"
	"errors"
	"testing"

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
	request.Identity.Stage = "review"
	request.Route.EmployeeRef = continuousDispatchEmployeeRefPrefix + "EMP-REVIEW"
	request.requireInReview = true
	request.reviewProvenance = &ContinuousDispatchReviewProvenance{
		SourceRef:       continuousDispatchReviewCommentRef(dispatchReceiptUUID(seed + 5)),
		SourceIssueID:   request.Identity.IssueID,
		SourceTaskID:    shadowUUIDString(dispatchReceiptUUID(seed + 6)),
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

func authorityReviewDispatchResponse(lookup companyopsapi.DispatchAuthorizationLookup) companyopsapi.DispatchAuthorizationResponse {
	response := companyopsapi.DispatchAuthorizationResponse{
		SchemaVersion:     companyopsapi.HiveCosmDispatchAuthorizationSchema,
		OK:                true,
		ReadOnly:          true,
		TenantID:          lookup.TenantID,
		Request:           lookup,
		ExecutionIdentity: lookup.ExecutionIdentity,
	}
	response.Authorization.EventReconcile.Eligible = true
	return response
}

func TestAuthorityReviewDispatchGateForwardsOnlyExactAuthorizedCandidate(t *testing.T) {
	identity, candidate := authorityReviewDispatchFixture(210)
	lookup, err := identity.lookup()
	if err != nil {
		t.Fatal(err)
	}
	authorizer := &authorityReviewDispatchAuthorizerFake{response: authorityReviewDispatchResponse(lookup)}
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
			}, identity: identity, candidate: candidate, response: authorityReviewDispatchResponse(lookup), want: ErrAuthorityReviewDispatchSourceGap,
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
			}, identity: identity, candidate: candidate, response: companyopsapi.DispatchAuthorizationResponse{SchemaVersion: companyopsapi.HiveCosmDispatchAuthorizationSchema, OK: true, ReadOnly: true, TenantID: lookup.TenantID, Request: lookup, ExecutionIdentity: lookup.ExecutionIdentity}, want: ErrAuthorityReviewDispatchDenied, wantAuthCalls: 1,
		},
		{
			name: "authorizer response selector drift", gate: func(a *authorityReviewDispatchAuthorizerFake, d *authorityReviewDispatchDispatcherFake) *AuthorityReviewDispatchGate {
				return NewAuthorityReviewDispatchGate(a, d)
			}, identity: identity, candidate: candidate, response: func() companyopsapi.DispatchAuthorizationResponse {
				r := authorityReviewDispatchResponse(lookup)
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
