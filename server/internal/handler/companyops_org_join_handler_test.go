package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompanyOpsWorkforceBaseRuntimeHandlerReturnsStrictJoin(t *testing.T) {
	handler := &Handler{CompanyOpsDirectory: newHandlerDirectoryService(&handlerAdapter{})}

	request := withDirectoryWorkspace(httptest.NewRequest(http.MethodGet, "/api/company-ops/workforce-base-runtime", nil))
	recorder := httptest.NewRecorder()
	handler.GetCompanyOpsWorkforceBaseRuntime(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	assertDirectorySecurityHeaders(t, recorder)

	var body struct {
		SchemaVersion string `json:"schema_version"`
		WorkspaceID   string `json:"workspace_id"`
		Items         []struct {
			EmployeeID       string `json:"employee_id"`
			WorkforceAgentID string `json:"workforce_agent_id"`
			HiveCrewAgentID  string `json:"hivecrew_agent_id"`
			RuntimeID        string `json:"runtime_id"`
			BaseMachineTitle string `json:"base_machine_title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SchemaVersion != "hivecrew.workforce-base-runtime.v1" ||
		body.WorkspaceID != "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee" {
		t.Fatalf("unexpected envelope: %#v", body)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(body.Items))
	}
	item := body.Items[0]
	if item.EmployeeID != "DE-ALICE" ||
		item.WorkforceAgentID != "KT-ALICE" ||
		item.HiveCrewAgentID != "11111111-2222-4333-8444-555555555555" ||
		item.RuntimeID != "22222222-3333-4444-8555-666666666666" ||
		item.BaseMachineTitle != "unknown" {
		t.Fatalf("unexpected join item: %#v", item)
	}
}

func TestCompanyOpsWorkforceBaseRuntimeHandlerRejectsQueryParams(t *testing.T) {
	handler := &Handler{CompanyOpsDirectory: newHandlerDirectoryService(&handlerAdapter{})}
	request := withDirectoryWorkspace(httptest.NewRequest(http.MethodGet, "/api/company-ops/workforce-base-runtime?expand=1", nil))
	recorder := httptest.NewRecorder()
	handler.GetCompanyOpsWorkforceBaseRuntime(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}
