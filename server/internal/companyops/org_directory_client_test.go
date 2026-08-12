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

const (
	testTenantID   = "hivecosm-test-tenant"
	testWorkspace  = "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	testAgentID    = "11111111-2222-4333-8444-555555555555"
	testBindingID  = "IB-ABCDEF0123456789ABCDEF01"
	testDigest     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testObservedAt = "2026-08-12T00:59:00.000Z"
)

var testNow = time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)

func validAuthority() AdapterAuthorityRef {
	return AdapterAuthorityRef{
		SourceRef:         AdapterAuthoritySourceRef,
		SourceVersion:     AdapterAuthoritySourceVersion,
		SourceRevision:    testDigest,
		ContentDigest:     testDigest,
		ObservedAt:        testObservedAt,
		SourceGeneratedAt: testObservedAt,
		Freshness:         AdapterAuthorityFreshness,
		ReadModelOnly:     true,
	}
}

func validSummary(state string, agentID *string) AdapterEmployeeSummary {
	return AdapterEmployeeSummary{
		EmployeeID:            "DE-ALICE",
		WorkforceAgentID:      "KT-ALICE",
		DisplayName:           "Alice",
		EmployeeContractState: "existing_digital_employee_contract",
		DepartmentID:          "DEPT-001",
		DepartmentName:        "Engineering",
		PositionID:            "POS-001",
		PositionTitle:         "Developer",
		BindingState:          state,
		Binding: AdapterBindingProjection{
			State:                 state,
			CandidateOnly:         true,
			ExecutabilityVerified: false,
			HiveCrewAgentID:       agentID,
		},
	}
}

func validOrganization() AdapterOrganizationResponse {
	return AdapterOrganizationResponse{
		SchemaVersion: HiveCrewOrganizationSchema,
		OK:            true,
		TenantID:      testTenantID,
		WorkspaceID:   testWorkspace,
		Authority:     validAuthority(),
		Departments: []AdapterOrganizationDepartment{{
			DepartmentID:   "DEPT-001",
			DepartmentName: "Engineering",
			Mission:        "Build",
			EmployeeCount:  1,
			Positions: []AdapterOrganizationPosition{{
				PositionID:    "POS-001",
				PositionTitle: "Developer",
				EmployeeCount: 1,
				EmployeeIDs:   []string{"DE-ALICE"},
				Appointments: []AdapterOrganizationAppointment{{
					AppointmentID:    "APPOINTMENT-DE-ALICE-POS-001",
					EmployeeID:       "DE-ALICE",
					WorkforceAgentID: "KT-ALICE",
				}},
			}},
		}},
	}
}

func validEmployees() AdapterEmployeesResponse {
	agentID := testAgentID
	return AdapterEmployeesResponse{
		SchemaVersion: HiveCrewEmployeesSchema,
		OK:            true,
		TenantID:      testTenantID,
		WorkspaceID:   testWorkspace,
		Authority:     validAuthority(),
		Employees:     []AdapterEmployeeSummary{validSummary(BindingStateUniqueActiveCandidate, &agentID)},
	}
}

func validAvailableDossier() AdapterDossierEnrichment {
	return AdapterDossierEnrichment{
		State: "available",
		Available: &AdapterDossierAvailable{
			State:         "available",
			SourceVersion: "EmployeeOperatingViewV2",
			GeneratedAt:   testObservedAt,
			WorkContext: AdapterDossierWorkContext{
				WorkOrderCount:          1,
				AssignmentCount:         1,
				ConversationThreadCount: 1,
				SourceGap:               false,
			},
			ModelDriver: AdapterDossierModelDriver{
				AssignmentPresent:      true,
				ProposalCount:          1,
				ProposalWriteAvailable: false,
				ModelCallPerformed:     false,
				SecretValuesExposed:    false,
			},
			ExecutionBridge: AdapterDossierExecutionBridge{
				Source:         "HQ-06",
				State:          "hq06_goal_run_linked",
				Configured:     true,
				Reachable:      true,
				ProjectID:      "PRJ-HCW-V2",
				GoalRunID:      stringPointer("GR-001"),
				TaskIDs:        []string{"TASK-001"},
				WritePerformed: false,
			},
			Boundaries: AdapterDossierBoundaries{
				ReadModelOnly:                   true,
				BrowserIdentityTrusted:          false,
				ProductionMutationAllowed:       false,
				ProviderCallPerformed:           false,
				ParallelEmployeeRegistryCreated: false,
			},
		},
	}
}

