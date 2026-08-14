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
	return DispatchAuthorizationLookup{TenantID: "tenant-1", ExecutionIdentity: DispatchAuthorizationExecutionIdentity{WorkOrderSourceRef: "hive://hivecosm/delivery/project/project-1/work-order/wo-1", EmployeeID: "employee-1", IdentityBindingID: "binding-1", AgentID: "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001", AssignmentID: "assignment-1"}}
}
func ptr(v string) *string { return &v }
func validDispatchResponse() DispatchAuthorizationResponse {
	return validDispatchResponseAt(time.Now().UTC())
}
func validDispatchResponseAt(now time.Time) DispatchAuthorizationResponse {
	l := testLookup()
	now = now.UTC()
	observed := now.Add(-time.Minute).Format(time.RFC3339Nano)
	generated := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	expires := now.Add(5 * time.Minute).Format(time.RFC3339Nano)
	workspaceID := "01972f7e-7e8d-77ef-a13d-1b0ce3e9c010"
	issueID := "01972f7e-7e8d-77ef-a13d-1b0ce3e9c011"
	scopeRef := "hive://scope/tenant-1/01972f7e-7e8d-77ef-a13d-1b0ce3e9c010/wf-1/goal-1/wo-1"
	issueRef := "hive://issues/project-1/wo-1/01972f7e-7e8d-77ef-a13d-1b0ce3e9c011"
	rev := "revision:dispatch-1"
	scope := DispatchAuthorizationScope{State: "OBSERVED", TenantID: ptr(l.TenantID), WorkspaceID: ptr(workspaceID), WorkflowID: ptr("wf-1"), GoalID: ptr("goal-1"), WorkOrderID: ptr("wo-1"), SourceRef: ptr(scopeRef), SourceRevision: ptr(rev), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}
	issue := DispatchAuthorizationIssueLinkage{State: "OBSERVED", IssueID: ptr(issueID), ProjectID: ptr("project-1"), WorkOrderID: ptr("wo-1"), SourceRef: ptr(issueRef), SourceRevision: ptr(rev), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}
	rec := dispatchEvidenceRecord{State: "OBSERVED", SourceRef: ptr(l.ExecutionIdentity.WorkOrderSourceRef), SourceRevision: ptr(rev), WorkOrderID: ptr("wo-1"), ProjectID: ptr("project-1"), Status: ptr("running"), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}
	assignmentRec := rec
	assignmentRec.SourceRef = ptr("hive://assignments/assignment-1")
	bindingRec := rec
	bindingRec.SourceRef = ptr("hive://identity-bindings/binding-1")
	custodyRec := rec
	custodyRec.SourceRef = ptr("hive://custody/wo-1/assignment-1")
	assignment := dispatchAssignmentEvidence{dispatchEvidenceRecord: assignmentRec, AssignmentID: ptr(l.ExecutionIdentity.AssignmentID), EmployeeID: ptr(l.ExecutionIdentity.EmployeeID), AgentID: ptr(l.ExecutionIdentity.AgentID)}
	binding := dispatchBindingEvidence{dispatchEvidenceRecord: bindingRec, IdentityBindingID: ptr(l.ExecutionIdentity.IdentityBindingID), EmployeeID: ptr(l.ExecutionIdentity.EmployeeID), AgentID: ptr(l.ExecutionIdentity.AgentID), Active: true}
	custody := dispatchCustodyEvidence{dispatchEvidenceRecord: custodyRec, AssignmentID: ptr(l.ExecutionIdentity.AssignmentID), EmployeeID: ptr(l.ExecutionIdentity.EmployeeID), AgentID: ptr(l.ExecutionIdentity.AgentID)}
	workflow := dispatchWorkflowEvidence{State: "AUTHORIZED", Scope: ptr("event_reconcile"), WorkflowID: ptr("wf-1"), GoalID: ptr("goal-1"), WorkOrderID: ptr("wo-1"), OwnerDecisionRef: ptr("hive://owner-decisions/decision-1"), SourceRef: ptr("hive://authorizations/auth-1"), SourceRevision: ptr(rev), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires), OwnerDecisionAuthority: dispatchOwnerDecisionEvidence{State: "OBSERVED", OwnerDecisionRef: ptr("hive://owner-decisions/decision-1"), SourceRef: ptr("hive://owner-decisions/decision-1"), SourceRevision: ptr(rev), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}}
	evidence := &DispatchAuthorizationEvidence{Scope: scope, IssueLinkage: issue, WorkOrder: rec, Assignment: assignment, IdentityBinding: binding, Custody: custody, ContinuousWorkflowAuthorization: workflow, WorkflowAuthority: dispatchAuthorityEvidenceRecord{State: "OBSERVED", WorkflowID: ptr("wf-1"), SourceRef: ptr("hive://workflows/wf-1"), SourceRevision: ptr(rev), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}, GoalAuthority: dispatchAuthorityEvidenceRecord{State: "OBSERVED", GoalID: ptr("goal-1"), SourceRef: ptr("hive://goals/goal-1"), SourceRevision: ptr(rev), ObservedAt: observed, SourceGeneratedAt: ptr(generated), Freshness: "current", ExpiresAt: ptr(expires)}}
	r := DispatchAuthorizationResponse{SchemaVersion: HiveCosmDispatchAuthorizationSchema, OK: true, ReadOnly: true, TenantID: l.TenantID, Request: l, ExecutionIdentity: l.ExecutionIdentity, Scope: scope, IssueLinkage: issue, Evidence: evidence}
	sourceRefs := expectedDecisionSourceRefs(*evidence)
	sourceRevisions := expectedDecisionSourceRevisions(*evidence)
	r.Authorization.EventReconcile = DispatchAuthorizationDecision{Eligible: true, Reason: "eligible:all_required_authority_evidence_current", SourceRefs: sourceRefs, SourceRevisions: sourceRevisions, ObservedAt: observed, Freshness: "current", ExpiresAt: ptr(expires)}
	r.Authorization.RecoveryOnly = DispatchAuthorizationDecision{Eligible: false, Reason: "blocked:authorization_scope_mismatch", SourceRefs: sourceRefs, SourceRevisions: sourceRevisions, ObservedAt: observed, Freshness: "current", ExpiresAt: ptr(expires)}
	return r
}

