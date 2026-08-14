package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/routescore"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	quotaTestWorkspace = "11111111-1111-4111-8111-111111111111"
	quotaTestAgent     = "22222222-2222-4222-8222-222222222222"
	quotaTestRuntime   = "33333333-3333-4333-8333-333333333333"
)

func quotaTestUUID(value string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(value), Valid: true}
}

func quotaTestAgentRuntime() (db.Agent, db.AgentRuntime) {
	workspace := quotaTestUUID(quotaTestWorkspace)
	runtimeID := quotaTestUUID(quotaTestRuntime)
	agent := db.Agent{
		ID: quotaTestUUID(quotaTestAgent), WorkspaceID: workspace, RuntimeID: runtimeID,
		Model: pgtype.Text{String: "deepseek-v4-flash-0731", Valid: true},
	}
	runtime := db.AgentRuntime{
		ID: runtimeID, WorkspaceID: workspace, Provider: "bailian",
	}
	return agent, runtime
}

func quotaTestNow() time.Time {
	return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
}

func quotaTestResponse() HiveCosmQuotaObservationResponse {
	return HiveCosmQuotaObservationResponse{
		SchemaVersion: HiveCosmQuotaObservationSchema,
		OK:            true,
		TenantID:      "tenant-hivecosm-1",
		WorkspaceID:   quotaTestWorkspace,
		Request: QuotaObservationLookup{
			WorkspaceID: quotaTestWorkspace, AgentID: quotaTestAgent, RuntimeID: quotaTestRuntime,
		},
		Observation: QuotaObservation{
			AgentID: quotaTestAgent, RuntimeID: quotaTestRuntime,
			Provider: "bailian", Plan: "token-plan-personal", Model: "deepseek-v4-flash-0731",
			BillingAccountRef: "billing:bailian/personal", KeyRef: "keychain:bailian/token-plan-personal",
			Window: QuotaObservationWindow{
				Kind: "7d", StartsAt: "2026-08-14T10:00:00Z", ResetAt: "2026-08-14T13:00:00Z",
			},
			Unit: "tokens", Limit: 1000, Used: 250, Remaining: 750, Ratio: 0.25,
			ObservedAt: "2026-08-14T11:59:00Z", ExpiresAt: "2026-08-14T12:05:00Z",
			EvidenceState: "verified", EvidenceRef: "hive://authority/evidence/quota-1",
			SourceRef:      "hive://authority/quota-observation/1",
			SourceRevision: "sha256:" + strings.Repeat("a", 64), AuthorityState: "authoritative",
		},
	}
}

func quotaTestClient(t *testing.T, handler http.Handler) (*HiveCosmQuotaObservationClient, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	client, err := NewHiveCosmQuotaObservationClient(server.URL, server.Client(), "tenant-hivecosm-1")
	if err != nil {
		server.Close()
		t.Fatalf("New client: %v", err)
	}
	client.now = quotaTestNow
	return client, server.Close
}

func TestQuotaObservationClientAcceptsExactFreshBinding(t *testing.T) {
	agent, runtime := quotaTestAgentRuntime()
	response := quotaTestResponse()
	var gotAuth string
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		clone.Header.Set("Authorization", "Bearer test-authority-token")
		return http.DefaultTransport.RoundTrip(clone)
	})
	client, err := NewHiveCosmQuotaObservationClient(server.URL, &http.Client{Transport: transport}, response.TenantID)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}
	client.now = quotaTestNow
	got, err := client.Lookup(context.Background(), agent, runtime)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.State != routescore.QuotaFresh || got.CheckedAt.Format(time.RFC3339Nano) != response.Observation.ObservedAt || got.AccountRef != response.Observation.BillingAccountRef {
		t.Fatalf("snapshot = %+v, want fresh observation", got)
	}
	if gotAuth != "Bearer test-authority-token" {
		t.Fatalf("authorization header = %q", gotAuth)
	}
	for _, expected := range []string{"workspace_id=" + quotaTestWorkspace, "agent_id=" + quotaTestAgent, "runtime_id=" + quotaTestRuntime} {
		if !strings.Contains(gotQuery, expected) {
			t.Fatalf("query %q does not contain %q", gotQuery, expected)
		}
	}
}

func TestQuotaObservationClientMarksKnownExhaustedQuota(t *testing.T) {
	agent, runtime := quotaTestAgentRuntime()
	response := quotaTestResponse()
	response.Observation.Used = response.Observation.Limit
	response.Observation.Remaining = 0
	response.Observation.Ratio = 1
	client, closeServer := quotaTestClient(t, quotaObservationJSONHandler(response))
	defer closeServer()
	got, err := client.Lookup(context.Background(), agent, runtime)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.State != routescore.QuotaExhausted {
		t.Fatalf("state = %q, want exhausted", got.State)
	}
}

