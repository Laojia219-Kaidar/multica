package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const authorityClientAgentID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001"

func authorityClientLookup() HiveCosmAuthorityLookup {
	return HiveCosmAuthorityLookup{
		WorkOrderSourceRef: "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-OWNER-JOURNEY-001",
		EmployeeID:         "EMP-OWNER-JOURNEY-001",
		IdentityBindingID:  "BIND-OWNER-JOURNEY-001",
		AgentID:            authorityClientAgentID,
	}
}

func validAuthorityClientEnvelope(lookup HiveCosmAuthorityLookup) map[string]any {
	employeeSourceRef := "hivecosm://employees/" + lookup.EmployeeID
	return map[string]any{
		"schema_version": HiveCosmOwnerWorkContextSchemaVersion,
		"lookup_mode":    "exact",
		"complete":       true,
		"ok":             true,
		"request": map[string]any{
			"work_order_source_ref": lookup.WorkOrderSourceRef,
			"employee_id":           lookup.EmployeeID,
			"identity_binding_id":   lookup.IdentityBindingID,
			"agent_id":              lookup.AgentID,
		},
		"work_orders": []any{map[string]any{
			"authority": authorityClientSnapshot("WorkOrder", lookup.WorkOrderSourceRef, "wo-rev-17", "a"),
		}},
		"employees": []any{map[string]any{
			"employee_id": lookup.EmployeeID,
			"authority":   authorityClientSnapshot("Employee", employeeSourceRef, "employee-rev-9", "b"),
		}},
		"identity_bindings": []any{map[string]any{
			"identity_binding_id": lookup.IdentityBindingID,
			"employee_id":         lookup.EmployeeID,
			"agent_id":            lookup.AgentID,
			"employee_ref":        employeeSourceRef,
			"agent_ref":           "/api/agents/" + lookup.AgentID,
			"active":              true,
			"authority":           authorityClientSnapshot("IdentityBinding", "hivecosm://identity-bindings/"+lookup.IdentityBindingID, "binding-rev-12", "c"),
		}},
	}
}

func authorityClientSnapshot(kind, sourceRef, revision, digestChar string) map[string]any {
	return map[string]any{
		"kind":           kind,
		"source_ref":     sourceRef,
		"revision":       revision,
		"content_digest": "sha256:" + strings.Repeat(digestChar, 64),
		"freshness":      "current",
	}
}

func writeAuthorityClientJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func newAuthorityClientForServer(t *testing.T, serverURL string) *HiveCosmAuthorityClient {
	t.Helper()
	client, err := NewHiveCosmAuthorityClient(serverURL, nil)
	if err != nil {
		t.Fatalf("NewHiveCosmAuthorityClient: %v", err)
	}
	return client
}

func requireAuthorityErrorKind(t *testing.T, err error, want HiveCosmAuthorityErrorKind) {
	t.Helper()
	var authorityErr *HiveCosmAuthorityError
	if !errors.As(err, &authorityErr) {
		t.Fatalf("error = %v, want *HiveCosmAuthorityError", err)
	}
	if authorityErr.Kind != want {
		t.Fatalf("error kind = %q, want %q (error: %v)", authorityErr.Kind, want, err)
	}
}

