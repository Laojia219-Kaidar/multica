package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

type workConservingProjectionFixture struct {
	result service.WorkConservingProjection
	err    error
	req    service.WorkConservingProjectionRequest
}

func (f *workConservingProjectionFixture) ProjectWorkConserving(_ context.Context, req service.WorkConservingProjectionRequest) (service.WorkConservingProjection, error) {
	f.req = req
	return f.result, f.err
}

func (f *shadowInspectorFixture) InspectProject(_ context.Context, _, _ pgtype.UUID, limit, offset int) (*service.ContinuousDispatchShadowResult, error) {
	f.limit, f.offset = limit, offset
	return f.result, f.err
}

func validWorkConservingAuthoritySnapshot() service.WorkConservingAuthoritySnapshot {
	now := time.Now().UTC().Truncate(time.Second)
	return service.WorkConservingAuthoritySnapshot{
		WorkspaceID: testWorkspaceID,
		ProjectID:   "00000000-0000-0000-0000-000000000201",
		SourceRef:   "hivecosm://company-ops/goal/goal-global-1",
		Revision:    "sha256:" + strings.Repeat("a", 64),
		ObservedAt:  now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt:   now.Add(14 * time.Minute).Format(time.RFC3339),
	}
}

func validWorkConservingProjection() service.WorkConservingProjection {
	return service.WorkConservingProjection{
		SchemaVersion: service.WorkConservingProjectionSchemaV1,
		State:         service.WorkConservingProjectionReady,
		GoalID:        "goal-global-1",
		Authority:     validWorkConservingAuthoritySnapshot(),
		Suggestions: []continuousdispatch.WorkConservingSuggestion{{
			IssueID: "issue-global-1", GoalID: "goal-global-1", EmployeeID: "DE-1", AgentID: "agent-1", RuntimeID: "runtime-1",
			Score: 42, Receiver: "dispatch-coordinator", WakeCondition: "fresh evidence",
		}},
		BlockedBacklog: []continuousdispatch.WorkConservingBlockedIssue{{
			IssueID: "issue-blocked-1", GoalID: "goal-global-1", Receiver: "authority-operator", WakeCondition: "authority available",
			Reasons: []continuousdispatch.Reason{continuousdispatch.ReasonIssueAuthorityMissing},
		}},
		Mismatch: continuousdispatch.WorkConservingMismatch{OpenIssues: 2, PlannedIssues: 1, BlockedBacklog: 1},
		Total:    2, Limit: 50, Offset: 0,
	}
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
	if _, present := body["work_conserving"]; present {
		t.Fatal("projection field must be absent when projection query is omitted")
	}
}

