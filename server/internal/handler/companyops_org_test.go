package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const handlerDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var (
	handlerWorkspaceID = handlerUUID("aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee")
	handlerAgentID     = handlerUUID("11111111-2222-4333-8444-555555555555")
	handlerRuntimeID   = handlerUUID("22222222-3333-4444-8555-666666666666")
)

func handlerUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		panic(err)
	}
	return id
}

func withDirectoryWorkspace(request *http.Request) *http.Request {
	ctx := middleware.SetMemberContext(request.Context(), "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee", db.Member{})
	return request.WithContext(ctx)
}

type handlerAdapter struct {
	organization *companyopsapi.AdapterOrganizationResponse
	employees    *companyopsapi.AdapterEmployeesResponse
	employee     *companyopsapi.AdapterEmployeeDetailResponse
	employeesErr error
	employeeErr  error
}

func (s *handlerAdapter) GetOrganization(context.Context, string) (*companyopsapi.AdapterOrganizationResponse, error) {
	return s.organization, nil
}

func (s *handlerAdapter) GetEmployees(context.Context, string) (*companyopsapi.AdapterEmployeesResponse, error) {
	return s.employees, s.employeesErr
}

func (s *handlerAdapter) GetEmployee(context.Context, string, string) (*companyopsapi.AdapterEmployeeDetailResponse, error) {
	return s.employee, s.employeeErr
}

type handlerLookup struct{}

func (handlerLookup) GetAgentInWorkspace(context.Context, db.GetAgentInWorkspaceParams) (db.Agent, error) {
	return db.Agent{
		ID: handlerAgentID, WorkspaceID: handlerWorkspaceID, Name: "Alice",
		Kind: "user", Status: "idle", RuntimeID: handlerRuntimeID, RuntimeMode: "local",
	}, nil
}

func (handlerLookup) GetAgentRuntimeForWorkspace(context.Context, db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error) {
	return db.AgentRuntime{
		ID: handlerRuntimeID, WorkspaceID: handlerWorkspaceID, RuntimeMode: "local", Status: "online",
	}, nil
}

func handlerAuthority() companyopsapi.AdapterAuthorityRef {
	return companyopsapi.AdapterAuthorityRef{
		SourceRef:         companyopsapi.AdapterAuthoritySourceRef,
		SourceVersion:     companyopsapi.AdapterAuthoritySourceVersion,
		SourceRevision:    handlerDigest,
		ContentDigest:     handlerDigest,
		ObservedAt:        "2026-08-12T00:59:00.000Z",
		SourceGeneratedAt: "2026-08-12T00:59:00.000Z",
		Freshness:         companyopsapi.AdapterAuthorityFreshness,
		ReadModelOnly:     true,
	}
}

func handlerSummary() companyopsapi.AdapterEmployeeSummary {
	agentID := "11111111-2222-4333-8444-555555555555"
	return companyopsapi.AdapterEmployeeSummary{
		EmployeeID: "DE-ALICE", WorkforceAgentID: "KT-ALICE", DisplayName: "Alice",
		EmployeeContractState: "existing_digital_employee_contract",
		DepartmentID:          "DEPT-001", DepartmentName: "Engineering",
		PositionID: "POS-001", PositionTitle: "Developer",
		BindingState: companyopsapi.BindingStateUniqueActiveCandidate,
		Binding: companyopsapi.AdapterBindingProjection{
			State:         companyopsapi.BindingStateUniqueActiveCandidate,
			CandidateOnly: true, HiveCrewAgentID: &agentID,
		},
	}
}

func handlerOrganization() *companyopsapi.AdapterOrganizationResponse {
	return &companyopsapi.AdapterOrganizationResponse{
		SchemaVersion: companyopsapi.HiveCrewOrganizationSchema,
		OK:            true, TenantID: "tenant", WorkspaceID: "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee",
		Authority: handlerAuthority(),
		Departments: []companyopsapi.AdapterOrganizationDepartment{{
			DepartmentID: "DEPT-001", DepartmentName: "Engineering", Mission: "Build", EmployeeCount: 1,
			Positions: []companyopsapi.AdapterOrganizationPosition{{
				PositionID: "POS-001", PositionTitle: "Developer", EmployeeCount: 1,
				EmployeeIDs: []string{"DE-ALICE"},
				Appointments: []companyopsapi.AdapterOrganizationAppointment{{
					AppointmentID: "APPOINTMENT-DE-ALICE-POS-001",
					EmployeeID:    "DE-ALICE", WorkforceAgentID: "KT-ALICE",
				}},
			}},
		}},
	}
}