func TestQuotaObservationClientFailsClosedForInvalidAuthorityResponses(t *testing.T) {
	agent, runtime := quotaTestAgentRuntime()
	base := quotaTestResponse()
	tests := []struct {
		name       string
		status     int
		content    string
		mutate     func(*HiveCosmQuotaObservationResponse)
		addUnknown bool
		body       string
		wantSecret string
	}{
		{name: "unauthenticated body is not parsed", status: http.StatusUnauthorized, content: "application/json", body: `{"error":"Bearer super-secret-token"}`, wantSecret: "super-secret-token"},
		{name: "unknown field", status: http.StatusOK, content: "application/json", addUnknown: true},
		{name: "tenant drift", status: http.StatusOK, content: "application/json", mutate: func(v *HiveCosmQuotaObservationResponse) { v.TenantID = "other-tenant" }},
		{name: "request drift", status: http.StatusOK, content: "application/json", mutate: func(v *HiveCosmQuotaObservationResponse) { v.Request.RuntimeID = quotaTestAgent }},
		{name: "provider drift", status: http.StatusOK, content: "application/json", mutate: func(v *HiveCosmQuotaObservationResponse) { v.Observation.Provider = "other-provider" }},
		{name: "stale observation", status: http.StatusOK, content: "application/json", mutate: func(v *HiveCosmQuotaObservationResponse) { v.Observation.ObservedAt = "2026-08-14T11:00:00Z" }},
		{name: "expired observation", status: http.StatusOK, content: "application/json", mutate: func(v *HiveCosmQuotaObservationResponse) { v.Observation.ExpiresAt = "2026-08-14T11:59:59Z" }},
		{name: "expires before observation", status: http.StatusOK, content: "application/json", mutate: func(v *HiveCosmQuotaObservationResponse) {
			v.Observation.ObservedAt = "2026-08-14T12:00:04Z"
			v.Observation.ExpiresAt = "2026-08-14T12:00:02Z"
		}},
		{name: "ratio mismatch", status: http.StatusOK, content: "application/json", mutate: func(v *HiveCosmQuotaObservationResponse) { v.Observation.Ratio = 0.5 }},
		{name: "reset expired", status: http.StatusOK, content: "application/json", mutate: func(v *HiveCosmQuotaObservationResponse) { v.Observation.Window.ResetAt = "2026-08-14T11:59:00Z" }},
		{name: "secret key value", status: http.StatusOK, content: "application/json", mutate: func(v *HiveCosmQuotaObservationResponse) { v.Observation.KeyRef = "keychain:Bearer super-secret-token" }, wantSecret: "super-secret-token"},
		{name: "wrong media type", status: http.StatusOK, content: "text/plain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := base
			if tt.mutate != nil {
				tt.mutate(&response)
			}
			body := tt.body
			if body == "" {
				body = string(quotaObservationJSON(response, tt.addUnknown))
			}
			client, closeServer := quotaTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.content)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, body)
			}))
			defer closeServer()
			_, err := client.Lookup(context.Background(), agent, runtime)
			if err == nil {
				t.Fatal("Lookup succeeded for an invalid authority response")
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

func TestQuotaObservationClientRejectsLocalBindingDriftWithoutRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*db.Agent, *db.AgentRuntime)
	}{
		{name: "workspace drift", mutate: func(_ *db.Agent, runtime *db.AgentRuntime) {
			runtime.WorkspaceID = quotaTestUUID("44444444-4444-4444-8444-444444444444")
		}},
		{name: "model null", mutate: func(agent *db.Agent, _ *db.AgentRuntime) { agent.Model = pgtype.Text{} }},
		{name: "model empty", mutate: func(agent *db.Agent, _ *db.AgentRuntime) { agent.Model = pgtype.Text{Valid: true} }},
		{name: "model whitespace", mutate: func(agent *db.Agent, _ *db.AgentRuntime) { agent.Model = pgtype.Text{String: " ", Valid: true} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, runtime := quotaTestAgentRuntime()
			tt.mutate(&agent, &runtime)
			var calls atomic.Int32
			client, closeServer := quotaTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(quotaTestResponse())
			}))
			defer closeServer()
			_, err := client.Lookup(context.Background(), agent, runtime)
			if err == nil || !strings.Contains(err.Error(), string(HiveCosmAuthoritySourceGap)) {
				t.Fatalf("error = %v, want source_gap", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("authority calls = %d, want zero on local binding drift", calls.Load())
			}
		})
	}
}

func TestQuotaObservationClientRejectsRedirectWithoutLeavingConfiguredOrigin(t *testing.T) {
	agent, runtime := quotaTestAgentRuntime()
	var redirectedCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(quotaTestResponse())
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+HiveCosmQuotaObservationEndpoint, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client, err := NewHiveCosmQuotaObservationClient(origin.URL, origin.Client(), "tenant-hivecosm-1")
	if err != nil {
		t.Fatalf("New client: %v", err)
	}
	client.now = quotaTestNow
	_, err = client.Lookup(context.Background(), agent, runtime)
	if err == nil {
		t.Fatal("Lookup succeeded after cross-origin redirect")
	}
	var authorityErr *HiveCosmAuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Kind != HiveCosmAuthoritySourceGap || authorityErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("error = %v, want source_gap 307", err)
	}
	if redirectedCalls.Load() != 0 {
		t.Fatalf("redirect target calls = %d, want zero", redirectedCalls.Load())
	}
}

func TestQuotaObservationClientRejectsOversizedResponse(t *testing.T) {
	agent, runtime := quotaTestAgentRuntime()
	client, closeServer := quotaTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"schema_version":"`+HiveCosmQuotaObservationSchema+`","padding":"`+strings.Repeat("x", maxHiveCosmAuthorityBodySize)+`"}`)
	}))
	defer closeServer()
	_, err := client.Lookup(context.Background(), agent, runtime)
	if err == nil || !strings.Contains(err.Error(), string(HiveCosmAuthoritySourceGap)) {
		t.Fatalf("error = %v, want source_gap", err)
	}
}

func quotaObservationJSONHandler(response HiveCosmQuotaObservationResponse) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
}

func quotaObservationJSON(response HiveCosmQuotaObservationResponse, addUnknown bool) []byte {
	data, err := json.Marshal(response)
	if err != nil {
		panic(err)
	}
	if !addUnknown {
		return data
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		panic(err)
	}
	object["unexpected"] = true
	data, err = json.Marshal(object)
	if err != nil {
		panic(err)
	}
	return data
}
