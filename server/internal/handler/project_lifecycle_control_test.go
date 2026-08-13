package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func seedControlProject(t *testing.T, status, leadType, leadID string) ProjectResponse {
	t.Helper()
	body := map[string]any{"title": "control seed", "status": status}
	if leadType != "" {
		body["lead_type"] = leadType
	}
	if leadID != "" {
		body["lead_id"] = leadID
	}
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects?workspace_id="+testWorkspaceID, body)
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed CreateProject: %d %s", w.Code, w.Body.String())
	}
	var p ProjectResponse
	if err := json.NewDecoder(w.Body).Decode(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Cleanup(func() {
		req := newRequest("DELETE", "/api/projects/"+p.ID, nil)
		req = withURLParam(req, "id", p.ID)
		testHandler.DeleteProject(httptest.NewRecorder(), req)
	})
	return p
}

// C2 (handler): continue on a project with no lead fails closed with
// ACCOUNTABLE_LEAD_REQUIRED and zero writes.
func TestProjectLifecycleAction_ContinueMissingLead(t *testing.T) {
	p := seedControlProject(t, "in_progress", "", "")
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/continue?workspace_id="+testWorkspaceID, map[string]any{
		"idempotency_key": "k-missing-lead",
	})
	req = withURLParams(req, "id", p.ID, "action", "continue")
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 receipt, got %d: %s", w.Code, w.Body.String())
	}
	var receipt map[string]any
	if err := json.NewDecoder(w.Body).Decode(&receipt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	blockers, _ := receipt["blockers"].([]any)
	found := false
	for _, b := range blockers {
		if b == "ACCOUNTABLE_LEAD_REQUIRED" {
			found = true
		}
	}
	if !found {
		t.Fatalf("receipt blockers = %v, want ACCOUNTABLE_LEAD_REQUIRED", receipt["blockers"])
	}
}

// PauseDispatch flips status to paused (stop new dispatch only).
func TestProjectLifecycleAction_PauseDispatch(t *testing.T) {
	p := seedControlProject(t, "in_progress", "agent", testUserID)
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/pause_dispatch?workspace_id="+testWorkspaceID, map[string]any{
		"idempotency_key": "k-pause",
	})
	req = withURLParams(req, "id", p.ID, "action", "pause_dispatch")
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var receipt map[string]any
	if err := json.NewDecoder(w.Body).Decode(&receipt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if receipt["after_status"] != "paused" || receipt["applied"] != true {
		t.Fatalf("receipt = %v, want after_status=paused applied=true", receipt)
	}
}

// Unknown action is a 400.
func TestProjectLifecycleAction_UnknownAction(t *testing.T) {
	p := seedControlProject(t, "in_progress", "agent", testUserID)
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/bogus?workspace_id="+testWorkspaceID, map[string]any{})
	req = withURLParams(req, "id", p.ID, "action", "bogus")
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
