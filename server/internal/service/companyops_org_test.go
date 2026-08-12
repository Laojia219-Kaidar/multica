package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	testWorkspaceUUID = companyOpsTestUUID("aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee")
	testAgentUUID     = companyOpsTestUUID("11111111-2222-4333-8444-555555555555")
	testRuntimeUUID   = companyOpsTestUUID("22222222-3333-4444-8555-666666666666")
	otherWorkspace    = companyOpsTestUUID("ffffffff-ffff-4fff-8fff-ffffffffffff")
)

const serviceDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func companyOpsTestUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		panic(err)
	}
	return id
}

type stubDirectoryAdapter struct {
	organization      *companyopsapi.AdapterOrganizationResponse
	employees         *companyopsapi.AdapterEmployeesResponse
	employee          *companyopsapi.AdapterEmployeeDetailResponse
	organizationErr   error
	employeesErr      error
	employeeErr       error
	organizationWS    string
	employeesWS       string
	employeeWS        string
	requestedEmployee string
}

func (s *stubDirectoryAdapter) GetOrganization(_ context.Context, workspaceID string) (*companyopsapi.AdapterOrganizationResponse, error) {
	s.organizationWS = workspaceID
	return s.organization, s.organizationErr
}

func (s *stubDirectoryAdapter) GetEmployees(_ context.Context, workspaceID string) (*companyopsapi.AdapterEmployeesResponse, error) {
	s.employeesWS = workspaceID
	return s.employees, s.employeesErr
}

func (s *stubDirectoryAdapter) GetEmployee(_ context.Context, workspaceID, employeeID string) (*companyopsapi.AdapterEmployeeDetailResponse, error) {
	s.employeeWS = workspaceID
	s.requestedEmployee = employeeID
	return s.employee, s.employeeErr
}

type stubAgentLookup struct {
	agent         db.Agent
	agentErr      error
	runtime       db.AgentRuntime
	runtimeErr    error
	agentCalls    int
	runtimeCalls  int
	agentParams   db.GetAgentInWorkspaceParams
	runtimeParams db.GetAgentRuntimeForWorkspaceParams
}

func (s *stubAgentLookup) GetAgentInWorkspace(_ context.Context, params db.GetAgentInWorkspaceParams) (db.Agent, error) {
	s.agentCalls++
	s.agentParams = params
	return s.agent, s.agentErr
}

func (s *stubAgentLookup) GetAgentRuntimeForWorkspace(_ context.Context, params db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error) {
	s.runtimeCalls++
	s.runtimeParams = params
	return s.runtime, s.runtimeErr
}

func serviceAuthority() companyopsapi.AdapterAuthorityRef {
	return companyopsapi.AdapterAuthorityRef{
		SourceRef:         companyopsapi.AdapterAuthoritySourceRef,
		SourceVersion:     companyopsapi.AdapterAuthoritySourceVersion,
		SourceRevision:    serviceDigest,
		ContentDigest:     serviceDigest,
		ObservedAt:        "2026-08-12T00:59:00.000Z",
		SourceGeneratedAt: "2026-08-12T00:59:00.000Z",
		Freshness:         companyopsapi.AdapterAuthorityFreshness,
		ReadModelOnly:     true,
	}
}

func serviceSummary(state string, agentID *string) companyopsapi.AdapterEmployeeSummary {
	return companyopsapi.AdapterEmployeeSummary{
		EmployeeID:            "DE-ALICE",
		WorkforceAgentID:      "KT-ALICE",
		DisplayName:           "Alice",
		EmployeeContractState: "existing_digital_employee_contract",
		DepartmentID:          "DEPT-001",
		DepartmentName:        "Engineering",
		PositionID:            "POS-001",
		PositionTitle:         "Developer",
		BindingState:          state,
		Binding: companyopsapi.AdapterBindingProjection{
			State:                 state,
			CandidateOnly:         true,
			ExecutabilityVerified: false,
			HiveCrewAgentID:       agentID,
		},
	}
}

