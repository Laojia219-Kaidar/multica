package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/service"
)

func byIssueTestUUID(value string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(value), Valid: true}
}

func byIssueBody(now time.Time, query url.Values, projectID, employeeID, agentID, assignmentID string) string {
	return fmt.Sprintf(`{
  "schema_version":"hivecosm.issue-dispatch-authorization.v1","ok":true,"read_only":true,
  "request":{"tenant_id":%q,"workspace_id":%q,"issue_id":%q},"state":"OBSERVED",
  "execution_identity":{"work_order_source_ref":%q,"employee_id":%q,"identity_binding_id":%q,"agent_id":%q,"assignment_id":%q},
  "work_order_id":"WO-1","project_id":%q,"owner_decision_ref":"hive://owner-decisions/OWNER-1",
  "source_ref":"hive://hivecosm/direct-dispatch-authorizations/DDA-1","source_revision":"revision:DDA-1:1",
  "observed_at":%q,"expires_at":%q
}`,
		query.Get("tenant_id"), query.Get("workspace_id"), query.Get("issue_id"),
		"hive://hivecosm/delivery/project/"+projectID+"/work-order/WO-1", employeeID,
		"IB-"+employeeID, agentID, assignmentID, projectID,
		now.Add(-time.Minute).Format(time.RFC3339Nano), now.Add(10*time.Minute).Format(time.RFC3339Nano))
}

func TestByIssueIdentityProviderResolvesDistinctImplementationAndReviewDelegations(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	workspaceID := byIssueTestUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c010")
	projectID := byIssueTestUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c011")
	issueID := byIssueTestUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c012")
	authorAgent := "01972f7e-7e8d-77ef-a13d-1b0ce3e9c013"
	reviewerAgent := "01972f7e-7e8d-77ef-a13d-1b0ce3e9c014"
	seen := map[string]int{}
	transport := companyOpsRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		purpose := request.URL.Query().Get("purpose")
		seen[purpose]++
		employee, agent, assignment := "EMP-AUTHOR", authorAgent, "ASSIGN-AUTHOR"
		if purpose == "review" {
			employee, agent, assignment = "EMP-REVIEWER", reviewerAgent, "ASSIGN-REVIEWER"
		}
		body := byIssueBody(now, request.URL.Query(), pgUUIDString(projectID), employee, agent, assignment)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	provider := newByIssueIdentityProvider("http://authority.test", "tenant-1", transport)
	provider.now = func() time.Time { return now }
	author, err := provider.ResolveImplementationIdentity(context.Background(), workspaceID, projectID, issueID)
	if err != nil {
		t.Fatalf("ResolveImplementationIdentity: %v", err)
	}
	reviewer, err := provider.ResolveReviewDispatchIdentity(context.Background(), workspaceID, projectID, issueID, service.AuthorityReviewDispatchCandidate{})
	if err != nil {
		t.Fatalf("ResolveReviewDispatchIdentity: %v", err)
	}
	if author.AgentID != authorAgent || author.EmployeeID != "EMP-AUTHOR" || reviewer.AgentID != reviewerAgent || reviewer.EmployeeID != "EMP-REVIEWER" {
		t.Fatalf("author/reviewer = %+v / %+v", author, reviewer)
	}
	if seen["implementation"] != 1 || seen["review"] != 1 {
		t.Fatalf("purpose calls = %#v, want one each", seen)
	}
}