func validDetail() AdapterEmployeeDetailResponse {
	agentID := testAgentID
	return AdapterEmployeeDetailResponse{
		SchemaVersion: HiveCrewEmployeeSchema,
		OK:            true,
		TenantID:      testTenantID,
		WorkspaceID:   testWorkspace,
		Authority:     validAuthority(),
		Employee:      validSummary(BindingStateUniqueActiveCandidate, &agentID),
		Bindings: []AdapterBindingDetail{{
			IdentityBindingID: testBindingID,
			WorkforceAgentID:  "KT-ALICE",
			HiveCrewAgentID:   testAgentID,
			AgentRef:          "/api/agents/" + testAgentID,
			Active:            true,
			EffectiveFrom:     "2026-08-01T00:00:00.000Z",
			Authority: AdapterBindingAuthority{
				SourceRef:     "hivecosm://identity-bindings/" + testBindingID,
				Revision:      testDigest,
				ContentDigest: testDigest,
				CandidateOnly: true,
			},
		}},
		DossierEnrichment: validAvailableDossier(),
	}
}

func stringPointer(value string) *string {
	return &value
}

func newDirectoryClient(t *testing.T, responder func(http.ResponseWriter, *http.Request)) *HiveCrewDirectoryClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(responder))
	t.Cleanup(server.Close)
	client, err := NewHiveCrewDirectoryClient(server.URL, server.Client(), testTenantID)
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return testNow }
	return client
}

func writePayload(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryClientAcceptsExactAdapterWire(t *testing.T) {
	client := newDirectoryClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("workspace_id"); got != testWorkspace {
			t.Fatalf("workspace_id = %q", got)
		}
		switch r.URL.Path {
		case "/api/company-ops/organization":
			writePayload(t, w, validOrganization())
		case "/api/company-ops/employees":
			writePayload(t, w, validEmployees())
		case "/api/company-ops/employees/DE-ALICE":
			writePayload(t, w, validDetail())
		default:
			http.NotFound(w, r)
		}
	})
	if _, err := client.GetOrganization(context.Background(), testWorkspace); err != nil {
		t.Fatalf("organization: %v", err)
	}
	if _, err := client.GetEmployees(context.Background(), testWorkspace); err != nil {
		t.Fatalf("employees: %v", err)
	}
	detail, err := client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE")
	if err != nil {
		t.Fatalf("employee: %v", err)
	}
	if detail.DossierEnrichment.Available == nil ||
		detail.DossierEnrichment.Available.ExecutionBridge.State != "hq06_goal_run_linked" {
		t.Fatal("full available dossier was not preserved")
	}
}

func TestDirectoryClientAcceptsExactSourceGapDossier(t *testing.T) {
	detail := validDetail()
	detail.Employee = validSummary(BindingStateSourceGap, nil)
	detail.Employee.WorkforceAgentID = "KT-087_INTL"
	detail.Bindings = []AdapterBindingDetail{}
	detail.DossierEnrichment = AdapterDossierEnrichment{
		State: "source_gap",
		SourceGap: &AdapterDossierSourceGap{
			State:         "source_gap",
			SourceVersion: nil,
			Reason:        "workforce_identity_not_bindable",
		},
	}
	client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writePayload(t, w, detail)
	})
	result, err := client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE")
	if err != nil {
		t.Fatal(err)
	}
	if result.DossierEnrichment.SourceGap == nil {
		t.Fatal("source_gap dossier was not preserved")
	}
}