func serviceEmployees(summary companyopsapi.AdapterEmployeeSummary) *companyopsapi.AdapterEmployeesResponse {
	return &companyopsapi.AdapterEmployeesResponse{
		SchemaVersion: companyopsapi.HiveCrewEmployeesSchema,
		OK:            true,
		TenantID:      "tenant",
		WorkspaceID:   "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee",
		Authority:     serviceAuthority(),
		Employees:     []companyopsapi.AdapterEmployeeSummary{summary},
	}
}

func serviceOrganization() *companyopsapi.AdapterOrganizationResponse {
	return &companyopsapi.AdapterOrganizationResponse{
		SchemaVersion: companyopsapi.HiveCrewOrganizationSchema,
		OK:            true,
		TenantID:      "tenant",
		WorkspaceID:   "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee",
		Authority:     serviceAuthority(),
		Departments: []companyopsapi.AdapterOrganizationDepartment{{
			DepartmentID:   "DEPT-001",
			DepartmentName: "Engineering",
			Mission:        "Build",
			EmployeeCount:  1,
			Positions: []companyopsapi.AdapterOrganizationPosition{{
				PositionID:    "POS-001",
				PositionTitle: "Developer",
				EmployeeCount: 1,
				EmployeeIDs:   []string{"DE-ALICE"},
				Appointments: []companyopsapi.AdapterOrganizationAppointment{{
					AppointmentID:    "APPOINTMENT-DE-ALICE-POS-001",
					EmployeeID:       "DE-ALICE",
					WorkforceAgentID: "KT-ALICE",
				}},
			}},
		}},
	}
}

func serviceDetail(summary companyopsapi.AdapterEmployeeSummary) *companyopsapi.AdapterEmployeeDetailResponse {
	return &companyopsapi.AdapterEmployeeDetailResponse{
		SchemaVersion: companyopsapi.HiveCrewEmployeeSchema,
		OK:            true,
		TenantID:      "tenant",
		WorkspaceID:   "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee",
		Authority:     serviceAuthority(),
		Employee:      summary,
		Bindings: []companyopsapi.AdapterBindingDetail{{
			IdentityBindingID: "IB-ABCDEF0123456789ABCDEF01",
			WorkforceAgentID:  "KT-ALICE",
			HiveCrewAgentID:   "11111111-2222-4333-8444-555555555555",
			AgentRef:          "/api/agents/11111111-2222-4333-8444-555555555555",
			Active:            true,
			EffectiveFrom:     "2026-08-01T00:00:00.000Z",
			Authority: companyopsapi.AdapterBindingAuthority{
				SourceRef:     "hivecosm://identity-bindings/IB-ABCDEF0123456789ABCDEF01",
				Revision:      serviceDigest,
				ContentDigest: serviceDigest,
				CandidateOnly: true,
			},
		}},
		DossierEnrichment: companyopsapi.AdapterDossierEnrichment{
			State: "available",
			Available: &companyopsapi.AdapterDossierAvailable{
				State:         "available",
				SourceVersion: "EmployeeOperatingViewV2",
				GeneratedAt:   "2026-08-12T00:59:00.000Z",
				ExecutionBridge: companyopsapi.AdapterDossierExecutionBridge{
					Source: "HQ-06", State: "hq06_goal_run_linked", Configured: true,
					Reachable: true, ProjectID: "PRJ-HCW-V2", TaskIDs: []string{},
				},
				Boundaries: companyopsapi.AdapterDossierBoundaries{ReadModelOnly: true},
			},
		},
	}
}

func executableAgent() db.Agent {
	return db.Agent{
		ID:          testAgentUUID,
		WorkspaceID: testWorkspaceUUID,
		Name:        "Alice",
		Kind:        "user",
		Status:      "idle",
		RuntimeID:   testRuntimeUUID,
		RuntimeMode: "local",
		Model:       pgtype.Text{String: "qwen3.7-plus", Valid: true},
	}
}

func onlineRuntime() db.AgentRuntime {
	return db.AgentRuntime{
		ID:          testRuntimeUUID,
		WorkspaceID: testWorkspaceUUID,
		RuntimeMode: "local",
		Status:      "online",
	}
}

