package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
)

var (
	// ErrAuthorityReviewDispatchSourceGap means the server cannot prove that a
	// particular review candidate is currently authorized by HiveCosm. It is
	// deliberately distinct from a normal dispatch conflict: callers must not
	// retry it by selecting a different employee or identity locally.
	ErrAuthorityReviewDispatchSourceGap = errors.New("authority review dispatch source gap")
	// ErrAuthorityReviewDispatchDenied is an explicit current Authority denial.
	// The candidate remains untouched; any later retry must perform another
	// Authority read rather than replaying this decision.
	ErrAuthorityReviewDispatchDenied = errors.New("authority review dispatch denied")
)

// AuthorityReviewDispatchIdentity is the full, Authority-owned execution
// selector set for one review dispatch. It is intentionally not derived from
// an Issue, Task, display name, or local Agent record.
type AuthorityReviewDispatchIdentity struct {
	TenantID           string
	WorkOrderSourceRef string
	EmployeeID         string
	IdentityBindingID  string
	AgentID            string
	AssignmentID       string
}

func (i AuthorityReviewDispatchIdentity) lookup() (companyopsapi.DispatchAuthorizationLookup, error) {
	if !canonicalNonEmpty(
		i.TenantID,
		i.WorkOrderSourceRef,
		i.EmployeeID,
		i.IdentityBindingID,
		i.AgentID,
		i.AssignmentID,
	) {
		return companyopsapi.DispatchAuthorizationLookup{}, ErrAuthorityReviewDispatchSourceGap
	}
	agentID := parseDispatchUUID(i.AgentID)
	if !agentID.Valid || shadowUUIDString(agentID) != i.AgentID {
		return companyopsapi.DispatchAuthorizationLookup{}, ErrAuthorityReviewDispatchSourceGap
	}
	return companyopsapi.DispatchAuthorizationLookup{
		TenantID: i.TenantID,
		ExecutionIdentity: companyopsapi.DispatchAuthorizationExecutionIdentity{
			WorkOrderSourceRef: i.WorkOrderSourceRef,
			EmployeeID:         i.EmployeeID,
			IdentityBindingID:  i.IdentityBindingID,
			AgentID:            i.AgentID,
			AssignmentID:       i.AssignmentID,
		},
	}, nil
}

// authorityReviewDispatchCandidate deliberately hides the server-built
// ContinuousDispatchRequest. HTTP and other external callers cannot populate
// this type's payload; a future Trigger integration must construct it only
// after it has recomputed the local review route from its Shadow read.
type AuthorityReviewDispatchCandidate struct {
	request ContinuousDispatchRequest
}

func authorityReviewDispatchCandidateFromServer(request ContinuousDispatchRequest) AuthorityReviewDispatchCandidate {
	return AuthorityReviewDispatchCandidate{request: request}
}

// AuthorityReviewDispatchAuthorizer is satisfied by the strict read-only
// HiveCosmDispatchAuthorizationClient. The seam is not a local policy engine:
// it forwards all five selectors unchanged and consumes the Authority result.
type AuthorityReviewDispatchAuthorizer interface {
	Resolve(context.Context, companyopsapi.DispatchAuthorizationLookup) (companyopsapi.DispatchAuthorizationResponse, error)
}

// AuthorityReviewDispatchGate is a candidate-only wrapper around the existing
// exact dispatcher. It is intentionally not installed in the HTTP trigger or
// environment wiring yet; runtime enablement awaits a real Authority readback
// and the canonical Issue-to-work-order mapping.
type AuthorityReviewDispatchGate struct {
	authorizer AuthorityReviewDispatchAuthorizer
	dispatcher ContinuousDispatchExactDispatcher
}

func NewAuthorityReviewDispatchGate(
	authorizer AuthorityReviewDispatchAuthorizer,
	dispatcher ContinuousDispatchExactDispatcher,
) *AuthorityReviewDispatchGate {
	return &AuthorityReviewDispatchGate{authorizer: authorizer, dispatcher: dispatcher}
}