func TestGetProjectNextActionsWorkConservingRejectsNonCanonicalProjectionAndRouteSelectors(t *testing.T) {
	for _, rawQuery := range []string{
		"projection=other",
		"projection=work_conserving&projection=work_conserving",
		"projection=work_conserving&goal_id=goal-1",
		"projection=work_conserving&employee=DE-1",
		"projection=work_conserving&agent=agent-1",
		"projection=work_conserving&runtime=runtime-1",
		"projection=work_conserving&model=model-1",
		"projection=work_conserving&account=account-1",
		"projection=work_conserving&stage=review",
		"projection=work_conserving&revision=rev-1",
		"projection=work_conserving&generation=gen-1",
	} {
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

func TestGetProjectNextActionsWorkConservingMissingProviderIsSourceGapAndNoWrite(t *testing.T) {
	inspector := &shadowInspectorFixture{result: &service.ContinuousDispatchShadowResult{
		SchemaVersion: service.ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   testWorkspaceID,
		ProjectID:     "00000000-0000-0000-0000-000000000201",
		Items:         []service.ContinuousDispatchShadowItem{{IssueID: "issue-page-only"}},
		Total:         1,
		Limit:         1,
		Offset:        0,
	}}
	h := &Handler{ContinuousDispatchShadow: inspector}
	req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID+"&limit=1&projection=work_conserving", nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
	w := httptest.NewRecorder()
	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body service.ContinuousDispatchShadowResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.WorkConserving == nil {
		t.Fatal("missing work_conserving projection")
	}
	p := body.WorkConserving
	if p.State != service.WorkConservingProjectionSourceGap || !p.NoWrite || len(p.Suggestions) != 0 || len(p.BlockedBacklog) != 0 {
		t.Fatalf("projection = %+v, want source_gap blocked no-write empty plan", *p)
	}
	if p.Total != 0 || p.Limit != 1 || p.Offset != 0 {
		t.Fatalf("projection pagination = %+v, want empty source-gap metadata", *p)
	}
}

func TestGetProjectNextActionsWorkConservingProviderRoundTripsGlobalTotal(t *testing.T) {
	provider := &workConservingProjectionFixture{result: service.WorkConservingProjection{
		SchemaVersion: service.WorkConservingProjectionSchemaV1,
		State:         service.WorkConservingProjectionReady,
		GoalID:        "goal-global-1",
		Authority:     validWorkConservingAuthoritySnapshot(),
		Suggestions: []continuousdispatch.WorkConservingSuggestion{{
			IssueID: "issue-global-1", GoalID: "goal-global-1", EmployeeID: "DE-1", AgentID: "agent-1", RuntimeID: "runtime-1",
			Score: 42, Receiver: "dispatch-coordinator", WakeCondition: "fresh evidence",
		}},
		BlockedBacklog: []continuousdispatch.WorkConservingBlockedIssue{{
			IssueID: "issue-blocked-1", GoalID: "goal-global-1", Receiver: "authority-operator", WakeCondition: "authority available",
			Reasons: []continuousdispatch.Reason{continuousdispatch.ReasonIssueAuthorityMissing},
		}},
		Mismatch: continuousdispatch.WorkConservingMismatch{OpenIssues: 2, PlannedIssues: 1, BlockedBacklog: 1},
		Total:    2, Limit: 1, Offset: 0,
	}}
	inspector := &shadowInspectorFixture{result: &service.ContinuousDispatchShadowResult{
		SchemaVersion: service.ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   testWorkspaceID,
		ProjectID:     "00000000-0000-0000-0000-000000000201",
		Items:         []service.ContinuousDispatchShadowItem{{IssueID: "issue-page-only"}},
		Total:         1,
		Limit:         1,
		Offset:        0,
	}}
	h := &Handler{ContinuousDispatchShadow: inspector, WorkConservingProjection: provider}
	req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID+"&limit=1&projection=work_conserving", nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
	w := httptest.NewRecorder()
	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body service.ContinuousDispatchShadowResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.WorkConserving == nil {
		t.Fatal("missing work_conserving projection")
	}
	p := body.WorkConserving
	if p.State != service.WorkConservingProjectionReady || p.GoalID != "goal-global-1" || p.Total != 2 || len(p.Suggestions) != 1 || len(p.BlockedBacklog) != 1 || !p.NoWrite {
		t.Fatalf("projection = %+v, want provider plan with enforced no-write", *p)
	}
	if provider.req.Limit != 1 || provider.req.Offset != 0 || !provider.req.WorkspaceID.Valid || !provider.req.ProjectID.Valid {
		t.Fatalf("provider request = %+v", provider.req)
	}
}

func TestGetProjectNextActionsWorkConservingAuthorityAndPlanContractFailsClosed(t *testing.T) {
	base := validWorkConservingProjection()
	mutations := map[string]func(*service.WorkConservingProjection){
		"total_99_for_two_entries": func(p *service.WorkConservingProjection) { p.Total = 99 },
		"open_issues_mismatch":     func(p *service.WorkConservingProjection) { p.Mismatch.OpenIssues = 99 },
		"planned_mismatch":         func(p *service.WorkConservingProjection) { p.Mismatch.PlannedIssues = 99 },
		"blocked_mismatch":         func(p *service.WorkConservingProjection) { p.Mismatch.BlockedBacklog = 99 },
		"cross_workspace": func(p *service.WorkConservingProjection) {
			p.Authority.WorkspaceID = "00000000-0000-0000-0000-000000000999"
		},
		"cross_project": func(p *service.WorkConservingProjection) {
			p.Authority.ProjectID = "00000000-0000-0000-0000-000000000999"
		},
		"empty_source_ref":    func(p *service.WorkConservingProjection) { p.Authority.SourceRef = "" },
		"empty_revision":      func(p *service.WorkConservingProjection) { p.Authority.Revision = "" },
		"malformed_revision":  func(p *service.WorkConservingProjection) { p.Authority.Revision = "sha256:" + strings.Repeat("A", 64) },
		"invalid_observed_at": func(p *service.WorkConservingProjection) { p.Authority.ObservedAt = "not-a-time" },
		"invalid_expires_at":  func(p *service.WorkConservingProjection) { p.Authority.ExpiresAt = "not-a-time" },
		"expired": func(p *service.WorkConservingProjection) {
			now := time.Now().UTC().Truncate(time.Second)
			p.Authority.ObservedAt = now.Add(-16 * time.Minute).Format(time.RFC3339)
			p.Authority.ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339)
		},
		"future_snapshot": func(p *service.WorkConservingProjection) {
			now := time.Now().UTC().Truncate(time.Second)
			p.Authority.ObservedAt = now.Add(time.Minute).Format(time.RFC3339)
			p.Authority.ExpiresAt = now.Add(16 * time.Minute).Format(time.RFC3339)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			provider := &workConservingProjectionFixture{result: candidate}
			h := &Handler{ContinuousDispatchShadow: &shadowInspectorFixture{result: &service.ContinuousDispatchShadowResult{
				SchemaVersion: service.ContinuousDispatchShadowSchemaV1,
				WorkspaceID:   testWorkspaceID, ProjectID: "00000000-0000-0000-0000-000000000201",
				Items: []service.ContinuousDispatchShadowItem{{IssueID: "issue-page-only"}}, Total: 1, Limit: 50, Offset: 0,
			}}, WorkConservingProjection: provider}
			req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID+"&projection=work_conserving", nil)
			req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
			w := httptest.NewRecorder()
			h.GetProjectNextActions(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
			var body service.ContinuousDispatchShadowResult
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			p := body.WorkConserving
			if p == nil || p.State != service.WorkConservingProjectionSourceGap || !p.Blocked || !p.NoWrite || len(p.Suggestions) != 0 || len(p.BlockedBacklog) != 0 {
				t.Fatalf("projection = %+v, want source_gap blocked no-write empty plan", p)
			}
		})
	}
}

func TestGetProjectNextActionsWorkConservingInvalidProviderFailsClosed(t *testing.T) {
	provider := &workConservingProjectionFixture{result: service.WorkConservingProjection{
		State:  service.WorkConservingProjectionReady,
		GoalID: "goal-without-schema",
		Total:  4, Limit: 50, Offset: 0,
	}}
	h := &Handler{ContinuousDispatchShadow: &shadowInspectorFixture{result: &service.ContinuousDispatchShadowResult{
		SchemaVersion: service.ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   testWorkspaceID,
		ProjectID:     "00000000-0000-0000-0000-000000000201",
		Items:         []service.ContinuousDispatchShadowItem{}, Total: 4, Limit: 50, Offset: 0,
	}}, WorkConservingProjection: provider}
	req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID+"&projection=work_conserving", nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
	w := httptest.NewRecorder()
	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body service.ContinuousDispatchShadowResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.WorkConserving == nil || body.WorkConserving.State != service.WorkConservingProjectionSourceGap || !body.WorkConserving.NoWrite || len(body.WorkConserving.Suggestions) != 0 {
		t.Fatalf("projection = %+v, want fail-closed source gap", body.WorkConserving)
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
