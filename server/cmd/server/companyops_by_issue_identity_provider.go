package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

// Option A per William's 2026-08-15 decision: the review identity provider
// resolved through the Authority's by-issue reverse lookup. One read-only
// authenticated GET per candidate; OBSERVED with a complete five-selector
// identity resolves, anything else fails closed. This replaces the Option C
// env canary as the formal mechanism.
type byIssueIdentityProvider struct {
	baseURL   string
	tenantID  string
	transport http.RoundTripper
	now       func() time.Time
}

var errByIssueIdentity = errors.New("by-issue authority identity is unavailable")

type byIssueResponse struct {
	SchemaVersion string `json:"schema_version"`
	OK            bool   `json:"ok"`
	ReadOnly      bool   `json:"read_only"`
	Request       struct {
		TenantID    string `json:"tenant_id"`
		WorkspaceID string `json:"workspace_id"`
		IssueID     string `json:"issue_id"`
	} `json:"request"`
	State             string `json:"state"`
	ExecutionIdentity *struct {
		WorkOrderSourceRef string `json:"work_order_source_ref"`
		EmployeeID         string `json:"employee_id"`
		IdentityBindingID  string `json:"identity_binding_id"`
		AgentID            string `json:"agent_id"`
		AssignmentID       string `json:"assignment_id"`
	} `json:"execution_identity"`
	WorkOrderID      string `json:"work_order_id"`
	ProjectID        string `json:"project_id"`
	OwnerDecisionRef string `json:"owner_decision_ref"`
	SourceRef        string `json:"source_ref"`
	SourceRevision   string `json:"source_revision"`
	ObservedAt       string `json:"observed_at"`
	ExpiresAt        string `json:"expires_at"`
}

func newByIssueIdentityProvider(baseURL, tenantID string, transport http.RoundTripper) *byIssueIdentityProvider {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(tenantID) == "" || transport == nil {
		return nil
	}
	return &byIssueIdentityProvider{baseURL: strings.TrimSpace(baseURL), tenantID: strings.TrimSpace(tenantID), transport: transport, now: time.Now}
}

func (p *byIssueIdentityProvider) ResolveReviewDispatchIdentity(
	ctx context.Context,
	workspaceID, projectID, issueID pgtype.UUID,
	_ service.AuthorityReviewDispatchCandidate,
) (service.AuthorityReviewDispatchIdentity, error) {
	return p.resolveByPurpose(ctx, workspaceID, projectID, issueID, "review")
}

func (p *byIssueIdentityProvider) ResolveImplementationIdentity(
	ctx context.Context,
	workspaceID, projectID, issueID pgtype.UUID,
) (service.AuthorityReviewDispatchIdentity, error) {
	return p.resolveByPurpose(ctx, workspaceID, projectID, issueID, "implementation")
}

func (p *byIssueIdentityProvider) resolveByPurpose(
	ctx context.Context,
	workspaceID, projectID, issueID pgtype.UUID,
	purpose string,
) (service.AuthorityReviewDispatchIdentity, error) {
	if p == nil || !workspaceID.Valid || !issueID.Valid {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	if !projectID.Valid || (purpose != "implementation" && purpose != "review") {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	workspace, project, issue := pgUUIDString(workspaceID), pgUUIDString(projectID), pgUUIDString(issueID)
	endpoint, err := url.Parse(strings.TrimSuffix(p.baseURL, "/") + "/api/company-ops/issue-dispatch-authorization")
	if err != nil {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	query := endpoint.Query()
	query.Set("tenant_id", p.tenantID)
	query.Set("workspace_id", workspace)
	query.Set("issue_id", issue)
	query.Set("purpose", purpose)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: p.transport, Timeout: 10 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	decoder := json.NewDecoder(response.Body)
	var body byIssueResponse
	if err := decoder.Decode(&body); err != nil {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	if body.SchemaVersion != "hivecosm.issue-dispatch-authorization.v1" || !body.OK || !body.ReadOnly ||
		body.Request.TenantID != p.tenantID || body.Request.WorkspaceID != workspace || body.Request.IssueID != issue ||
		body.State != "OBSERVED" || body.ExecutionIdentity == nil || body.ProjectID != project ||
		strings.TrimSpace(body.WorkOrderID) == "" || strings.TrimSpace(body.OwnerDecisionRef) == "" ||
		strings.TrimSpace(body.SourceRef) == "" || strings.TrimSpace(body.SourceRevision) == "" {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	now := time.Now().UTC()
	if p.now != nil {
		now = p.now().UTC()
	}
	observedAt, observedErr := time.Parse(time.RFC3339Nano, body.ObservedAt)
	expiresAt, expiresErr := time.Parse(time.RFC3339Nano, body.ExpiresAt)
	if observedErr != nil || expiresErr != nil || observedAt.After(now.Add(5*time.Second)) ||
		now.Sub(observedAt) > 15*time.Minute || !expiresAt.After(now) {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	identity := service.AuthorityReviewDispatchIdentity{
		TenantID:           p.tenantID,
		WorkOrderSourceRef: body.ExecutionIdentity.WorkOrderSourceRef,
		EmployeeID:         body.ExecutionIdentity.EmployeeID,
		IdentityBindingID:  body.ExecutionIdentity.IdentityBindingID,
		AgentID:            body.ExecutionIdentity.AgentID,
		AssignmentID:       body.ExecutionIdentity.AssignmentID,
	}
	for _, value := range []string{identity.TenantID, identity.WorkOrderSourceRef, identity.EmployeeID, identity.IdentityBindingID, identity.AgentID, identity.AssignmentID} {
		if strings.TrimSpace(value) == "" {
			return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
		}
	}
	if _, err := uuid.Parse(identity.AgentID); err != nil {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	return identity, nil
}

// companyOpsTransportForBaseURL builds the origin-pinned bearer transport
// from the same env token source the runtime clients use.
func companyOpsTransportForBaseURL(baseURL string) http.RoundTripper {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || !isSafeCompanyOpsAuthorityURL(parsed) {
		return nil
	}
	token, err := companyOpsAuthorityBearerTokenFromEnv(context.Background(), nil)
	if err != nil {
		return nil
	}
	return companyOpsBearerTransport{base: http.DefaultTransport, token: token, authorityScheme: parsed.Scheme, authorityHost: parsed.Host}
}

func pgUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, b := range value.Bytes {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out)
}
