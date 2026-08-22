package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetWorkQuota_AllowsMachineActor(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	req := httptest.NewRequest("GET", "/api/work/quota", nil)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Actor-Source", "task_token")
	w := httptest.NewRecorder()
	testHandler.GetWorkQuota(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetWorkQuota status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req2 := httptest.NewRequest("GET", "/api/work/quota", nil)
	req2.Header.Set("X-Actor-Source", "task_token")
	w2 := httptest.NewRecorder()
	RequireHumanActor(next).ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("RequireHumanActor wrapper status = %d, want 403", w2.Code)
	}
}

func TestPostProviderQuotaObservationValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	body := `{"observations":[{"provider":"MiniMax","plan":"MiniMax API","window_kind":"5h","source":"live_vendor","remaining_tokens":100}]}`
	req := httptest.NewRequest("POST", "/api/company-ops/usage/quota-observation", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	w := httptest.NewRecorder()
	testHandler.PostProviderQuotaObservation(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
}

func TestPostProviderQuotaObservationRejectsSecrets(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	body := `{"observations":[{"provider":"MiniMax","plan":"MiniMax API","window_kind":"5h","source":"live_vendor","source_ref":"Bearer sk-live-super-secret-key-material-that-should-never-be-stored"}]}`
	req := httptest.NewRequest("POST", "/api/company-ops/usage/quota-observation", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	w := httptest.NewRecorder()
	testHandler.PostProviderQuotaObservation(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestGetProviderPlanUsage_StillRequiresHumanViaRouter(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := RequireHumanActor(next)
	req := httptest.NewRequest("GET", "/api/company-ops/usage", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("company-ops usage must stay human-only, got %d", w.Code)
	}
}