func TestHiveCosmAuthorityClientReadyUsesExactReadOnlyQueryAndThreeObjectBundle(t *testing.T) {
	lookup := authorityClientLookup()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != HiveCosmOwnerWorkContextEndpoint {
			t.Fatalf("path = %q, want %q", r.URL.Path, HiveCosmOwnerWorkContextEndpoint)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if len(body) != 0 {
			t.Fatalf("read-only lookup sent body %q", body)
		}
		query := r.URL.Query()
		if len(query) != 4 {
			t.Fatalf("query keys = %v, want exactly four authority selectors", query)
		}
		wantQuery := url.Values{
			"work_order_source_ref": {lookup.WorkOrderSourceRef},
			"employee_id":           {lookup.EmployeeID},
			"identity_binding_id":   {lookup.IdentityBindingID},
			"agent_id":              {lookup.AgentID},
		}
		if query.Encode() != wantQuery.Encode() {
			t.Fatalf("query = %q, want %q", query.Encode(), wantQuery.Encode())
		}
		if query.Has("session") || query.Has("session_id") {
			t.Fatalf("session leaked into authority query: %v", query)
		}
		if strings.Contains(r.URL.RawQuery, lookup.WorkOrderSourceRef) {
			t.Fatalf("special source_ref was not URL encoded: %q", r.URL.RawQuery)
		}
		writeAuthorityClientJSON(t, w, http.StatusOK, validAuthorityClientEnvelope(lookup))
	}))
	defer srv.Close()

	bundle, err := newAuthorityClientForServer(t, srv.URL+"/ignored-base-path").ResolveOwnerWorkContext(context.Background(), lookup)
	if err != nil {
		t.Fatalf("ResolveOwnerWorkContext: %v", err)
	}
	if bundle.WorkOrder.SourceRef != lookup.WorkOrderSourceRef || bundle.WorkOrder.Revision != "wo-rev-17" {
		t.Fatalf("WorkOrder authority was not preserved exactly: %+v", bundle.WorkOrder)
	}
	if bundle.WorkOrder.ContentDigest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("upstream WorkOrder digest was changed: %q", bundle.WorkOrder.ContentDigest)
	}
	if bundle.Employee.SourceRef != "hivecosm://employees/"+lookup.EmployeeID {
		t.Fatalf("Employee authority = %+v", bundle.Employee)
	}
	if bundle.IdentityBinding.AgentRef != "/api/agents/"+lookup.AgentID || !bundle.IdentityBinding.Active {
		t.Fatalf("IdentityBinding edge = %+v", bundle.IdentityBinding)
	}
	if bundle.RequestedAgentID != lookup.AgentID {
		t.Fatalf("RequestedAgentID = %q, want %q", bundle.RequestedAgentID, lookup.AgentID)
	}
}

func TestHiveCosmAuthorityClientEndpoint404IsUnsupported(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
	}{
		{name: "plain framework 404", handler: http.NotFoundHandler()},
		{name: "generic JSON 404", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeAuthorityClientJSON(t, w, http.StatusNotFound, map[string]any{"error": "route not found"})
		})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			_, err := newAuthorityClientForServer(t, srv.URL).ResolveOwnerWorkContext(context.Background(), authorityClientLookup())
			requireAuthorityErrorKind(t, err, HiveCosmAuthorityUnsupported)
		})
	}
}

func TestHiveCosmAuthorityClientStructuredExactObject404IsNotFound(t *testing.T) {
	lookup := authorityClientLookup()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAuthorityClientJSON(t, w, http.StatusNotFound, map[string]any{
			"schema_version": HiveCosmOwnerWorkContextSchemaVersion,
			"lookup_mode":    "exact",
			"complete":       false,
			"ok":             false,
			"request": map[string]any{
				"work_order_source_ref": lookup.WorkOrderSourceRef,
				"employee_id":           lookup.EmployeeID,
				"identity_binding_id":   lookup.IdentityBindingID,
				"agent_id":              lookup.AgentID,
			},
			"error": map[string]any{
				"code":            "not_found",
				"object_kind":     "identity_binding",
				"requested_value": lookup.IdentityBindingID,
			},
		})
	}))
	defer srv.Close()

	_, err := newAuthorityClientForServer(t, srv.URL).ResolveOwnerWorkContext(context.Background(), lookup)
	requireAuthorityErrorKind(t, err, HiveCosmAuthorityNotFound)
}