// DispatchReview performs no local selector translation. It validates that
// the already server-determined candidate is a canonical review dispatch for
// the exact Authority employee/Agent, asks Authority for event_reconcile, and
// only then delegates the unchanged request to the existing dispatcher.
func (g *AuthorityReviewDispatchGate) DispatchReview(
	ctx context.Context,
	identity AuthorityReviewDispatchIdentity,
	candidate AuthorityReviewDispatchCandidate,
) (ContinuousDispatchReceipt, error) {
	if g == nil || g.authorizer == nil || g.dispatcher == nil {
		return ContinuousDispatchReceipt{}, ErrAuthorityReviewDispatchSourceGap
	}
	lookup, err := identity.lookup()
	if err != nil {
		return ContinuousDispatchReceipt{}, err
	}
	if err := validateAuthorityReviewDispatchCandidate(identity, candidate.request); err != nil {
		return ContinuousDispatchReceipt{}, err
	}

	response, err := g.authorizer.Resolve(ctx, lookup)
	if err != nil {
		return ContinuousDispatchReceipt{}, fmt.Errorf("%w: authority read failed", ErrAuthorityReviewDispatchSourceGap)
	}
	if err := companyopsapi.ValidateDispatchAuthorizationResponseAt(response, lookup, time.Now().UTC()); err != nil {
		return ContinuousDispatchReceipt{}, fmt.Errorf("%w: authority response invalid: %v", ErrAuthorityReviewDispatchSourceGap, err)
	}
	if err := bindAuthorityReviewDispatchCandidate(identity, response, candidate.request); err != nil {
		return ContinuousDispatchReceipt{}, err
	}
	if !response.Authorization.EventReconcile.Eligible {
		return ContinuousDispatchReceipt{}, ErrAuthorityReviewDispatchDenied
	}

	return g.dispatcher.Dispatch(ctx, candidate.request)
}

func validateAuthorityReviewDispatchCandidate(
	identity AuthorityReviewDispatchIdentity,
	request ContinuousDispatchRequest,
) error {
	if err := validateContinuousDispatchRequest(request); err != nil {
		return fmt.Errorf("%w: local dispatch candidate is invalid", ErrAuthorityReviewDispatchSourceGap)
	}
	if !request.requireInReview || request.reviewProvenance == nil || request.Identity.Stage != "review" {
		return ErrAuthorityReviewDispatchSourceGap
	}
	if request.Route.EmployeeRef != continuousDispatchEmployeeRefPrefix+identity.EmployeeID {
		return ErrAuthorityReviewDispatchSourceGap
	}
	if shadowUUIDString(request.Route.LocalAgentID) != identity.AgentID {
		return ErrAuthorityReviewDispatchSourceGap
	}
	return nil
}

func bindAuthorityReviewDispatchCandidate(
	identity AuthorityReviewDispatchIdentity,
	response companyopsapi.DispatchAuthorizationResponse,
	request ContinuousDispatchRequest,
) error {
	if response.Scope.WorkspaceID == nil || *response.Scope.WorkspaceID != request.Identity.WorkspaceID ||
		response.IssueLinkage.IssueID == nil || *response.IssueLinkage.IssueID != request.Identity.IssueID ||
		response.Evidence == nil || response.Evidence.WorkOrder.SourceRef == nil || *response.Evidence.WorkOrder.SourceRef != identity.WorkOrderSourceRef {
		return ErrAuthorityReviewDispatchSourceGap
	}
	if request.reviewProvenance == nil || request.reviewProvenance.SourceIssueID != *response.IssueLinkage.IssueID {
		return ErrAuthorityReviewDispatchSourceGap
	}
	// Authority's current read contract has no candidate_revision or generation
	// fields. Those values remain protected by validateContinuousDispatchRequest
	// and the existing transactional lineage check; the upstream contract must
	// expose them before this gate can bind them to Authority evidence.
	return nil
}

var _ AuthorityReviewDispatchAuthorizer = (*companyopsapi.HiveCosmDispatchAuthorizationClient)(nil)
