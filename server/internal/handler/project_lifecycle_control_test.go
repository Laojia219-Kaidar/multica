package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// C16: pause preview=true must be read-only (no status change).
func TestProjectLifecycleAction_PausePreviewIsReadOnly(t *testing.T) {
	p := seedControlProject(t, "in_progress", "agent", testUserID)
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/pause_dispatch?workspace_id="+testWorkspaceID, map[string]any{"preview": true})
	req = withURLParams(req, "id", p.ID, "action", "pause_dispatch")
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// Re-read: status must still be in_progress.
	w2 := httptest.NewRecorder()
	req2 := newRequest("GET", "/api/projects/"+p.ID+"?workspace_id="+testWorkspaceID, nil)
	req2 = withURLParam(req2, "id", p.ID)
	testHandler.GetProject(w2, req2)
	var after ProjectResponse
	if err := json.NewDecoder(w2.Body).Decode(&after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after.Status != "in_progress" {
		t.Fatalf("preview mutated status to %q, want in_progress", after.Status)
	}
}

func TestProjectLifecycleAction_TerminalProjectionPreviewAndMissingKey(t *testing.T) {
	p := seedControlProject(t, "completed", "agent", testUserID)

	// Preview is available without a mutation key and fails closed because the
	// project has no live inconsistency to repair.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/repair_terminal_projection?workspace_id="+testWorkspaceID, map[string]any{"preview": true})
	req = withURLParams(req, "id", p.ID, "action", "repair_terminal_projection")
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("preview expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var previewEnvelope struct {
		Preview struct {
			Blockers []string `json:"blockers"`
		} `json:"preview"`
	}
	if err := json.NewDecoder(w.Body).Decode(&previewEnvelope); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if len(previewEnvelope.Preview.Blockers) == 0 || previewEnvelope.Preview.Blockers[0] != "TERMINAL_PROJECTION_NOT_INCONSISTENT" {
		t.Fatalf("preview blockers = %v, want terminal inconsistency guard", previewEnvelope.Preview.Blockers)
	}

	// A commit without an idempotency key is rejected before any lifecycle
	// write, because this action must always leave a durable receipt.
	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/repair_terminal_projection?workspace_id="+testWorkspaceID, map[string]any{})
	req = withURLParams(req, "id", p.ID, "action", "repair_terminal_projection")
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing key expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProjectLifecycleAction_CompletedWithOpenIssueRepairsAndReplays(t *testing.T) {
	p := seedControlProject(t, "completed", "agent", testUserID)
	ctx := t.Context()
	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, project_id, title, status, priority, creator_type, creator_id, number, position)
		VALUES ($1,$2,'stale open issue','todo','medium','member',$3,1,1) RETURNING id
	`, testWorkspaceID, p.ID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("insert open issue: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1`, issueID) })

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/repair_terminal_projection?workspace_id="+testWorkspaceID, map[string]any{"preview": true})
	req = withURLParams(req, "id", p.ID, "action", "repair_terminal_projection")
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "completed_with_nonterminal_or_active") {
		t.Fatalf("preview = %d %s, want completed projection finding", w.Code, w.Body.String())
	}

	const key = "terminal-projection-repair-replay"
	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/repair_terminal_projection?workspace_id="+testWorkspaceID, map[string]any{"idempotency_key": key})
	req = withURLParams(req, "id", p.ID, "action", "repair_terminal_projection")
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"after_status":"in_progress"`) {
		t.Fatalf("repair = %d %s, want in_progress receipt", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/repair_terminal_projection?workspace_id="+testWorkspaceID, map[string]any{"idempotency_key": key})
	req = withURLParams(req, "id", p.ID, "action", "repair_terminal_projection")
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatalf("replay = %d %s, want replayed receipt", w.Code, w.Body.String())
	}
}

// C15: a non-admin member gets 403.
func TestProjectLifecycleAction_NonAdminForbidden(t *testing.T) {
	ctx := t.Context()
	p := seedControlProject(t, "in_progress", "agent", testUserID)
	// Create a plain member.
	var memberUserID string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('ctrl-member','ctrl-member-`+time.Unix(time.Now().Unix(), 0).Format("20060102150405")+`@example.com') RETURNING id`).Scan(&memberUserID); err != nil {
		t.Fatalf("insert member user: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1,$2,'member')`, testWorkspaceID, memberUserID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, memberUserID)
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id=$1`, memberUserID)
	})
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+p.ID+"/lifecycle/actions/pause_dispatch?workspace_id="+testWorkspaceID, map[string]any{})
	req = withURLParams(req, "id", p.ID, "action", "pause_dispatch")
	req.Header.Set("X-User-ID", memberUserID)
	testHandler.ProjectLifecycleAction(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// Slice 4: closure-package endpoint returns a candidate package (200) and is
// owner/admin gated; close preview is read-only.
func TestProjectClosurePackageEndpoint(t *testing.T) {
	p := seedControlProject(t, "in_progress", "agent", testUserID)
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+p.ID+"/closure-package?workspace_id="+testWorkspaceID, map[string]any{})
	req = withURLParam(req, "id", p.ID)
	testHandler.ProjectClosurePackage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var pkg struct {
		ReviewRequired bool   `json:"review_required"`
		Digest         string `json:"digest"`
	}
	if err := json.NewDecoder(w.Body).Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !pkg.ReviewRequired {
		t.Fatalf("review_required = false, want true")
	}
	if pkg.Digest == "" {
		t.Fatalf("digest empty")
	}
}