func TestDispatchAuthorizationClientConsumesExactAuthorityRead(t *testing.T) {
	lookup := testLookup()
	valid := validDispatchResponse()
	var gotAuth, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
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
	for _, key := range []string{"tenant_id=tenant-1", "work_order_source_ref=hive%3A%2F%2Fhivecosm%2Fdelivery%2Fproject%2Fproject-1%2Fwork-order%2Fwo-1", "employee_id=employee-1", "identity_binding_id=binding-1", "agent_id=01972f7e-7e8d-77ef-a13d-1b0ce3e9c001", "assignment_id=assignment-1"} {
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
	for _, mutate := range []func(*DispatchAuthorizationLookup){
		func(l *DispatchAuthorizationLookup) { l.ExecutionIdentity.AssignmentID = "" },
		func(l *DispatchAuthorizationLookup) { l.ExecutionIdentity.AgentID = "agent-1" },
		func(l *DispatchAuthorizationLookup) {
			l.ExecutionIdentity.WorkOrderSourceRef = "hive://hivecosm/delivery/project/project-1/work-order/prefix-wo-1/extra"
		},
	} {
		l := testLookup()
		mutate(&l)
		if err := validateDispatchAuthorizationLookup(l); err == nil {
			t.Fatal("accepted malformed execution selector")
		}
	}
}

func TestDispatchAuthorizationClientRejectsCrossTenantBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(validDispatchResponse())
	}))
	defer server.Close()
	client, err := NewHiveCosmDispatchAuthorizationClient(server.URL, nil, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	lookup := testLookup()
	lookup.TenantID = "tenant-other"
	if _, err := client.Resolve(context.Background(), lookup); err == nil {
		t.Fatal("cross-tenant lookup reached Authority")
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestDispatchAuthorizationClientRejectsNestedDriftForgedPathAndInvalidDecision(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DispatchAuthorizationResponse)
	}{
		{"nested scope drift", func(r *DispatchAuthorizationResponse) { r.Evidence.Scope.GoalID = ptr("goal-other") }},
		{"nested linkage drift", func(r *DispatchAuthorizationResponse) { r.Evidence.IssueLinkage.WorkOrderID = ptr("wo-other") }},
		{"forged path segment", func(r *DispatchAuthorizationResponse) {
			r.Scope.SourceRef = ptr("hive://scope/tenant-1/01972f7e-7e8d-77ef-a13d-1b0ce3e9c010/wf-1/goal-1/prefix-wo-1")
		}},
		{"ineligible selected decision", func(r *DispatchAuthorizationResponse) {
			r.Authorization.EventReconcile.Eligible = false
			r.Authorization.EventReconcile.Reason = "blocked:unexpected"
		}},
		{"forged decision provenance", func(r *DispatchAuthorizationResponse) {
			r.Authorization.EventReconcile.SourceRefs = []string{"hive://authorizations/auth-1"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validDispatchResponse()
			tt.mutate(&r)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(r)
			}))
			defer server.Close()
			client, _ := NewHiveCosmDispatchAuthorizationClient(server.URL, nil, "tenant-1")
			if _, err := client.Resolve(context.Background(), testLookup()); err == nil {
				t.Fatal("accepted invalid nested Authority evidence")
			}
		})
	}
}