func TestHiveCosmAuthorityClientSourceGapsNeverBecomeReady(t *testing.T) {
	t.Run("transport", func(t *testing.T) {
		client, err := NewHiveCosmAuthorityClient("https://hivecosm.invalid", &http.Client{Transport: authorityClientRoundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("upstream unavailable")
		})})
		if err != nil {
			t.Fatalf("NewHiveCosmAuthorityClient: %v", err)
		}
		_, err = client.ResolveOwnerWorkContext(context.Background(), authorityClientLookup())
		requireAuthorityErrorKind(t, err, HiveCosmAuthoritySourceGap)
	})

	t.Run("non JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("<html>upstream proxy error</html>"))
		}))
		defer srv.Close()
		_, err := newAuthorityClientForServer(t, srv.URL).ResolveOwnerWorkContext(context.Background(), authorityClientLookup())
		requireAuthorityErrorKind(t, err, HiveCosmAuthoritySourceGap)
	})

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			lookup := authorityClientLookup()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeAuthorityClientJSON(t, w, status, map[string]any{
					"schema_version": HiveCosmOwnerWorkContextSchemaVersion,
					"lookup_mode":    "exact",
					"complete":       false,
					"ok":             false,
					"request": map[string]any{
						"work_order_source_ref": lookup.WorkOrderSourceRef,
						"employee_id":           lookup.EmployeeID,
						"identity_binding_id":   lookup.IdentityBindingID,
						"agent_id":              lookup.AgentID,
					},
					"error": map[string]any{
						"code":        map[bool]string{true: "auth_required", false: "auth_invalid"}[status == http.StatusUnauthorized],
						"object_kind": "request",
					},
				})
			}))
			defer srv.Close()

			_, err := newAuthorityClientForServer(t, srv.URL).ResolveOwnerWorkContext(context.Background(), lookup)
			requireAuthorityErrorKind(t, err, HiveCosmAuthoritySourceGap)
		})
	}
}

type authorityClientRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f authorityClientRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHiveCosmAuthorityClientRejectsListCappedAndIncompleteResponses(t *testing.T) {
	lookup := authorityClientLookup()
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "list projection", mutate: func(body map[string]any) { body["lookup_mode"] = "list" }},
		{name: "capped projection", mutate: func(body map[string]any) { body["lookup_mode"] = "capped" }},
		{name: "incomplete marker", mutate: func(body map[string]any) { body["complete"] = false }},
		{name: "missing WorkOrder object", mutate: func(body map[string]any) { body["work_orders"] = []any{} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := validAuthorityClientEnvelope(lookup)
			tt.mutate(body)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeAuthorityClientJSON(t, w, http.StatusOK, body)
			}))
			defer srv.Close()
			_, err := newAuthorityClientForServer(t, srv.URL).ResolveOwnerWorkContext(context.Background(), lookup)
			requireAuthorityErrorKind(t, err, HiveCosmAuthorityInvalid)
		})
	}
}

func TestHiveCosmAuthorityClientRejectsDuplicateOrConflictingMatches(t *testing.T) {
	lookup := authorityClientLookup()
	tests := []struct {
		name string
		key  string
	}{
		{name: "WorkOrders", key: "work_orders"},
		{name: "Employees", key: "employees"},
		{name: "IdentityBindings", key: "identity_bindings"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := validAuthorityClientEnvelope(lookup)
			objects := body[tt.key].([]any)
			body[tt.key] = append(objects, objects[0])
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeAuthorityClientJSON(t, w, http.StatusOK, body)
			}))
			defer srv.Close()
			_, err := newAuthorityClientForServer(t, srv.URL).ResolveOwnerWorkContext(context.Background(), lookup)
			requireAuthorityErrorKind(t, err, HiveCosmAuthorityConflict)
		})
	}
}

func TestHiveCosmAuthorityClientRejectsBindingMismatchOrInactiveEdge(t *testing.T) {
	lookup := authorityClientLookup()
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "binding ID mismatch", mutate: func(binding map[string]any) { binding["identity_binding_id"] = "BIND-DISPLAY-NAME-FALLBACK" }},
		{name: "employee ID mismatch", mutate: func(binding map[string]any) { binding["employee_id"] = "EMP-OTHER" }},
		{name: "agent ID mismatch", mutate: func(binding map[string]any) { binding["agent_id"] = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c002" }},
		{name: "employee ref mismatch", mutate: func(binding map[string]any) { binding["employee_ref"] = "hivecosm://employees/EMP-OTHER" }},
		{name: "agent ref mismatch", mutate: func(binding map[string]any) {
			binding["agent_ref"] = "/api/agents/01972f7e-7e8d-77ef-a13d-1b0ce3e9c002"
		}},
		{name: "inactive", mutate: func(binding map[string]any) { binding["active"] = false }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := validAuthorityClientEnvelope(lookup)
			binding := body["identity_bindings"].([]any)[0].(map[string]any)
			tt.mutate(binding)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeAuthorityClientJSON(t, w, http.StatusOK, body)
			}))
			defer srv.Close()
			_, err := newAuthorityClientForServer(t, srv.URL).ResolveOwnerWorkContext(context.Background(), lookup)
			requireAuthorityErrorKind(t, err, HiveCosmAuthorityInvalid)
		})
	}
}

