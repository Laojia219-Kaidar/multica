package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDispatchAuthorizationClientAcceptsOnlyExactFreshAuthority(t *testing.T) {
	lookup := DispatchAuthorizationLookup{WorkspaceID: "ws-1", WorkflowID: "wf-1", GoalID: "goal-1", WorkOrderID: "wo-1"}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	decision := DispatchAuthorization{
		State: "authorized", Scope: "event_reconcile", WorkflowID: lookup.WorkflowID, GoalID: lookup.GoalID, WorkOrderID: lookup.WorkOrderID,
		OwnerDecisionRef: "hive://owner/decision/42", SourceRef: "hive://authority/dispatch/42", SourceRevision: "sha256:" + strings.Repeat("a", 64),
		ObservedAt: "2026-08-14T11:59:00Z", SourceGeneratedAt: "2026-08-14T11:58:59Z", Freshness: "fresh", ExpiresAt: "2026-08-14T12:05:00Z",
	}
	var gotAuth string
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HiveCosmDispatchAuthorizationResponse{
			SchemaVersion: HiveCosmDispatchAuthorizationSchema, OK: true, TenantID: "tenant-1", WorkspaceID: lookup.WorkspaceID,
			Request: lookup, Authorization: decision,
		})
	}))
	defer server.Close()
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer test-authority-token")
		return http.DefaultTransport.RoundTrip(r)
	})
	client, err := NewHiveCosmDispatchAuthorizationClient(server.URL, &http.Client{Transport: transport}, "tenant-1")
	if err != nil {
		t.Fatalf("New client: %v", err)
	}
	client.now = func() time.Time { return now }
	got, err := client.Resolve(context.Background(), lookup)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != decision {
		t.Fatalf("decision = %+v, want %+v", got, decision)
	}
	if gotAuth != "Bearer test-authority-token" {
		t.Fatalf("authorization header = %q", gotAuth)
	}
	for _, expected := range []string{"workspace_id=ws-1", "workflow_id=wf-1", "goal_id=goal-1", "work_order_id=wo-1"} {
		if !strings.Contains(gotQuery, expected) {
			t.Fatalf("query %q does not contain %q", gotQuery, expected)
		}
	}
}

func TestDispatchAuthorizationClientFailsClosed(t *testing.T) {
	lookup := DispatchAuthorizationLookup{WorkspaceID: "ws-1", WorkflowID: "wf-1", GoalID: "goal-1", WorkOrderID: "wo-1"}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	valid := HiveCosmDispatchAuthorizationResponse{
		SchemaVersion: HiveCosmDispatchAuthorizationSchema, OK: true, TenantID: "tenant-1", WorkspaceID: lookup.WorkspaceID, Request: lookup,
		Authorization: DispatchAuthorization{State: "authorized", Scope: "recovery_only", WorkflowID: lookup.WorkflowID, GoalID: lookup.GoalID, WorkOrderID: lookup.WorkOrderID,
			OwnerDecisionRef: "decision", SourceRef: "source", SourceRevision: "revision", ObservedAt: "2026-08-14T11:59:00Z", SourceGeneratedAt: "2026-08-14T11:59:00Z", Freshness: "fresh", ExpiresAt: "2026-08-14T12:01:00Z"},
	}
	tests := []struct {
		name       string
		status     int
		body       any
		content    string
		wantSecret string
	}{
		{name: "unauthenticated", status: http.StatusUnauthorized, body: map[string]string{"error": "Bearer real-secret-token"}, content: "application/json", wantSecret: "real-secret-token"},
		{name: "unknown field", status: http.StatusOK, body: map[string]any{"schema_version": HiveCosmDispatchAuthorizationSchema, "ok": true, "tenant_id": "tenant-1", "workspace_id": lookup.WorkspaceID, "request": lookup, "authorization": valid.Authorization, "unexpected": true}, content: "application/json"},
		{name: "tenant drift", status: http.StatusOK, body: func() HiveCosmDispatchAuthorizationResponse { v := valid; v.TenantID = "other"; return v }(), content: "application/json"},
		{name: "request drift", status: http.StatusOK, body: func() HiveCosmDispatchAuthorizationResponse { v := valid; v.Request.GoalID = "other"; return v }(), content: "application/json"},
		{name: "revoked", status: http.StatusOK, body: func() HiveCosmDispatchAuthorizationResponse { v := valid; v.Authorization.State = "revoked"; return v }(), content: "application/json"},
		{name: "stale", status: http.StatusOK, body: func() HiveCosmDispatchAuthorizationResponse {
			v := valid
			v.Authorization.ObservedAt = "2026-08-14T11:00:00Z"
			return v
		}(), content: "application/json"},
		{name: "expired", status: http.StatusOK, body: func() HiveCosmDispatchAuthorizationResponse {
			v := valid
			v.Authorization.ExpiresAt = "2026-08-14T11:59:59Z"
			return v
		}(), content: "application/json"},
		{name: "wrong media", status: http.StatusOK, body: valid, content: "text/plain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.content)
				w.WriteHeader(tt.status)
				if tt.body != nil {
					if raw, ok := tt.body.(string); ok {
						_, _ = io.WriteString(w, raw)
					} else {
						_ = json.NewEncoder(w).Encode(tt.body)
					}
				}
			}))
			defer server.Close()
			client, err := NewHiveCosmDispatchAuthorizationClient(server.URL, nil, "tenant-1")
			if err != nil {
				t.Fatalf("New client: %v", err)
			}
			client.now = func() time.Time { return now }
			_, err = client.Resolve(context.Background(), lookup)
			if err == nil {
				t.Fatal("Resolve succeeded for ineligible authority response")
			}
			var authorityErr *HiveCosmAuthorityError
			if !errors.As(err, &authorityErr) || authorityErr.Kind != HiveCosmAuthoritySourceGap {
				t.Fatalf("error = %v, want source_gap authority error", err)
			}
			if tt.wantSecret != "" && strings.Contains(err.Error(), tt.wantSecret) {
				t.Fatalf("error leaked secret %q: %v", tt.wantSecret, err)
			}
		})
	}
}

func TestDispatchAuthorizationClientRejectsMissingConfiguration(t *testing.T) {
	client := &HiveCosmDispatchAuthorizationClient{}
	_, err := client.Resolve(context.Background(), DispatchAuthorizationLookup{WorkspaceID: "ws-1", WorkflowID: "wf-1", GoalID: "goal-1", WorkOrderID: "wo-1"})
	if err == nil || !strings.Contains(err.Error(), string(HiveCosmAuthoritySourceGap)) {
		t.Fatalf("error = %v, want source_gap", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