func TestDirectoryClientRejectsMalformedEnvelope(t *testing.T) {
	tests := map[string]func(*AdapterEmployeesResponse){
		"wrong schema":       func(value *AdapterEmployeesResponse) { value.SchemaVersion = "wrong.v1" },
		"tenant mismatch":    func(value *AdapterEmployeesResponse) { value.TenantID = "other" },
		"workspace mismatch": func(value *AdapterEmployeesResponse) { value.WorkspaceID = "ffffffff-ffff-4fff-8fff-ffffffffffff" },
		"ok false":           func(value *AdapterEmployeesResponse) { value.OK = false },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validEmployees()
			mutate(&value)
			client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePayload(t, w, value)
			})
			_, err := client.GetEmployees(context.Background(), testWorkspace)
			if err == nil {
				t.Fatal("expected fail-closed error")
			}
		})
	}
}

func TestDirectoryClientRejectsAuthorityDrift(t *testing.T) {
	tests := map[string]func(*AdapterAuthorityRef){
		"wrong source":   func(value *AdapterAuthorityRef) { value.SourceRef = "/wrong" },
		"wrong version":  func(value *AdapterAuthorityRef) { value.SourceVersion = "Other" },
		"short revision": func(value *AdapterAuthorityRef) { value.SourceRevision = "sha256:abc" },
		"uppercase revision": func(value *AdapterAuthorityRef) {
			value.SourceRevision = "sha256:" + strings.Repeat("A", 64)
			value.ContentDigest = value.SourceRevision
		},
		"digest mismatch": func(value *AdapterAuthorityRef) { value.ContentDigest = "sha256:" + strings.Repeat("b", 64) },
		"offset time":     func(value *AdapterAuthorityRef) { value.ObservedAt = "2026-08-12T08:59:00+08:00" },
		"date only":       func(value *AdapterAuthorityRef) { value.ObservedAt = "2026-08-12" },
		"stale": func(value *AdapterAuthorityRef) {
			value.ObservedAt = "2026-08-12T00:54:59.999Z"
			value.SourceGeneratedAt = value.ObservedAt
		},
		"future": func(value *AdapterAuthorityRef) {
			value.ObservedAt = "2026-08-12T01:00:05.001Z"
			value.SourceGeneratedAt = value.ObservedAt
		},
		"source later":    func(value *AdapterAuthorityRef) { value.SourceGeneratedAt = "2026-08-12T00:59:01.000Z" },
		"wrong freshness": func(value *AdapterAuthorityRef) { value.Freshness = "stale" },
		"write capable":   func(value *AdapterAuthorityRef) { value.ReadModelOnly = false },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validEmployees()
			mutate(&value.Authority)
			client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePayload(t, w, value)
			})
			_, err := client.GetEmployees(context.Background(), testWorkspace)
			if !errors.Is(err, ErrAdapterMalformed) {
				t.Fatalf("error = %v, want ErrAdapterMalformed", err)
			}
		})
	}
}

func TestDirectoryClientRejectsBindingProjectionDrift(t *testing.T) {
	tests := map[string]func(*AdapterEmployeeSummary){
		"unknown state":             func(value *AdapterEmployeeSummary) { value.BindingState = "invented"; value.Binding.State = "invented" },
		"state mismatch":            func(value *AdapterEmployeeSummary) { value.Binding.State = BindingStateNone },
		"candidate false":           func(value *AdapterEmployeeSummary) { value.Binding.CandidateOnly = false },
		"adapter claims executable": func(value *AdapterEmployeeSummary) { value.Binding.ExecutabilityVerified = true },
		"unique missing uuid":       func(value *AdapterEmployeeSummary) { value.Binding.HiveCrewAgentID = nil },
		"nonunique exposes uuid": func(value *AdapterEmployeeSummary) {
			value.BindingState = BindingStateNone
			value.Binding.State = BindingStateNone
		},
		"legacy promoted": func(value *AdapterEmployeeSummary) { value.WorkforceAgentID = "KT-087_INTL" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validEmployees()
			mutate(&value.Employees[0])
			client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePayload(t, w, value)
			})
			_, err := client.GetEmployees(context.Background(), testWorkspace)
			if !errors.Is(err, ErrAdapterMalformed) {
				t.Fatalf("error = %v, want ErrAdapterMalformed", err)
			}
		})
	}
}