func TestHiveCosmAuthorityClientRejectsMissingRevisionDigestOrCurrent(t *testing.T) {
	lookup := authorityClientLookup()
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing revision", mutate: func(authority map[string]any) { authority["revision"] = "" }},
		{name: "missing digest", mutate: func(authority map[string]any) { authority["content_digest"] = "" }},
		{name: "malformed digest", mutate: func(authority map[string]any) { authority["content_digest"] = "sha256:short" }},
		{name: "not current", mutate: func(authority map[string]any) { authority["freshness"] = "stale" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := validAuthorityClientEnvelope(lookup)
			authority := body["work_orders"].([]any)[0].(map[string]any)["authority"].(map[string]any)
			tt.mutate(authority)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeAuthorityClientJSON(t, w, http.StatusOK, body)
			}))
			defer srv.Close()
			_, err := newAuthorityClientForServer(t, srv.URL).ResolveOwnerWorkContext(context.Background(), lookup)
			requireAuthorityErrorKind(t, err, HiveCosmAuthorityInvalid)
		})
	}
}

func TestHiveCosmAuthorityClientRejectsHiveCosmAgentSnapshot(t *testing.T) {
	lookup := authorityClientLookup()
	body := validAuthorityClientEnvelope(lookup)
	body["agents"] = []any{map[string]any{
		"agent_id": lookup.AgentID,
		"authority": authorityClientSnapshot(
			"Agent",
			"hivecosm://agents/"+lookup.AgentID,
			"forged-agent-revision",
			"f",
		),
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeAuthorityClientJSON(t, w, http.StatusOK, body)
	}))
	defer srv.Close()

	_, err := newAuthorityClientForServer(t, srv.URL).ResolveOwnerWorkContext(context.Background(), lookup)
	requireAuthorityErrorKind(t, err, HiveCosmAuthorityInvalid)
}

func TestHiveCosmAuthorityClientFailsClosedOnMissingOrNonCanonicalSelectors(t *testing.T) {
	base := authorityClientLookup()
	tests := []struct {
		name   string
		mutate func(*HiveCosmAuthorityLookup)
	}{
		{name: "missing WorkOrder source ref", mutate: func(lookup *HiveCosmAuthorityLookup) { lookup.WorkOrderSourceRef = "" }},
		{name: "legacy WorkOrder source ref", mutate: func(lookup *HiveCosmAuthorityLookup) { lookup.WorkOrderSourceRef = "hivecosm://work-orders/WO-P2-001" }},
		{name: "WorkOrder query", mutate: func(lookup *HiveCosmAuthorityLookup) { lookup.WorkOrderSourceRef += "?revision=2" }},
		{name: "WorkOrder fragment", mutate: func(lookup *HiveCosmAuthorityLookup) { lookup.WorkOrderSourceRef += "#draft" }},
		{name: "missing Employee ID", mutate: func(lookup *HiveCosmAuthorityLookup) { lookup.EmployeeID = "" }},
		{name: "missing IdentityBinding ID", mutate: func(lookup *HiveCosmAuthorityLookup) { lookup.IdentityBindingID = "" }},
		{name: "Agent display name instead of UUID", mutate: func(lookup *HiveCosmAuthorityLookup) { lookup.AgentID = "Platform Engineer" }},
	}

	client, err := NewHiveCosmAuthorityClient("https://hivecosm.invalid", &http.Client{Transport: authorityClientRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid selectors must fail before transport")
		return nil, nil
	})})
	if err != nil {
		t.Fatalf("NewHiveCosmAuthorityClient: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := base
			tt.mutate(&lookup)
			_, err := client.ResolveOwnerWorkContext(context.Background(), lookup)
			requireAuthorityErrorKind(t, err, HiveCosmAuthorityInvalid)
		})
	}
}
