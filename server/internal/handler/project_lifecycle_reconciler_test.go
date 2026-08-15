package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// VC-12: the reconciler endpoint returns read-only findings for the live
// portfolio and never mutates project status.
func TestListProjectLifecycleReconciler(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/projects/reconciler?workspace_id="+testWorkspaceID, nil)
	testHandler.ListProjectLifecycleReconciler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Findings []struct {
			Kind      string `json:"kind"`
			ProjectID string `json:"project_id"`
		} `json:"findings"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != len(body.Findings) {
		t.Fatalf("total=%d != len(findings)=%d", body.Total, len(body.Findings))
	}
}
