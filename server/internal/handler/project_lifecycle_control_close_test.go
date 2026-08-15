package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func doControlAction(t *testing.T, projectID, action string, body map[string]any) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects/"+projectID+"/lifecycle/actions/"+action, body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID)
	rctx.URLParams.Add("action", action)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.ProjectLifecycleAction(w, req)
	var resp map[string]any
	if w.Body.Len() > 0 {
		_ = json.NewDecoder(w.Body).Decode(&resp)
	}
	return w.Code, resp
}

func TestCloseSupersedeClosurePackage(t *testing.T) {
	p1 := seedProjectForPagination(t, "close test project")
	p2 := seedProjectForPagination(t, "supersede target project")

	// generate closure package for p1 (read-only)
	code, pkg := doControlAction(t, p1.ID, "generate_closure_package", nil)
	if code != http.StatusOK {
		t.Fatalf("generate_closure_package: %d %v", code, pkg)
	}
	if pkg["project_id"] == nil {
		t.Errorf("expected project_id in closure package, got %v", pkg)
	}

	// close p1 (no lead -> ACCOUNTABLE_LEAD_REQUIRED fail-closed)
	code, receipt := doControlAction(t, p1.ID, "close", map[string]any{"idempotency_key": "c1"})
	if code != http.StatusOK {
		t.Fatalf("close: %d %v", code, receipt)
	}
	if got := receipt["blockers"]; got == nil {
		t.Errorf("expected blockers (no lead), got %v", receipt)
	} else {
		found := false
		for _, b := range got.([]any) {
			if b == "ACCOUNTABLE_LEAD_REQUIRED" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected ACCOUNTABLE_LEAD_REQUIRED blocker, got %v", got)
		}
	}

	// supersede p2 -> target p1
	code, sreceipt := doControlAction(t, p2.ID, "supersede", map[string]any{"target_project_id": p1.ID, "idempotency_key": "s1"})
	if code != http.StatusOK {
		t.Fatalf("supersede: %d %v", code, sreceipt)
	}
	if sreceipt["after_status"] != "cancelled" {
		t.Errorf("expected after_status=cancelled, got %v", sreceipt["after_status"])
	}

	// supersede a fresh project without target -> SUPERSEDE_TARGET_REQUIRED blocker
	p3 := seedProjectForPagination(t, "supersede no-target project")
	code, breceipt := doControlAction(t, p3.ID, "supersede", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("supersede no-target: %d %v", code, breceipt)
	}
	if breceipt["blockers"] == nil {
		t.Errorf("expected SUPERSEDE_TARGET_REQUIRED blocker, got %v", breceipt)
	}

	// re-supersede the already-cancelled p2 -> idempotent replay (no duplicate)
	code, rreceipt := doControlAction(t, p2.ID, "supersede", map[string]any{"target_project_id": p1.ID, "idempotency_key": "s1"})
	if code != http.StatusOK {
		t.Fatalf("re-supersede: %d %v", code, rreceipt)
	}
	if rreceipt["replayed"] != true {
		t.Errorf("expected replayed=true on re-supersede, got %v", rreceipt)
	}
}