func TestDirectoryServiceMapsAllSixAvailabilityStates(t *testing.T) {
	agentID := "11111111-2222-4333-8444-555555555555"
	tests := []struct {
		name          string
		summary       companyopsapi.AdapterEmployeeSummary
		lookup        *stubAgentLookup
		want          string
		wantCalls     int
		wantAgentLink bool
	}{
		{"none", serviceSummary(companyopsapi.BindingStateNone, nil), &stubAgentLookup{}, companyopsapi.AvailabilityNone, 0, false},
		{"inactive", serviceSummary(companyopsapi.BindingStateInactiveOnly, nil), &stubAgentLookup{}, companyopsapi.AvailabilityInactiveOnly, 0, false},
		{"conflict", serviceSummary(companyopsapi.BindingStateMultiConflict, nil), &stubAgentLookup{}, companyopsapi.AvailabilityMultiConflict, 0, false},
		{"source gap", serviceSummary(companyopsapi.BindingStateSourceGap, nil), &stubAgentLookup{}, companyopsapi.AvailabilitySourceGap, 0, false},
		{"local missing", serviceSummary(companyopsapi.BindingStateUniqueActiveCandidate, &agentID), &stubAgentLookup{agentErr: errors.New("missing")}, companyopsapi.AvailabilityMissingOrInvalid, 1, false},
		{"available", serviceSummary(companyopsapi.BindingStateUniqueActiveCandidate, &agentID), &stubAgentLookup{agent: executableAgent(), runtime: onlineRuntime()}, companyopsapi.AvailabilityAvailable, 1, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &stubDirectoryAdapter{employees: serviceEmployees(test.summary)}
			service := NewCompanyOpsDirectoryService(adapter, test.lookup)
			result, err := service.GetEmployees(context.Background(), testWorkspaceUUID, "", "", 50, 0)
			if err != nil {
				t.Fatal(err)
			}
			item := result.Items[0]
			if item.Availability != test.want {
				t.Fatalf("availability = %s, want %s", item.Availability, test.want)
			}
			if test.lookup.agentCalls != test.wantCalls {
				t.Fatalf("agent calls = %d, want %d", test.lookup.agentCalls, test.wantCalls)
			}
			if (item.LocalAgent != nil) != test.wantAgentLink {
				t.Fatalf("local agent presence = %v", item.LocalAgent != nil)
			}
			if !test.wantAgentLink && (item.HiveCrewAgentID != "" || item.Binding.HiveCrewAgentID != nil) {
				t.Fatal("non-available row leaked HiveCrew Agent identity")
			}
			if test.wantAgentLink {
				if item.Binding.ExecutabilityVerified != true ||
					item.Binding.HiveCrewAgentID == nil ||
					item.LocalAgent.RuntimeMode != "local" ||
					item.LocalAgent.Model == nil ||
					*item.LocalAgent.Model != "qwen3.7-plus" {
					t.Fatal("available row did not preserve exact executable Agent projection")
				}
			}
		})
	}
}