func newHandlerDirectoryService(adapter *handlerAdapter) *service.CompanyOpsDirectoryService {
	if adapter.organization == nil {
		adapter.organization = handlerOrganization()
	}
	if adapter.employees == nil && adapter.employeesErr == nil {
		adapter.employees = &companyopsapi.AdapterEmployeesResponse{
			SchemaVersion: companyopsapi.HiveCrewEmployeesSchema,
			OK:            true, TenantID: "tenant", WorkspaceID: "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee",
			Authority: handlerAuthority(),
			Employees: []companyopsapi.AdapterEmployeeSummary{handlerSummary()},
		}
	}
	if adapter.employee == nil && adapter.employeeErr == nil {
		adapter.employee = &companyopsapi.AdapterEmployeeDetailResponse{
			SchemaVersion: companyopsapi.HiveCrewEmployeeSchema,
			OK:            true, TenantID: "tenant", WorkspaceID: "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee",
			Authority: handlerAuthority(),
			Employee:  handlerSummary(),
			Bindings: []companyopsapi.AdapterBindingDetail{{
				IdentityBindingID: "IB-ABCDEF0123456789ABCDEF01",
				WorkforceAgentID:  "KT-ALICE",
				HiveCrewAgentID:   "11111111-2222-4333-8444-555555555555",
				AgentRef:          "/api/agents/11111111-2222-4333-8444-555555555555",
				Active:            true,
				EffectiveFrom:     "2026-08-01T00:00:00.000Z",
				Authority: companyopsapi.AdapterBindingAuthority{
					SourceRef:     "hivecosm://identity-bindings/IB-ABCDEF0123456789ABCDEF01",
					Revision:      handlerDigest,
					ContentDigest: handlerDigest,
					CandidateOnly: true,
				},
			}},
			DossierEnrichment: companyopsapi.AdapterDossierEnrichment{
				State: "available",
				Available: &companyopsapi.AdapterDossierAvailable{
					State: "available", SourceVersion: "EmployeeOperatingViewV2",
					GeneratedAt: "2026-08-12T00:59:00.000Z",
					ExecutionBridge: companyopsapi.AdapterDossierExecutionBridge{
						Source: "HQ-06", State: "hq06_goal_run_linked",
						Configured: true, Reachable: true, ProjectID: "PRJ-HCW-V2",
						TaskIDs: []string{},
					},
					Boundaries: companyopsapi.AdapterDossierBoundaries{ReadModelOnly: true},
				},
			},
		}
	}
	return service.NewCompanyOpsDirectoryService(adapter, handlerLookup{})
}

func assertDirectorySecurityHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestCompanyOpsDirectoryHandlersReturnStrictPublicEnvelopes(t *testing.T) {
	handler := &Handler{CompanyOpsDirectory: newHandlerDirectoryService(&handlerAdapter{})}
	tests := []struct {
		name string
		path string
		call func(http.ResponseWriter, *http.Request)
		keys []string
	}{
		{"organization", "/api/company-ops/organization", handler.GetCompanyOpsOrganization, []string{"schema_version", "workspace_id", "authority", "departments"}},
		{"employees", "/api/company-ops/employees", handler.GetCompanyOpsEmployees, []string{"schema_version", "workspace_id", "authority", "items", "total", "limit", "offset"}},
		{"employee", "/api/company-ops/employees/DE-ALICE", handler.GetCompanyOpsEmployee, []string{"schema_version", "workspace_id", "authority", "employee", "bindings", "dossier_enrichment"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := withDirectoryWorkspace(httptest.NewRequest(http.MethodGet, test.path, nil))
			if test.name == "employee" {
				request.SetPathValue("employeeId", "DE-ALICE")
			}
			recorder := httptest.NewRecorder()
			test.call(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
			assertDirectorySecurityHeaders(t, recorder)
			var object map[string]json.RawMessage
			if err := json.Unmarshal(recorder.Body.Bytes(), &object); err != nil {
				t.Fatal(err)
			}
			if len(object) != len(test.keys) {
				t.Fatalf("keys = %v", object)
			}
			for _, key := range test.keys {
				if _, exists := object[key]; !exists {
					t.Fatalf("missing key %s", key)
				}
			}
		})
	}
}

func TestCompanyOpsEmployeesRejectsNoncanonicalQueries(t *testing.T) {
	handler := &Handler{CompanyOpsDirectory: newHandlerDirectoryService(&handlerAdapter{})}
	queries := []string{
		"unknown=1",
		"q=",
		"q=%20alice",
		"q=alice&q=bob",
		"status=invented",
		"status=available&status=none",
		"limit=0",
		"limit=01",
		"limit=%2B1",
		"limit=501",
		"limit=1&limit=2",
		"offset=-1",
		"offset=01",
		"offset=%2B1",
		"offset=0&offset=1",
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			request := withDirectoryWorkspace(httptest.NewRequest(http.MethodGet, "/api/company-ops/employees?"+query, nil))
			recorder := httptest.NewRecorder()
			handler.GetCompanyOpsEmployees(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
			assertDirectorySecurityHeaders(t, recorder)
		})
	}
}

