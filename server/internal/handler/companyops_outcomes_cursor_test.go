package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

func TestGetCompanyOpsOutcomes_RejectsWhitespaceCursor(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes?cursor=%20%20token", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestGetCompanyOpsOutcomes_CursorAllowsBackwardCompatParams(t *testing.T) {
	// limit/offset remain valid alongside cursor in the allowlist; the handler
	// must not reject them as unknown keys. A nil service means we never reach
	// the query, so a 503 (service gap) proves parsing passed the key gate.
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes?limit=5&offset=10&cursor=eyJ2IjoxLCJjcmVhdGVkX2F0IjoiMjAyNi0wOC0xM1QxMjowMDowMFoiLCJjb21tYW5kX2lkIjoiMTExMTExMTEtMTExMS00MTExLTgxMTEtMTExMTExMTExMTExIn0", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}
