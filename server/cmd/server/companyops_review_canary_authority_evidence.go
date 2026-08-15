package main

import (
	"log/slog"
	"context"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/service"
)

// Option C per William's 2026-08-15 decision: the review reconciler's
// authority-evidence seam resolved through the exact-scope canary identity,
// one read-only Authority dispatch-authorization call, and the author
// identity as recorded on the Authority's own assignment for this work
// order (delivered via env until the Option A by-issue lookup returns it).
// The provider performs no writes, creates no Tasks, and fails closed on
// any incomplete input.
type reviewCanaryAuthorityEvidenceProvider struct {
	identities service.AuthorityReviewDispatchIdentityProvider
	authorize  func(context.Context, service.AuthorityReviewDispatchIdentity) (companyops.DispatchAuthorizationResponse, error)
	authorEmployeeID string
	authorAgentID    string
}

func reviewCanaryAuthorityEvidenceProviderFromEnv(
	identities service.AuthorityReviewDispatchIdentityProvider,
	authorize func(context.Context, service.AuthorityReviewDispatchIdentity) (companyops.DispatchAuthorizationResponse, error),
) *reviewCanaryAuthorityEvidenceProvider {
	authorEmployee := strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_AUTHOR_EMPLOYEE_ID"))
	authorAgent := strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_AUTHOR_AGENT_ID"))
	if identities == nil || authorize == nil || authorEmployee == "" || authorAgent == "" {
		return nil
	}
	return &reviewCanaryAuthorityEvidenceProvider{
		identities:       identities,
		authorize:        authorize,
		authorEmployeeID: authorEmployee,
		authorAgentID:    authorAgent,
	}
}

func (p *reviewCanaryAuthorityEvidenceProvider) ResolveReviewReconcileEvidence(
	ctx context.Context,
	workspaceID, projectID pgtype.UUID,
	candidate service.ReviewReconcileCandidate,
) (service.ReviewReconcileAuthorityEvidence, error) {
	evidence := service.ReviewReconcileAuthorityEvidence{}
	if p == nil {
		return evidence, nil
	}
	parsedIssue, issueErr := uuid.Parse(strings.TrimSpace(candidate.IssueID))
	if issueErr != nil {
		return evidence, nil
	}
	issueID := pgtype.UUID{Bytes: parsedIssue, Valid: true}
	identity, err := p.identities.ResolveReviewDispatchIdentity(ctx, workspaceID, projectID, issueID, service.AuthorityReviewDispatchCandidate{})
	if err != nil {
		slog.Warn("review canary evidence: identity resolution failed", "error", err)
		return evidence, nil
	}
	response, err := p.authorize(ctx, identity)
	if err != nil {
		slog.Warn("review canary evidence: authority resolve failed", "error", err)
		return evidence, nil
	}
	evidence.AuthorityKnown = true
	evidence.AuthorityEligible = response.Authorization.EventReconcile.Eligible && response.Authorization.RecoveryOnly.Eligible
	evidence.AuthorKnown = true
	evidence.AuthorEmployeeID = p.authorEmployeeID
	evidence.AuthorAgentID = p.authorAgentID
	evidence.Reviewer = service.ReviewReconcileReviewer{
		Known:       true,
		Healthy:     evidence.AuthorityEligible,
		Independent: identity.EmployeeID != p.authorEmployeeID && identity.AgentID != p.authorAgentID,
		EmployeeID:  identity.EmployeeID,
		AgentID:     identity.AgentID,
	}
	return evidence, nil
}

// canaryReviewAuthorize adapts the runtime dispatch-authorization client to
// the provider seam without exposing the underlying HTTP machinery.
func canaryReviewAuthorize(client interface {
	Resolve(context.Context, companyops.DispatchAuthorizationLookup) (companyops.DispatchAuthorizationResponse, error)
}, tenant string) func(context.Context, service.AuthorityReviewDispatchIdentity) (companyops.DispatchAuthorizationResponse, error) {
	return func(ctx context.Context, identity service.AuthorityReviewDispatchIdentity) (companyops.DispatchAuthorizationResponse, error) {
		return client.Resolve(ctx, companyops.DispatchAuthorizationLookup{
			TenantID: tenant,
			ExecutionIdentity: companyops.DispatchAuthorizationExecutionIdentity{
				WorkOrderSourceRef: identity.WorkOrderSourceRef,
				EmployeeID:         identity.EmployeeID,
				IdentityBindingID:  identity.IdentityBindingID,
				AgentID:            identity.AgentID,
				AssignmentID:       identity.AssignmentID,
			},
		})
	}
}