func TestCompanyOpsOrganizationAndDetailRejectAnyQuery(t *testing.T) {
	handler := &Handler{CompanyOpsDirectory: newHandlerDirectoryService(&handlerAdapter{})}
	for _, test := range []struct {
		path string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"/api/company-ops/organization?workspace_id=other", handler.GetCompanyOpsOrganization},
		{"/api/company-ops/employees/DE-ALICE?expand=secret", handler.GetCompanyOpsEmployee},
	} {
		request := withDirectoryWorkspace(httptest.NewRequest(http.MethodGet, test.path, nil))
		request.SetPathValue("employeeId", "DE-ALICE")
		recorder := httptest.NewRecorder()
		test.call(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", recorder.Code)
		}
	}
}

func TestCompanyOpsEmployeeIDValidation(t *testing.T) {
	tests := map[string]bool{
		"DE-ALICE":       true,
		"DE-A1":          true,
		"DE-ABC_def-123": false,
		"DE-A_1":         true,
		"DE-":            false,
		"DE-A":           false,
		"DE-abc":         false,
		"KT-ALICE":       false,
		" DE-ALICE":      false,
	}
	for value, want := range tests {
		if got := isValidEmployeeID(value); got != want {
			t.Fatalf("isValidEmployeeID(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestCompanyOpsDirectoryErrorsAreStableAndDoNotLeakRawValues(t *testing.T) {
	rawTenant := "tenant-secret-123"
	rawWorkspace := "workspace-secret-456"
	rawEmployee := "DE-PRIVATE"
	tests := []struct {
		name string
		err  error
		call func(*Handler, http.ResponseWriter, *http.Request)
		path string
		code int
		raw  string
	}{
		{
			"tenant", fmt.Errorf("%w: %s", companyopsapi.ErrAdapterTenantMismatch, rawTenant),
			func(handler *Handler, w http.ResponseWriter, r *http.Request) { handler.GetCompanyOpsEmployees(w, r) },
			"/api/company-ops/employees", http.StatusForbidden, rawTenant,
		},
		{
			"workspace", fmt.Errorf("%w: %s", companyopsapi.ErrAdapterWorkspaceMismatch, rawWorkspace),
			func(handler *Handler, w http.ResponseWriter, r *http.Request) { handler.GetCompanyOpsEmployees(w, r) },
			"/api/company-ops/employees", http.StatusForbidden, rawWorkspace,
		},
		{
			"employee", fmt.Errorf("%w: %s", companyopsapi.ErrAdapterNotFound, rawEmployee),
			func(handler *Handler, w http.ResponseWriter, r *http.Request) {
				r.SetPathValue("employeeId", "DE-ALICE")
				handler.GetCompanyOpsEmployee(w, r)
			},
			"/api/company-ops/employees/DE-ALICE", http.StatusNotFound, rawEmployee,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &handlerAdapter{}
			if test.name == "employee" {
				adapter.employeeErr = test.err
			} else {
				adapter.employeesErr = test.err
			}
			handler := &Handler{CompanyOpsDirectory: newHandlerDirectoryService(adapter)}
			request := withDirectoryWorkspace(httptest.NewRequest(http.MethodGet, test.path, nil))
			recorder := httptest.NewRecorder()
			test.call(handler, recorder, request)
			if recorder.Code != test.code {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
			assertDirectorySecurityHeaders(t, recorder)
			if strings.Contains(recorder.Body.String(), test.raw) {
				t.Fatalf("response leaked raw upstream value: %s", recorder.Body.String())
			}
		})
	}
}

func TestCompanyOpsDirectoryFailsClosedWithoutContextOrService(t *testing.T) {
	t.Run("nil service", func(t *testing.T) {
		handler := &Handler{}
		request := withDirectoryWorkspace(httptest.NewRequest(http.MethodGet, "/api/company-ops/organization", nil))
		recorder := httptest.NewRecorder()
		handler.GetCompanyOpsOrganization(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", recorder.Code)
		}
		assertDirectorySecurityHeaders(t, recorder)
	})
	t.Run("missing workspace", func(t *testing.T) {
		handler := &Handler{CompanyOpsDirectory: newHandlerDirectoryService(&handlerAdapter{})}
		request := httptest.NewRequest(http.MethodGet, "/api/company-ops/organization", nil)
		recorder := httptest.NewRecorder()
		handler.GetCompanyOpsOrganization(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", recorder.Code)
		}
		assertDirectorySecurityHeaders(t, recorder)
	})
}

func TestCompanyOpsNoStorePrecedesEarlyGuards(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	recorder := httptest.NewRecorder()
	CompanyOpsNoStore(next).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/company-ops/organization", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}
	assertDirectorySecurityHeaders(t, recorder)
}

func TestParseCanonicalDecimal(t *testing.T) {
	for _, test := range []struct {
		value     string
		allowZero bool
		want      int
		ok        bool
	}{
		{"1", false, 1, true},
		{"500", false, 500, true},
		{"0", true, 0, true},
		{"0", false, 0, false},
		{"01", true, 0, false},
		{"+1", true, 0, false},
		{" 1", true, 0, false},
	} {
		got, ok := parseCanonicalDecimal(test.value, test.allowZero)
		if got != test.want || ok != test.ok {
			t.Fatalf("parseCanonicalDecimal(%q) = (%d,%v)", test.value, got, ok)
		}
	}
}