func TestDirectoryClientRejectsDossierDrift(t *testing.T) {
	tests := map[string]func(*AdapterDossierAvailable){
		"stale generated_at":   func(value *AdapterDossierAvailable) { value.GeneratedAt = "2026-08-12T00:54:59.999Z" },
		"model call performed": func(value *AdapterDossierAvailable) { value.ModelDriver.ModelCallPerformed = true },
		"secret exposed":       func(value *AdapterDossierAvailable) { value.ModelDriver.SecretValuesExposed = true },
		"provider call":        func(value *AdapterDossierAvailable) { value.Boundaries.ProviderCallPerformed = true },
		"write performed":      func(value *AdapterDossierAvailable) { value.ExecutionBridge.WritePerformed = true },
		"wrong project":        func(value *AdapterDossierAvailable) { value.ExecutionBridge.ProjectID = "OTHER" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validDetail()
			mutate(value.DossierEnrichment.Available)
			client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePayload(t, w, value)
			})
			_, err := client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE")
			if !errors.Is(err, ErrAdapterMalformed) {
				t.Fatalf("error = %v, want ErrAdapterMalformed", err)
			}
		})
	}
}

func TestDirectoryClientRejectsNestedDossierUnknownKey(t *testing.T) {
	payload, err := json.Marshal(validDetail())
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatal(err)
	}
	dossier := object["dossier_enrichment"].(map[string]any)
	workContext := dossier["work_context"].(map[string]any)
	workContext["secret"] = true
	client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writePayload(t, w, object)
	})
	_, err = client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE")
	if !errors.Is(err, ErrAdapterMalformed) {
		t.Fatalf("error = %v, want ErrAdapterMalformed", err)
	}
}

func TestDirectoryClientRejectsDossierWorkforcePromotionDrift(t *testing.T) {
	tests := map[string]func(*AdapterEmployeeDetailResponse){
		"bindable workforce with source gap dossier": func(value *AdapterEmployeeDetailResponse) {
			value.DossierEnrichment = AdapterDossierEnrichment{
				State: "source_gap",
				SourceGap: &AdapterDossierSourceGap{
					State:  "source_gap",
					Reason: "workforce_identity_not_bindable",
				},
			}
		},
		"legacy workforce with available dossier": func(value *AdapterEmployeeDetailResponse) {
			value.Employee.WorkforceAgentID = "KT-087_INTL"
			value.Employee.BindingState = BindingStateSourceGap
			value.Employee.Binding = AdapterBindingProjection{
				State:                 BindingStateSourceGap,
				CandidateOnly:         true,
				ExecutabilityVerified: false,
			}
			value.Bindings = []AdapterBindingDetail{}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validDetail()
			mutate(&value)
			client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePayload(t, w, value)
			})
			_, err := client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE")
			if !errors.Is(err, ErrAdapterMalformed) {
				t.Fatalf("error = %v, want ErrAdapterMalformed", err)
			}
		})
	}
}