func TestByIssueIdentityProviderRejectsStaleOrCrossProjectEvidence(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	workspaceID := byIssueTestUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c010")
	projectID := byIssueTestUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c011")
	issueID := byIssueTestUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c012")
	for _, test := range []struct {
		name   string
		mutate func(string) string
	}{
		{name: "cross project", mutate: func(body string) string {
			return strings.Replace(body, pgUUIDString(projectID), "01972f7e-7e8d-77ef-a13d-1b0ce3e9c099", 2)
		}},
		{name: "stale observation", mutate: func(body string) string {
			return strings.Replace(body, now.Add(-time.Minute).Format(time.RFC3339Nano), now.Add(-16*time.Minute).Format(time.RFC3339Nano), 1)
		}},
		{name: "expired", mutate: func(body string) string {
			return strings.Replace(body, now.Add(10*time.Minute).Format(time.RFC3339Nano), now.Add(-time.Second).Format(time.RFC3339Nano), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := companyOpsRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
				body := byIssueBody(now, request.URL.Query(), pgUUIDString(projectID), "EMP-REVIEWER", "01972f7e-7e8d-77ef-a13d-1b0ce3e9c014", "ASSIGN-REVIEWER")
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.mutate(body)))}, nil
			})
			provider := newByIssueIdentityProvider("http://authority.test", "tenant-1", transport)
			provider.now = func() time.Time { return now }
			if _, err := provider.ResolveReviewDispatchIdentity(context.Background(), workspaceID, projectID, issueID, service.AuthorityReviewDispatchCandidate{}); err == nil {
				t.Fatal("drifted Authority response resolved; want fail closed")
			}
		})
	}
}

func TestByIssueReviewAuthorityEvidenceBindsSourceAuthorAndPlannedReviewer(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	workspaceID := byIssueTestUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c010")
	projectID := byIssueTestUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c011")
	issueID := byIssueTestUUID("01972f7e-7e8d-77ef-a13d-1b0ce3e9c012")
	authorAgent := "01972f7e-7e8d-77ef-a13d-1b0ce3e9c013"
	reviewerAgent := "01972f7e-7e8d-77ef-a13d-1b0ce3e9c014"
	transport := companyOpsRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		employee, agent, assignment := "EMP-AUTHOR", authorAgent, "ASSIGN-AUTHOR"
		if request.URL.Query().Get("purpose") == "review" {
			employee, agent, assignment = "EMP-REVIEWER", reviewerAgent, "ASSIGN-REVIEWER"
		}
		body := byIssueBody(now, request.URL.Query(), pgUUIDString(projectID), employee, agent, assignment)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	identities := newByIssueIdentityProvider("http://authority.test", "tenant-1", transport)
	identities.now = func() time.Time { return now }
	authorizeCalls := 0
	provider := newByIssueReviewAuthorityEvidenceProvider(identities, func(_ context.Context, identity service.AuthorityReviewDispatchIdentity) (companyops.DispatchAuthorizationResponse, error) {
		authorizeCalls++
		if identity.AgentID != reviewerAgent {
			t.Fatalf("authorized identity = %+v, want reviewer", identity)
		}
		var response companyops.DispatchAuthorizationResponse
		response.Authorization.EventReconcile.Eligible = true
		response.Authorization.RecoveryOnly.Eligible = true
		return response, nil
	})
	candidate := service.ReviewReconcileCandidate{
		WorkspaceID: pgUUIDString(workspaceID), ProjectID: pgUUIDString(projectID), IssueID: pgUUIDString(issueID),
		SourceAuthorAgentID: authorAgent, PlannedReviewerEmployeeID: "EMP-REVIEWER", PlannedReviewerAgentID: reviewerAgent,
	}
	evidence, err := provider.ResolveReviewReconcileEvidence(context.Background(), workspaceID, projectID, candidate)
	if err != nil {
		t.Fatalf("ResolveReviewReconcileEvidence: %v", err)
	}
	if !evidence.AuthorityEligible || evidence.AuthorAgentID != authorAgent || evidence.Reviewer.AgentID != reviewerAgent || !evidence.Reviewer.Independent || authorizeCalls != 1 {
		t.Fatalf("evidence/calls = %+v/%d", evidence, authorizeCalls)
	}

	candidate.SourceAuthorAgentID = reviewerAgent
	if _, err := provider.ResolveReviewReconcileEvidence(context.Background(), workspaceID, projectID, candidate); err == nil {
		t.Fatal("self-review author drift accepted")
	}
}