func TestDispatchAuthorizationClientUsesInjectedClockForEveryEvidenceTimestamp(t *testing.T) {
	fixed := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	response := validDispatchResponseAt(fixed)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client, _ := NewHiveCosmDispatchAuthorizationClient(server.URL, nil, "tenant-1")
	client.now = func() time.Time { return fixed }
	if _, err := client.Resolve(context.Background(), testLookup()); err != nil {
		t.Fatalf("injected clock response rejected: %v", err)
	}
	client.now = func() time.Time { return fixed.Add(20 * time.Minute) }
	if _, err := client.Resolve(context.Background(), testLookup()); err == nil {
		t.Fatal("stale response accepted against injected clock")
	}
}

func TestDispatchAuthorizationClientRejectsNonCurrentAuthorityFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DispatchAuthorizationResponse)
	}{
		{"continuous authorization freshness", func(r *DispatchAuthorizationResponse) { r.Evidence.ContinuousWorkflowAuthorization.Freshness = "stale" }},
		{"owner decision freshness", func(r *DispatchAuthorizationResponse) {
			r.Evidence.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.Freshness = "stale"
		}},
		{"workflow authority freshness", func(r *DispatchAuthorizationResponse) { r.Evidence.WorkflowAuthority.Freshness = "stale" }},
		{"goal authority freshness", func(r *DispatchAuthorizationResponse) { r.Evidence.GoalAuthority.Freshness = "stale" }},
		{"event decision freshness", func(r *DispatchAuthorizationResponse) { r.Authorization.EventReconcile.Freshness = "stale" }},
		{"recovery decision freshness", func(r *DispatchAuthorizationResponse) { r.Authorization.RecoveryOnly.Freshness = "stale" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validDispatchResponse()
			tt.mutate(&r)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(r)
			}))
			defer server.Close()
			client, _ := NewHiveCosmDispatchAuthorizationClient(server.URL, nil, "tenant-1")
			if _, err := client.Resolve(context.Background(), testLookup()); err == nil {
				t.Fatal("accepted non-current authority field")
			}
		})
	}
}

func TestDispatchAuthorizationClientRejectsNonCanonicalAuthorityURIShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DispatchAuthorizationResponse)
	}{
		{"scope extra segment", func(r *DispatchAuthorizationResponse) {
			r.Evidence.Scope.SourceRef = ptr(*r.Evidence.Scope.SourceRef + "/extra")
			r.Scope = r.Evidence.Scope
		}},
		{"issue linkage extra segment", func(r *DispatchAuthorizationResponse) {
			r.Evidence.IssueLinkage.SourceRef = ptr(*r.Evidence.IssueLinkage.SourceRef + "/extra")
			r.IssueLinkage = r.Evidence.IssueLinkage
		}},
		{"work order extra segment", func(r *DispatchAuthorizationResponse) {
			r.Evidence.WorkOrder.SourceRef = ptr(*r.Evidence.WorkOrder.SourceRef + "/extra")
		}},
		{"assignment wrong type", func(r *DispatchAuthorizationResponse) {
			r.Evidence.Assignment.SourceRef = ptr("hive://identity-bindings/assignment-1")
		}},
		{"identity binding wrong type", func(r *DispatchAuthorizationResponse) {
			r.Evidence.IdentityBinding.SourceRef = ptr("hive://assignments/binding-1")
		}},
		{"custody extra segment", func(r *DispatchAuthorizationResponse) {
			r.Evidence.Custody.SourceRef = ptr("hive://custody/wo-1/assignment-1/extra")
		}},
		{"continuous authorization wrong type", func(r *DispatchAuthorizationResponse) {
			r.Evidence.ContinuousWorkflowAuthorization.SourceRef = ptr("hive://goals/auth-1")
		}},
		{"owner decision wrong source", func(r *DispatchAuthorizationResponse) {
			r.Evidence.ContinuousWorkflowAuthorization.OwnerDecisionAuthority.SourceRef = ptr("hive://owner-decisions/other")
		}},
		{"workflow authority wrong type", func(r *DispatchAuthorizationResponse) {
			r.Evidence.WorkflowAuthority.SourceRef = ptr("hive://goals/wf-1")
		}},
		{"goal authority extra segment", func(r *DispatchAuthorizationResponse) {
			r.Evidence.GoalAuthority.SourceRef = ptr("hive://goals/goal-1/extra")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validDispatchResponse()
			tt.mutate(&r)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(r)
			}))
			defer server.Close()
			client, _ := NewHiveCosmDispatchAuthorizationClient(server.URL, nil, "tenant-1")
			if _, err := client.Resolve(context.Background(), testLookup()); err == nil {
				t.Fatal("accepted non-canonical Authority URI")
			}
		})
	}
}

func TestDispatchAuthorizationFreshTimesRequiresExpiryAfterObservation(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	observed := "2026-08-14T09:59:00Z"
	generated := "2026-08-14T09:58:00Z"
	equal := observed
	if err := validateFreshTimes(observed, &generated, &equal, now); err == nil {
		t.Fatal("accepted expires_at equal to observed_at")
	}
	before := "2026-08-14T09:58:59Z"
	if err := validateFreshTimes(observed, &generated, &before, now); err == nil {
		t.Fatal("accepted expires_at before observed_at")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