func TestDirectoryClientRejectsBindingDetailDrift(t *testing.T) {
	tests := map[string]func(*AdapterEmployeeDetailResponse){
		"wrong employee":     func(value *AdapterEmployeeDetailResponse) { value.Employee.EmployeeID = "DE-BOB" },
		"invalid binding id": func(value *AdapterEmployeeDetailResponse) { value.Bindings[0].IdentityBindingID = "binding-1" },
		"wrong agent ref":    func(value *AdapterEmployeeDetailResponse) { value.Bindings[0].AgentRef = "/api/agents/other" },
		"inactive unique":    func(value *AdapterEmployeeDetailResponse) { value.Bindings[0].Active = false },
		"wrong binding digest": func(value *AdapterEmployeeDetailResponse) {
			value.Bindings[0].Authority.ContentDigest = "sha256:" + strings.Repeat("b", 64)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validDetail()
			mutate(&value)
			client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePayload(t, w, value)
			})
			_, err := client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE")
			if !errors.Is(err, ErrAdapterMalformed) {
				t.Fatalf("error = %v, want ErrAdapterMalformed", err)
			}
		})
	}
}

func TestDirectoryClientRejectsUnknownTrailingAndOversize(t *testing.T) {
	t.Run("unknown root key", func(t *testing.T) {
		value := map[string]any{}
		payload, _ := json.Marshal(validEmployees())
		_ = json.Unmarshal(payload, &value)
		value["unknown"] = true
		client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writePayload(t, w, value)
		})
		_, err := client.GetEmployees(context.Background(), testWorkspace)
		if !errors.Is(err, ErrAdapterMalformed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("trailing document", func(t *testing.T) {
		payload, _ := json.Marshal(validEmployees())
		client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(append(payload, []byte(` {}`)...))
		})
		_, err := client.GetEmployees(context.Background(), testWorkspace)
		if !errors.Is(err, ErrAdapterMalformed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", MaxAdapterBodyBytes+1)))
		})
		_, err := client.GetEmployees(context.Background(), testWorkspace)
		if !errors.Is(err, ErrAdapterMalformed) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestDirectoryClientRejectsConfigurationAndIdentityDrift(t *testing.T) {
	if _, err := NewHiveCrewDirectoryClient("http://localhost", http.DefaultClient, " "); err == nil {
		t.Fatal("blank tenant must fail")
	}
	if _, err := NewHiveCrewDirectoryClient("file:///tmp/source", http.DefaultClient, testTenantID); err == nil {
		t.Fatal("non-http source must fail")
	}
	client := newDirectoryClient(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("malformed workspace must fail before network")
	})
	if _, err := client.GetEmployees(context.Background(), "not-a-uuid"); !errors.Is(err, ErrAdapterMalformed) {
		t.Fatalf("error = %v", err)
	}
	if _, err := client.GetEmployee(context.Background(), testWorkspace, "EMP-1"); !errors.Is(err, ErrAdapterMalformed) {
		t.Fatalf("error = %v", err)
	}
}

func TestDirectoryClientAcceptsMultipleActiveConflictDetailForms(t *testing.T) {
	otherAgent := "33333333-4444-4555-8666-777777777777"
	makeBinding := func(id, agentID string) AdapterBindingDetail {
		return AdapterBindingDetail{
			IdentityBindingID: id,
			WorkforceAgentID:  "KT-ALICE",
			HiveCrewAgentID:   agentID,
			AgentRef:          "/api/agents/" + agentID,
			Active:            true,
			EffectiveFrom:     "2026-08-01T00:00:00.000Z",
			Authority: AdapterBindingAuthority{
				SourceRef:     "hivecosm://identity-bindings/" + id,
				Revision:      testDigest,
				ContentDigest: testDigest,
				CandidateOnly: true,
			},
		}
	}
	tests := map[string]func(*AdapterEmployeeDetailResponse){
		"multiple active bindings": func(value *AdapterEmployeeDetailResponse) {
			value.Bindings = append(value.Bindings, makeBinding("IB-ABCDEF0123456789ABCDEF02", otherAgent))
		},
		"single related active binding of cross-workforce conflict": func(value *AdapterEmployeeDetailResponse) {
			value.Bindings = value.Bindings[:1]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validDetail()
			value.Employee = validSummary(BindingStateMultiConflict, nil)
			value.Employee.Binding.State = BindingStateMultiConflict
			mutate(&value)
			client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePayload(t, w, value)
			})
			if _, err := client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE"); err != nil {
				t.Fatalf("conflict form must be accepted: %v", err)
			}
		})
	}
}