func TestDirectoryServiceRejectsMalformedLocalRows(t *testing.T) {
	agentID := "11111111-2222-4333-8444-555555555555"
	tests := map[string]func(*db.Agent, *db.AgentRuntime){
		"wrong agent id": func(agent *db.Agent, _ *db.AgentRuntime) {
			agent.ID = companyOpsTestUUID("33333333-4444-4555-8666-777777777777")
		},
		"wrong agent workspace": func(agent *db.Agent, _ *db.AgentRuntime) { agent.WorkspaceID = otherWorkspace },
		"wrong kind":            func(agent *db.Agent, _ *db.AgentRuntime) { agent.Kind = "system" },
		"archived":              func(agent *db.Agent, _ *db.AgentRuntime) { agent.ArchivedAt = pgtype.Timestamptz{Valid: true} },
		"disabled":              func(agent *db.Agent, _ *db.AgentRuntime) { agent.Status = "disabled" },
		"missing runtime":       func(agent *db.Agent, _ *db.AgentRuntime) { agent.RuntimeID = pgtype.UUID{} },
		"wrong runtime id": func(_ *db.Agent, runtime *db.AgentRuntime) {
			runtime.ID = companyOpsTestUUID("44444444-5555-4666-8777-888888888888")
		},
		"wrong runtime workspace": func(_ *db.Agent, runtime *db.AgentRuntime) { runtime.WorkspaceID = otherWorkspace },
		"offline runtime":         func(_ *db.Agent, runtime *db.AgentRuntime) { runtime.Status = "offline" },
		"runtime mode drift":      func(_ *db.Agent, runtime *db.AgentRuntime) { runtime.RuntimeMode = "cloud" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			agent := executableAgent()
			runtime := onlineRuntime()
			mutate(&agent, &runtime)
			lookup := &stubAgentLookup{agent: agent, runtime: runtime}
			adapter := &stubDirectoryAdapter{employees: serviceEmployees(serviceSummary(companyopsapi.BindingStateUniqueActiveCandidate, &agentID))}
			service := NewCompanyOpsDirectoryService(adapter, lookup)
			result, err := service.GetEmployees(context.Background(), testWorkspaceUUID, "", "", 50, 0)
			if err != nil {
				t.Fatal(err)
			}
			if result.Items[0].Availability != companyopsapi.AvailabilityMissingOrInvalid {
				t.Fatalf("availability = %s", result.Items[0].Availability)
			}
			if result.Items[0].LocalAgent != nil || result.Items[0].Binding.HiveCrewAgentID != nil {
				t.Fatal("invalid local row leaked executable identity")
			}
		})
	}
}

func TestDirectoryServiceUsesWorkspaceScopedQueries(t *testing.T) {
	agentID := "11111111-2222-4333-8444-555555555555"
	adapter := &stubDirectoryAdapter{employees: serviceEmployees(serviceSummary(companyopsapi.BindingStateUniqueActiveCandidate, &agentID))}
	lookup := &stubAgentLookup{agent: executableAgent(), runtime: onlineRuntime()}
	service := NewCompanyOpsDirectoryService(adapter, lookup)
	if _, err := service.GetEmployees(context.Background(), testWorkspaceUUID, "", "", 50, 0); err != nil {
		t.Fatal(err)
	}
	if !uuidEqual(lookup.agentParams.WorkspaceID, testWorkspaceUUID) ||
		!uuidEqual(lookup.runtimeParams.WorkspaceID, testWorkspaceUUID) ||
		!uuidEqual(lookup.runtimeParams.ID, testRuntimeUUID) {
		t.Fatal("local Agent or Runtime lookup escaped the requested workspace")
	}
	if adapter.employeesWS != "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee" {
		t.Fatalf("adapter workspace = %q", adapter.employeesWS)
	}
}

func TestDirectoryServiceDetailRedactsBindingsUnlessAvailable(t *testing.T) {
	agentID := "11111111-2222-4333-8444-555555555555"
	summary := serviceSummary(companyopsapi.BindingStateUniqueActiveCandidate, &agentID)
	for _, test := range []struct {
		name        string
		lookup      *stubAgentLookup
		wantBinding bool
	}{
		{"available", &stubAgentLookup{agent: executableAgent(), runtime: onlineRuntime()}, true},
		{"missing", &stubAgentLookup{agentErr: errors.New("missing")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := &stubDirectoryAdapter{employee: serviceDetail(summary)}
			service := NewCompanyOpsDirectoryService(adapter, test.lookup)
			result, err := service.GetEmployee(context.Background(), testWorkspaceUUID, "DE-ALICE")
			if err != nil {
				t.Fatal(err)
			}
			if (len(result.Bindings) == 1) != test.wantBinding {
				t.Fatalf("bindings = %d", len(result.Bindings))
			}
			if !test.wantBinding && (result.Employee.HiveCrewAgentID != "" || result.Employee.Binding.HiveCrewAgentID != nil) {
				t.Fatal("non-available detail leaked HiveCrew Agent identity")
			}
			if result.DossierEnrichment.Available == nil {
				t.Fatal("typed safe dossier was not preserved")
			}
		})
	}
}

