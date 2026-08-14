package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testLookup() DispatchAuthorizationLookup {
	return DispatchAuthorizationLookup{TenantID: "tenant-1", ExecutionIdentity: DispatchAuthorizationExecutionIdentity{WorkOrderSourceRef: "hive://orders/wo-1", EmployeeID: "employee-1", IdentityBindingID: "binding-1", AgentID: "agent-1", AssignmentID: "assignment-1"}}
}
func ptr(v string) *string { return &v }
func validDispatchResponse() DispatchAuthorizationResponse {
	l := testLookup()
	now := time.Now().UTC()
	observed := now.Add(-time.Minute).Format(time.RFC3339Nano)
	generated := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	expires := now.Add(5 * time.Minute).Format(time.RFC3339Nano)
	scopeRef := "hive://scope/tenant-1/ws-1/wf-1/goal-1/wo-1"
	issueRef := "hive://issues/project-1/wo-1/issue-1"
	rev := "revision:dispatch-1"
	scope := DispatchAuthorizationScope{State: "OBSERVED", TenantID: ptr(l.TenantID), WorkspaceID: ptr("ws-1"), WorkflowID: ptr("wf-1"), GoalID: ptr("goal-1"), WorkOrderID: ptr("wo-1"), SourceRef: ptr(scopeRef), SourceRevision: ptr(rev), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}
	issue := DispatchAuthorizationIssueLinkage{State: "OBSERVED", IssueID: ptr("issue-1"), ProjectID: ptr("project-1"), WorkOrderID: ptr("wo-1"), SourceRef: ptr(issueRef), SourceRevision: ptr(rev), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}
	rec := dispatchEvidenceRecord{State: "OBSERVED", SourceRef: ptr(l.ExecutionIdentity.WorkOrderSourceRef), SourceRevision: ptr(rev), WorkOrderID: ptr("wo-1"), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}
	assignment := dispatchAssignmentEvidence{dispatchEvidenceRecord: rec, AssignmentID: ptr(l.ExecutionIdentity.AssignmentID), EmployeeID: ptr(l.ExecutionIdentity.EmployeeID), AgentID: ptr(l.ExecutionIdentity.AgentID)}
	binding := dispatchBindingEvidence{dispatchEvidenceRecord: rec, IdentityBindingID: ptr(l.ExecutionIdentity.IdentityBindingID), EmployeeID: ptr(l.ExecutionIdentity.EmployeeID), AgentID: ptr(l.ExecutionIdentity.AgentID), Active: true}
	custody := dispatchCustodyEvidence{dispatchEvidenceRecord: rec, AssignmentID: ptr(l.ExecutionIdentity.AssignmentID), EmployeeID: ptr(l.ExecutionIdentity.EmployeeID), AgentID: ptr(l.ExecutionIdentity.AgentID)}
	workflow := dispatchWorkflowEvidence{State: "AUTHORIZED", Scope: ptr("event_reconcile"), WorkflowID: ptr("wf-1"), GoalID: ptr("goal-1"), WorkOrderID: ptr("wo-1"), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}
	evidence := &DispatchAuthorizationEvidence{Scope: scope, IssueLinkage: issue, WorkOrder: rec, Assignment: assignment, IdentityBinding: binding, Custody: custody, ContinuousWorkflowAuthorization: workflow}
	r := DispatchAuthorizationResponse{SchemaVersion: HiveCosmDispatchAuthorizationSchema, OK: true, ReadOnly: true, TenantID: l.TenantID, Request: l, ExecutionIdentity: l.ExecutionIdentity, Scope: scope, IssueLinkage: issue, Evidence: evidence}
	r.Authorization.EventReconcile = DispatchAuthorizationDecision{Eligible: true, Reason: "authorized", ObservedAt: observed, Freshness: "current", ExpiresAt: ptr(expires)}
	r.Authorization.RecoveryOnly = r.Authorization.EventReconcile
	return r
}

func TestDispatchAuthorizationClientConsumesExactAuthorityRead(t *testing.T) {
	lookup := testLookup()
	valid := validDispatchResponse()
	var gotAuth, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(valid)
	}))
	defer server.Close()
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer injected")
		return http.DefaultTransport.RoundTrip(r)
	})
	client, err := NewHiveCosmDispatchAuthorizationClient(server.URL, &http.Client{Transport: transport}, lookup.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Resolve(context.Background(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.SchemaVersion != HiveCosmDispatchAuthorizationSchema {
		t.Fatalf("unexpected response: %+v", got)
	}
	if gotAuth != "Bearer injected" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	for _, key := range []string{"tenant_id=tenant-1", "work_order_source_ref=hive%3A%2F%2Forders%2Fwo-1", "employee_id=employee-1", "identity_binding_id=binding-1", "agent_id=agent-1", "assignment_id=assignment-1"} {
		if !strings.Contains(gotQuery, key) {
			t.Fatalf("query %q missing %q", gotQuery, key)
		}
	}
}

func TestDispatchAuthorizationClientFailsClosedForDriftAndMalformedResponses(t *testing.T) {
	base := validDispatchResponse()
	tests := []struct {
		name   string
		mutate func(*DispatchAuthorizationResponse)
	}{{"tenant drift", func(r *DispatchAuthorizationResponse) { r.TenantID = "other" }}, {"selector drift", func(r *DispatchAuthorizationResponse) { r.ExecutionIdentity.AgentID = "other" }}, {"scope drift", func(r *DispatchAuthorizationResponse) { r.Scope.WorkspaceID = ptr("other") }}, {"issue linkage malformed", func(r *DispatchAuthorizationResponse) { r.IssueLinkage.IssueID = ptr("bad id") }}, {"stale evidence", func(r *DispatchAuthorizationResponse) { r.Evidence.Scope.ObservedAt = "2020-01-01T00:00:00Z" }}, {"unknown field", func(r *DispatchAuthorizationResponse) {}}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := base
			tt.mutate(&response)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tt.name == "unknown field" {
					_, _ = w.Write([]byte(`{"schema_version":"hivecosm.dispatch-authorization-read.v1","ok":true,"read_only":true,"unexpected":true}`))
					return
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client, _ := NewHiveCosmDispatchAuthorizationClient(server.URL, nil, "tenant-1")
			_, err := client.Resolve(context.Background(), testLookup())
			if err == nil {
				t.Fatal("accepted invalid Authority response")
			}
			var ae *HiveCosmAuthorityError
			if !errors.As(err, &ae) || ae.Kind != HiveCosmAuthoritySourceGap {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDispatchAuthorizationClientRejectsWrongMethodMediaAndRedirect(t *testing.T) {
	for _, mode := range []string{"POST", "wrong-media", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(validDispatchResponse())
			}))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusFound)
					return
				}
				if mode == "wrong-media" {
					w.Header().Set("Content-Type", "text/plain")
				}
				if mode == "POST" {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				_ = json.NewEncoder(w).Encode(validDispatchResponse())
			}))
			defer server.Close()
			client, _ := NewHiveCosmDispatchAuthorizationClient(server.URL, nil, "tenant-1")
			_, err := client.Resolve(context.Background(), testLookup())
			if err == nil {
				t.Fatal("accepted invalid method/media/redirect")
			}
		})
	}
}
func TestDispatchAuthorizationLookupRequiresAllFiveSelectors(t *testing.T) {
	l := testLookup()
	l.ExecutionIdentity.AssignmentID = ""
	if err := validateDispatchAuthorizationLookup(l); err == nil {
		t.Fatal("accepted missing assignment selector")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
