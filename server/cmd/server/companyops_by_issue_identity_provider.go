package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	State            string `json:"state"`
	ExecutionIdentity *struct {
		WorkOrderSourceRef string `json:"work_order_source_ref"`
		EmployeeID         string `json:"employee_id"`
		IdentityBindingID  string `json:"identity_binding_id"`
		AgentID            string `json:"agent_id"`
		AssignmentID       string `json:"assignment_id"`
	} `json:"execution_identity"`
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
	if p == nil || !workspaceID.Valid || !issueID.Valid {
		return service.AuthorityReviewDispatchIdentity{}, errByIssueIdentity
	}
	url := fmt.Sprintf("%s/api/company-ops/issue-dispatch-authorization?tenant_id=%s&workspace_id=%s&issue_id=%s&purpose=review",
		strings.TrimSuffix(p.baseURL, "/"), p.tenantID, pgUUIDString(workspaceID), pgUUIDString(issueID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	if body.State != "OBSERVED" || body.ExecutionIdentity == nil {
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