func TestDirectoryServiceOrganizationRequiresOneExactGenerationAndRosterSet(t *testing.T) {
	agentID := "11111111-2222-4333-8444-555555555555"
	baseOrganization := serviceOrganization()
	baseEmployees := serviceEmployees(serviceSummary(companyopsapi.BindingStateUniqueActiveCandidate, &agentID))
	tests := map[string]func(*companyopsapi.AdapterOrganizationResponse, *companyopsapi.AdapterEmployeesResponse){
		"authority drift": func(_ *companyopsapi.AdapterOrganizationResponse, employees *companyopsapi.AdapterEmployeesResponse) {
			employees.Authority.SourceGeneratedAt = "2026-08-12T00:58:59.000Z"
		},
		"missing employee": func(_ *companyopsapi.AdapterOrganizationResponse, employees *companyopsapi.AdapterEmployeesResponse) {
			employees.Employees = nil
		},
		"wrong workforce": func(organization *companyopsapi.AdapterOrganizationResponse, _ *companyopsapi.AdapterEmployeesResponse) {
			organization.Departments[0].Positions[0].Appointments[0].WorkforceAgentID = "KT-OTHER"
		},
		"wrong position": func(_ *companyopsapi.AdapterOrganizationResponse, employees *companyopsapi.AdapterEmployeesResponse) {
			employees.Employees[0].PositionID = "POS-OTHER"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			organization := cloneOrganization(baseOrganization)
			employees := cloneEmployees(baseEmployees)
			mutate(organization, employees)
			adapter := &stubDirectoryAdapter{organization: organization, employees: employees}
			service := NewCompanyOpsDirectoryService(adapter, &stubAgentLookup{agent: executableAgent(), runtime: onlineRuntime()})
			_, err := service.GetOrganization(context.Background(), testWorkspaceUUID)
			if !errors.Is(err, companyopsapi.ErrAdapterMalformed) {
				t.Fatalf("error = %v, want ErrAdapterMalformed", err)
			}
		})
	}
}

func TestDirectoryServiceOrganizationHappyPath(t *testing.T) {
	agentID := "11111111-2222-4333-8444-555555555555"
	adapter := &stubDirectoryAdapter{
		organization: serviceOrganization(),
		employees:    serviceEmployees(serviceSummary(companyopsapi.BindingStateUniqueActiveCandidate, &agentID)),
	}
	service := NewCompanyOpsDirectoryService(adapter, &stubAgentLookup{agent: executableAgent(), runtime: onlineRuntime()})
	result, err := service.GetOrganization(context.Background(), testWorkspaceUUID)
	if err != nil {
		t.Fatal(err)
	}
	appointment := result.Departments[0].Positions[0].Appointments[0]
	if appointment.Availability != companyopsapi.AvailabilityAvailable {
		t.Fatalf("availability = %s", appointment.Availability)
	}
	if adapter.organizationWS != adapter.employeesWS || adapter.organizationWS == "" {
		t.Fatal("organization join did not use one exact workspace")
	}
}

func TestDirectoryServiceFiltersSearchesAndPaginatesFinalAvailability(t *testing.T) {
	employees := serviceEmployees(serviceSummary(companyopsapi.BindingStateNone, nil))
	second := serviceSummary(companyopsapi.BindingStateSourceGap, nil)
	second.EmployeeID = "DE-BOB"
	second.WorkforceAgentID = "NEW-BOB"
	second.DisplayName = "Bob"
	second.PositionID = "POS-002"
	second.PositionTitle = "Reviewer"
	employees.Employees = append(employees.Employees, second)
	service := NewCompanyOpsDirectoryService(
		&stubDirectoryAdapter{employees: employees},
		&stubAgentLookup{},
	)
	result, err := service.GetEmployees(context.Background(), testWorkspaceUUID, "bob", companyopsapi.AvailabilitySourceGap, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].EmployeeID != "DE-BOB" {
		t.Fatalf("unexpected filtered result: %#v", result)
	}
	result, err = service.GetEmployees(context.Background(), testWorkspaceUUID, "", "", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Items) != 1 || result.Items[0].EmployeeID != "DE-BOB" {
		t.Fatalf("unexpected paginated result: %#v", result)
	}
}

