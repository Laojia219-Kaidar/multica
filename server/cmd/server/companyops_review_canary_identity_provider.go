package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

// Option C per William's 2026-08-15 decision: a server-only single-row canary
// identity provider for the controlled HIV-641 review canary. It exists only
// until the Authority by-issue reverse lookup (Option A) lands; before T4,
// batch review drain, or formal general running it must be removed or kept
// permanently disabled. The provider is default-off, binds exactly one
// workspace/project/issue and one exact five-selector identity, carries a hard
// TTL kill switch, never accepts browser input, and fails closed for every
// other candidate. It is not a projection table and not a second identity
// truth source: the authorization truth remains the Authority database rows.
type reviewCanaryIdentityProvider struct {
	workspaceID uuid.UUID
	projectID   uuid.UUID
	issueID     uuid.UUID
	identity    service.AuthorityReviewDispatchIdentity
	expiresAt   time.Time
	now         func() time.Time
}

var errReviewCanaryIdentity = errors.New("review canary identity provider refused the candidate")

// reviewCanaryIdentityProviderFromEnv returns nil unless every canary field is
// present and well-formed; a nil provider keeps the router's existing
// fail-closed nil-gate composition.
func reviewCanaryIdentityProviderFromEnv(now func() time.Time) *reviewCanaryIdentityProvider {
	if strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_ENABLED")) != "true" {
		return nil
	}
	workspaceID, err := uuid.Parse(strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_WORKSPACE_ID")))
	if err != nil {
		return nil
	}
	projectID, err := uuid.Parse(strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_PROJECT_ID")))
	if err != nil {
		return nil
	}
	issueID, err := uuid.Parse(strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_ISSUE_ID")))
	if err != nil {
		return nil
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_EXPIRES_AT")))
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		return nil
	}
	identity := service.AuthorityReviewDispatchIdentity{
		TenantID:           strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_TENANT_ID")),
		WorkOrderSourceRef: strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_WORK_ORDER_REF")),
		EmployeeID:         strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_EMPLOYEE_ID")),
		IdentityBindingID:  strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_IDENTITY_BINDING_ID")),
		AgentID:            strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_AGENT_ID")),
		AssignmentID:       strings.TrimSpace(os.Getenv("HIVECREW_REVIEW_CANARY_ASSIGNMENT_ID")),
	}
	for _, value := range []string{identity.TenantID, identity.WorkOrderSourceRef, identity.EmployeeID, identity.IdentityBindingID, identity.AgentID, identity.AssignmentID} {
		if value == "" {
			return nil
		}
	}
	if now == nil {
		now = time.Now
	}
	return &reviewCanaryIdentityProvider{
		workspaceID: workspaceID,
		projectID:   projectID,
		issueID:     issueID,
		identity:    identity,
		expiresAt:   expiresAt,
		now:         now,
	}
}

func (p *reviewCanaryIdentityProvider) ResolveReviewDispatchIdentity(
	_ context.Context,
	workspaceID, projectID, issueID pgtype.UUID,
	_ service.AuthorityReviewDispatchCandidate,
) (service.AuthorityReviewDispatchIdentity, error) {
	if p == nil || !workspaceID.Valid || !projectID.Valid || !issueID.Valid {
		return service.AuthorityReviewDispatchIdentity{}, errReviewCanaryIdentity
	}
	if workspaceID.Bytes != p.workspaceID || projectID.Bytes != p.projectID || issueID.Bytes != p.issueID {
		return service.AuthorityReviewDispatchIdentity{}, fmt.Errorf("%w: candidate is outside the canary scope", errReviewCanaryIdentity)
	}
	if p.now().UTC().After(p.expiresAt) {
		return service.AuthorityReviewDispatchIdentity{}, fmt.Errorf("%w: canary TTL expired", errReviewCanaryIdentity)
	}
	return p.identity, nil
}
