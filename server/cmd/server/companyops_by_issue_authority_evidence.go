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

// byIssueCompanyOpsDirectory is the minimal read-only Authority-backed
// directory capability needed to prove a candidate author's formal employee.
type byIssueCompanyOpsDirectory interface {
	GetEmployees(context.Context, pgtype.UUID, string, string, int, int) (*service.EmployeesResult, error)
}

// byIssueReviewAuthorityEvidenceProvider joins the Authority-backed employee
// directory with the by-issue review delegation and HiveCrew's server-built
// candidate. HiveCrew remains the sole owner of Task/Comment/revision/
// generation lineage and revalidates it transactionally.
type byIssueReviewAuthorityEvidenceProvider struct {
	identities *byIssueIdentityProvider
	directory  byIssueCompanyOpsDirectory
	authorize  func(context.Context, service.AuthorityReviewDispatchIdentity) (companyops.DispatchAuthorizationResponse, error)
}

func newByIssueReviewAuthorityEvidenceProvider(
	identities *byIssueIdentityProvider,
	directory byIssueCompanyOpsDirectory,
	authorize func(context.Context, service.AuthorityReviewDispatchIdentity) (companyops.DispatchAuthorizationResponse, error),
) *byIssueReviewAuthorityEvidenceProvider {
	if identities == nil || directory == nil || authorize == nil {
		return nil
	}
	return &byIssueReviewAuthorityEvidenceProvider{identities: identities, directory: directory, authorize: authorize}
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
	authorEmployeeID, err := p.resolveAuthorEmployeeID(ctx, workspaceID, candidate.SourceAuthorAgentID)
	if err != nil {
		return empty, errByIssueIdentity
	}
	reviewer, err := p.identities.ResolveReviewDispatchIdentity(ctx, workspaceID, projectID, issueID, service.AuthorityReviewDispatchCandidate{})
	if err != nil || reviewer.EmployeeID != candidate.PlannedReviewerEmployeeID || reviewer.AgentID != candidate.PlannedReviewerAgentID {
		return empty, errByIssueIdentity
	}
	independent := reviewer.EmployeeID != authorEmployeeID && reviewer.AgentID != candidate.SourceAuthorAgentID
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
		AuthorKnown: true, AuthorEmployeeID: authorEmployeeID, AuthorAgentID: candidate.SourceAuthorAgentID,
		Reviewer: service.ReviewReconcileReviewer{
			Known: true, Healthy: true, Independent: true,
			EmployeeID: reviewer.EmployeeID, AgentID: reviewer.AgentID,
		},
	}, nil
}

func (p *byIssueReviewAuthorityEvidenceProvider) resolveAuthorEmployeeID(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID string,
) (string, error) {
	if p == nil || p.directory == nil || !workspaceID.Valid || strings.TrimSpace(agentID) == "" {
		return "", errByIssueIdentity
	}
	page, err := p.directory.GetEmployees(ctx, workspaceID, "", "", 500, 0)
	if err != nil || page == nil || page.WorkspaceID != pgUUIDString(workspaceID) {
		return "", errByIssueIdentity
	}
	var employeeID string
	for _, employee := range page.Items {
		if employee.HiveCrewAgentID != strings.TrimSpace(agentID) {
			continue
		}
		if employee.BindingState != companyops.BindingStateUniqueActiveCandidate ||
			employee.Availability != companyops.AvailabilityAvailable ||
			employee.Binding.HiveCrewAgentID == nil || *employee.Binding.HiveCrewAgentID != employee.HiveCrewAgentID ||
			employee.LocalAgent == nil || employee.LocalAgent.ID != employee.HiveCrewAgentID ||
			strings.TrimSpace(employee.EmployeeID) == "" {
			return "", errByIssueIdentity
		}
		if employeeID != "" {
			return "", errByIssueIdentity
		}
		employeeID = employee.EmployeeID
	}
	if employeeID == "" {
		return "", errByIssueIdentity
	}
	return employeeID, nil
}