func TestDirectoryServiceDetailRedactsInactiveOnlyBindings(t *testing.T) {
	summary := serviceSummary(companyopsapi.BindingStateInactiveOnly, nil)
	detail := serviceDetail(summary)
	detail.Bindings = []companyopsapi.AdapterBindingDetail{{
		IdentityBindingID: "IB-ABCDEF0123456789ABCDEF02",
		WorkforceAgentID:  "KT-ALICE",
		HiveCrewAgentID:   "11111111-2222-4333-8444-555555555555",
		AgentRef:          "/api/agents/11111111-2222-4333-8444-555555555555",
		Active:            false,
		EffectiveFrom:     "2026-08-01T00:00:00.000Z",
		Authority: companyopsapi.AdapterBindingAuthority{
			SourceRef:     "hivecosm://identity-bindings/IB-ABCDEF0123456789ABCDEF02",
			Revision:      serviceDigest,
			ContentDigest: serviceDigest,
			CandidateOnly: true,
		},
	}}
	service := NewCompanyOpsDirectoryService(
		&stubDirectoryAdapter{employee: detail},
		&stubAgentLookup{},
	)
	result, err := service.GetEmployee(context.Background(), testWorkspaceUUID, "DE-ALICE")
	if err != nil {
		t.Fatal(err)
	}
	if result.Employee.Availability != companyopsapi.AvailabilityInactiveOnly {
		t.Fatalf("availability = %s", result.Employee.Availability)
	}
	if len(result.Bindings) != 0 {
		t.Fatalf("inactive_only detail leaked %d bindings", len(result.Bindings))
	}
	if result.Employee.HiveCrewAgentID != "" ||
		result.Employee.Binding.HiveCrewAgentID != nil ||
		result.Employee.LocalAgent != nil ||
		result.Employee.Binding.ExecutabilityVerified {
		t.Fatal("inactive_only detail leaked HiveCrew/local Agent identity")
	}
	serialized, err := json.Marshal(result.Employee)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"hivecrew_agent_id"`, `"local_agent"`, `"agent_ref"`} {
		if bytes.Contains(serialized, []byte(key)) {
			t.Fatalf("inactive_only detail serialized sensitive key %s", key)
		}
	}
}

func TestDirectoryServiceMapsNotFoundWithoutRawIdentity(t *testing.T) {
	service := NewCompanyOpsDirectoryService(
		&stubDirectoryAdapter{employeeErr: companyopsapi.ErrAdapterNotFound},
		&stubAgentLookup{},
	)
	_, err := service.GetEmployee(context.Background(), testWorkspaceUUID, "DE-MISSING")
	if !errors.Is(err, ErrCompanyOpsEmployeeNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func cloneOrganization(value *companyopsapi.AdapterOrganizationResponse) *companyopsapi.AdapterOrganizationResponse {
	copy := *value
	copy.Departments = append([]companyopsapi.AdapterOrganizationDepartment(nil), value.Departments...)
	copy.Departments[0].Positions = append([]companyopsapi.AdapterOrganizationPosition(nil), value.Departments[0].Positions...)
	copy.Departments[0].Positions[0].EmployeeIDs = append([]string(nil), value.Departments[0].Positions[0].EmployeeIDs...)
	copy.Departments[0].Positions[0].Appointments = append([]companyopsapi.AdapterOrganizationAppointment(nil), value.Departments[0].Positions[0].Appointments...)
	return &copy
}

func cloneEmployees(value *companyopsapi.AdapterEmployeesResponse) *companyopsapi.AdapterEmployeesResponse {
	copy := *value
	copy.Employees = append([]companyopsapi.AdapterEmployeeSummary(nil), value.Employees...)
	return &copy
}
