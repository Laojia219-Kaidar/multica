package main

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/service"
)

// byIssueReviewAuthorityEvidenceProvider joins two Authority-owned, read-only
// delegations with HiveCrew's server-built candidate. Authority proves the
// implementation and review identities; HiveCrew remains the sole owner of
// Task/Comment/revision/generation lineage and revalidates it transactionally.
type byIssueReviewAuthorityEvidenceProvider struct {
	identities *byIssueIdentityProvider
	authorize  func(context.Context, service.AuthorityReviewDispatchIdentity) (companyops.DispatchAuthorizationResponse, error)
}

func newByIssueReviewAuthorityEvidenceProvider(
	identities *byIssueIdentityProvider,
	authorize func(context.Context, service.AuthorityReviewDispatchIdentity) (companyops.DispatchAuthorizationResponse, error),
) *byIssueReviewAuthorityEvidenceProvider {
	if identities == nil || authorize == nil {
		return nil
	}
	return &byIssueReviewAuthorityEvidenceProvider{identities: identities, authorize: authorize}
}

func (p *byIssueReviewAuthorityEvidenceProvider) ResolveReviewReconcileEvidence(
	ctx context.Context,
	workspaceID, projectID pgtype.UUID,
	candidate service.ReviewReconcileCandidate,
) (service.ReviewReconcileAuthorityEvidence, error) {
	var empty service.ReviewReconcileAuthorityEvidence
	if p == nil || candidate.WorkspaceID != pgUUIDString(workspaceID) || candidate.ProjectID != pgUUIDString(projectID) ||
		strings.TrimSpace(candidate.SourceAuthorAgentID) == "" || strings.TrimSpace(candidate.PlannedReviewerEmployeeID) == "" ||
		strings.TrimSpace(candidate.PlannedReviewerAgentID) == "" {
		return empty, errByIssueIdentity
	}
	parsedIssue, err := uuid.Parse(strings.TrimSpace(candidate.IssueID))
	if err != nil {
		return empty, errByIssueIdentity
	}
	issueID := pgtype.UUID{Bytes: parsedIssue, Valid: true}
	author, err := p.identities.ResolveImplementationIdentity(ctx, workspaceID, projectID, issueID)
	if err != nil || author.AgentID != candidate.SourceAuthorAgentID {
		return empty, errByIssueIdentity
	}
	reviewer, err := p.identities.ResolveReviewDispatchIdentity(ctx, workspaceID, projectID, issueID, service.AuthorityReviewDispatchCandidate{})
	if err != nil || reviewer.EmployeeID != candidate.PlannedReviewerEmployeeID || reviewer.AgentID != candidate.PlannedReviewerAgentID {
		return empty, errByIssueIdentity
	}
	independent := reviewer.EmployeeID != author.EmployeeID && reviewer.AgentID != author.AgentID
	if !independent {
		return empty, errByIssueIdentity
	}
	response, err := p.authorize(ctx, reviewer)
	if err != nil {
		return empty, errByIssueIdentity
	}
	eligible := response.Authorization.EventReconcile.Eligible && response.Authorization.RecoveryOnly.Eligible
	if !eligible {
		return empty, errors.New("review delegation is not eligible")
	}
	return service.ReviewReconcileAuthorityEvidence{
		AuthorityKnown: true, AuthorityEligible: true,
		AuthorKnown: true, AuthorEmployeeID: author.EmployeeID, AuthorAgentID: author.AgentID,
		Reviewer: service.ReviewReconcileReviewer{
			Known: true, Healthy: true, Independent: true,
			EmployeeID: reviewer.EmployeeID, AgentID: reviewer.AgentID,
		},
	}, nil
}
