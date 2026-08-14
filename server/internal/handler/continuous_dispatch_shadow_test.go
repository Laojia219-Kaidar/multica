package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/service"
)

type shadowInspectorFixture struct {
	result *service.ContinuousDispatchShadowResult
	err    error
	limit  int
	offset int
}

func (f *shadowInspectorFixture) InspectProject(_ context.Context, _, _ pgtype.UUID, limit, offset int) (*service.ContinuousDispatchShadowResult, error) {
	f.limit, f.offset = limit, offset
	return f.result, f.err
}

func TestGetProjectNextActionsReturnsStrictReadOnlyEnvelope(t *testing.T) {
	inspector := &shadowInspectorFixture{result: &service.ContinuousDispatchShadowResult{
		SchemaVersion: service.ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   testWorkspaceID,
		ProjectID:     "00000000-0000-0000-0000-000000000201",
		Items:         []service.ContinuousDispatchShadowItem{},
		Total:         0,
		Limit:         25,
		Offset:        5,
	}}
	h := &Handler{ContinuousDispatchShadow: inspector}
	req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID+"&limit=25&offset=5", nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
	w := httptest.NewRecorder()

	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if inspector.limit != 25 || inspector.offset != 5 {
		t.Fatalf("pagination = %d/%d", inspector.limit, inspector.offset)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %v", w.Header())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["schema_version"] != service.ContinuousDispatchShadowSchemaV1 {
		t.Fatalf("body = %v", body)
	}
}

func TestGetProjectNextActionsReturnsRealisticReadOnlyNextAction(t *testing.T) {
	inspector := &shadowInspectorFixture{result: &service.ContinuousDispatchShadowResult{
		SchemaVersion: service.ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   testWorkspaceID,
		ProjectID:     "00000000-0000-0000-0000-000000000201",
		ProjectTitle:  "Bounded adapter project",
		Items: []service.ContinuousDispatchShadowItem{{
			IssueID:    "00000000-0000-0000-0000-000000000301",
			IssueTitle: "Implement read-only preview",
			Status:     "in_progress",
			NextAction: continuousdispatch.NextAction{
				State: continuousdispatch.StateFallback,
				Selected: &continuousdispatch.CandidateDecision{
					EmployeeID: "DE-REPAIR",
					AgentID:    "00000000-0000-0000-0000-000000000401",
					Eligible:   true,
				},
			},
		}},
		Total: 1, Limit: 50, Offset: 0,
	}}
	h := &Handler{ContinuousDispatchShadow: inspector}
	req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
	w := httptest.NewRecorder()

	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body service.ContinuousDispatchShadowResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode read-only envelope: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].NextAction.State != continuousdispatch.StateFallback ||
		body.Items[0].NextAction.Selected == nil || body.Items[0].NextAction.Selected.EmployeeID != "DE-REPAIR" {
		t.Fatalf("next action = %+v, want realistic read-only fallback", body.Items[0].NextAction)
	}
}

func TestGetProjectNextActionsRejectsUnknownOrNonCanonicalPagination(t *testing.T) {
	for _, rawQuery := range []string{"limit=01", "limit=201", "offset=-1", "unexpected=x", "limit=1&limit=2"} {
		t.Run(rawQuery, func(t *testing.T) {
			h := &Handler{ContinuousDispatchShadow: &shadowInspectorFixture{}}
			req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID+"&"+rawQuery, nil)
			req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
			w := httptest.NewRecorder()
			h.GetProjectNextActions(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestGetProjectNextActionsMapsSourceGapWithoutRawError(t *testing.T) {
	inspector := &shadowInspectorFixture{err: errors.Join(service.ErrContinuousDispatchSourceGap, errors.New("secret upstream detail"))}
	h := &Handler{ContinuousDispatchShadow: inspector}
	req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
	w := httptest.NewRecorder()
	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "secret upstream detail") {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestGetProjectNextActionsRejectsMalformedProjectID(t *testing.T) {
	h := &Handler{ContinuousDispatchShadow: &shadowInspectorFixture{}}
	req := newRequest(http.MethodGet, "/api/projects/not-a-uuid/next-actions?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", "not-a-uuid")
	w := httptest.NewRecorder()
	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}