func TestDirectoryClientRejectsNilOrEmptyOrganization(t *testing.T) {
	tests := map[string]func(*AdapterOrganizationResponse){
		"empty departments": func(value *AdapterOrganizationResponse) {
			value.Departments = []AdapterOrganizationDepartment{}
		},
		"nil positions": func(value *AdapterOrganizationResponse) {
			value.Departments[0].Positions = nil
		},
		"nil employee_ids": func(value *AdapterOrganizationResponse) {
			value.Departments[0].Positions[0].EmployeeIDs = nil
		},
		"nil appointments": func(value *AdapterOrganizationResponse) {
			value.Departments[0].Positions[0].Appointments = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validOrganization()
			mutate(&value)
			client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePayload(t, w, value)
			})
			if _, err := client.GetOrganization(context.Background(), testWorkspace); !errors.Is(err, ErrAdapterMalformed) {
				t.Fatalf("error = %v, want ErrAdapterMalformed", err)
			}
		})
	}
}

func TestDirectoryClientRejectsEmptyEmployees(t *testing.T) {
	value := validEmployees()
	value.Employees = []AdapterEmployeeSummary{}
	client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writePayload(t, w, value)
	})
	if _, err := client.GetEmployees(context.Background(), testWorkspace); !errors.Is(err, ErrAdapterMalformed) {
		t.Fatalf("error = %v, want ErrAdapterMalformed", err)
	}
}

func TestDirectoryClientRejectsBindingOutsideEffectiveWindow(t *testing.T) {
	futureFrom := "2026-08-13T00:00:00.000Z"
	expiredTo := "2026-08-10T00:00:00.000Z"
	boundaryTo := testObservedAt
	tests := map[string]func(*AdapterEmployeeDetailResponse){
		"future effective_from fails closed": func(value *AdapterEmployeeDetailResponse) {
			value.Bindings[0].EffectiveFrom = futureFrom
		},
		"expired effective_to fails closed": func(value *AdapterEmployeeDetailResponse) {
			value.Bindings[0].EffectiveTo = &expiredTo
		},
		"effective_to at observed_at fails closed": func(value *AdapterEmployeeDetailResponse) {
			value.Bindings[0].EffectiveTo = &boundaryTo
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := validDetail()
			mutate(&value)
			client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writePayload(t, w, value)
			})
			_, err := client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE")
			if !errors.Is(err, ErrAdapterMalformed) {
				t.Fatalf("error = %v, want ErrAdapterMalformed", err)
			}
		})
	}
}

func TestDirectoryClientAcceptsBindingWindowOpenAtObservedAt(t *testing.T) {
	value := validDetail()
	value.Bindings[0].EffectiveFrom = testObservedAt
	client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writePayload(t, w, value)
	})
	if _, err := client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE"); err != nil {
		t.Fatalf("effective_from at observed_at must be open: %v", err)
	}
}

func TestDirectoryClientMapsAdapterFailures(t *testing.T) {
	client := newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "private upstream error", http.StatusServiceUnavailable)
	})
	if _, err := client.GetOrganization(context.Background(), testWorkspace); !errors.Is(err, ErrAdapterSourceGap) {
		t.Fatalf("error = %v", err)
	}

	client = newDirectoryClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := client.GetEmployee(context.Background(), testWorkspace, "DE-ALICE"); !errors.Is(err, ErrAdapterNotFound) {
		t.Fatalf("error = %v", err)
	}
}
