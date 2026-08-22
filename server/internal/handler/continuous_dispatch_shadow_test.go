package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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
	calls  atomic.Int32
}

func (f *workConservingProjectionFixture) ProjectWorkConserving(_ context.Context, req service.WorkConservingProjectionRequest) (service.WorkConservingProjection, error) {
	f.calls.Add(1)
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
		// The provider round-trip is only meaningful when the same request's
		// top-level organization read was healthy.
		Sources: service.ContinuousDispatchShadowSources{
			Project: true, Runtime: true, Tasks: true,
			Organization: true, OrganizationSourceState: service.OrganizationSourceHealthy,
		},
		Items:  []service.ContinuousDispatchShadowItem{{IssueID: "issue-page-only"}},
		Total:  1,
		Limit:  1,
		Offset: 0,
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

// gracefulShadowStoreFixture is the handler-local store backing the real
// ContinuousDispatchShadowService in the graceful-read tests. It mirrors the
// minimal production read shape: one readable project, one in-progress issue,
// one bound agent/runtime pair, and no task or comment rows.
type gracefulShadowStoreFixture struct {
	project  db.Project
	issues   []db.ListIssuesRow
	agents   []db.Agent
	runtimes []db.AgentRuntime
}

func (f *gracefulShadowStoreFixture) GetProjectInWorkspace(context.Context, db.GetProjectInWorkspaceParams) (db.Project, error) {
	return f.project, nil
}

func (f *gracefulShadowStoreFixture) CountIssuesByProject(context.Context, pgtype.UUID) (int64, error) {
	return int64(len(f.issues)), nil
}

func (f *gracefulShadowStoreFixture) ListIssues(context.Context, db.ListIssuesParams) ([]db.ListIssuesRow, error) {
	return append([]db.ListIssuesRow(nil), f.issues...), nil
}

func (f *gracefulShadowStoreFixture) ListAllAgents(context.Context, pgtype.UUID) ([]db.Agent, error) {
	return append([]db.Agent(nil), f.agents...), nil
}

func (f *gracefulShadowStoreFixture) ListAgentRuntimes(context.Context, pgtype.UUID) ([]db.AgentRuntime, error) {
	return append([]db.AgentRuntime(nil), f.runtimes...), nil
}

func (f *gracefulShadowStoreFixture) ListWorkspaceAgentTaskSnapshot(context.Context, pgtype.UUID) ([]db.AgentTaskQueue, error) {
	return nil, nil
}

func (f *gracefulShadowStoreFixture) ListTasksByIssue(context.Context, pgtype.UUID) ([]db.AgentTaskQueue, error) {
	return nil, nil
}

func (f *gracefulShadowStoreFixture) ListCommentsForIssue(context.Context, db.ListCommentsForIssueParams) ([]db.Comment, error) {
	return nil, nil
}

type gracefulLeaseReader struct{}

func (gracefulLeaseReader) Read(context.Context, string) (*service.WriteLease, error) {
	return nil, service.ErrLeaseNotFound
}

type gracefulDirectoryFixture struct{}

func (gracefulDirectoryFixture) GetEmployees(_ context.Context, workspaceID pgtype.UUID, _, _ string, _, _ int) (*service.EmployeesResult, error) {
	return &service.EmployeesResult{
		WorkspaceID: uuid.UUID(workspaceID.Bytes).String(),
		Items: []companyopsapi.PublicEmployeeSummary{{
			EmployeeID: "DE-PRIMARY", DisplayName: "Primary", PositionTitle: "全栈工程师", PositionID: "implementation",
			Availability: companyopsapi.AvailabilityAvailable, HiveCrewAgentID: gracefulPrimaryAgentID,
			LocalAgent: &companyopsapi.PublicLocalAgent{ID: gracefulPrimaryAgentID, RuntimeStatus: "online"},
		}},
		Total: 1, Limit: 500,
	}, nil
}

const gracefulPrimaryAgentID = "00000000-0000-0000-0000-000000000401"

func newGracefulShadowFixture(t *testing.T) (store *gracefulShadowStoreFixture, projectID string) {
	t.Helper()
	parsedWorkspace, err := uuid.Parse(testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := pgtype.UUID{Bytes: parsedWorkspace, Valid: true}
	parsedProject, err := uuid.Parse("00000000-0000-0000-0000-000000000201")
	if err != nil {
		t.Fatal(err)
	}
	projectID = parsedProject.String()
	projectUUID := pgtype.UUID{Bytes: parsedProject, Valid: true}
	parsedAgent, err := uuid.Parse(gracefulPrimaryAgentID)
	if err != nil {
		t.Fatal(err)
	}
	agentUUID := pgtype.UUID{Bytes: parsedAgent, Valid: true}
	parsedRuntime, err := uuid.Parse("00000000-0000-0000-0000-000000000501")
	if err != nil {
		t.Fatal(err)
	}
	runtimeUUID := pgtype.UUID{Bytes: parsedRuntime, Valid: true}
	store = &gracefulShadowStoreFixture{
		project: db.Project{ID: projectUUID, WorkspaceID: workspaceID, Title: "Graceful read project"},
		issues: []db.ListIssuesRow{{
			ID:           pgtype.UUID{Bytes: parsedProject, Valid: true},
			WorkspaceID:  workspaceID,
			ProjectID:    projectUUID,
			Title:        "Keep demand readable",
			Status:       "in_progress",
			AssigneeType: pgtype.Text{String: "agent", Valid: true},
			AssigneeID:   agentUUID,
			Metadata:     []byte(`{"stage":"implementation","generation":"g-1","candidate_revision":"abc123","write_mutex_key":"repo:main"}`),
		}},
		agents: []db.Agent{{
			ID: agentUUID, WorkspaceID: workspaceID, Name: "Primary", RuntimeID: runtimeUUID,
			Status: "idle", MaxConcurrentTasks: 1, Model: pgtype.Text{String: "glm-5.2", Valid: true}, Kind: "user",
		}},
		runtimes: []db.AgentRuntime{{
			ID: runtimeUUID, WorkspaceID: workspaceID, Status: "online",
			DaemonID:   pgtype.Text{String: "base-a", Valid: true},
			LastSeenAt: pgtype.Timestamptz{Time: time.Now().UTC().Add(-time.Minute), Valid: true},
		}},
	}
	return store, projectID
}

func TestGetProjectNextActionsDegradesGracefullyWhenDirectoryUnavailable(t *testing.T) {
	store, projectID := newGracefulShadowFixture(t)
	shadow := service.NewContinuousDispatchShadowService(store, nil, nil, gracefulLeaseReader{})
	h := &Handler{ContinuousDispatchShadow: shadow}
	req := newRequest(http.MethodGet, "/api/projects/"+projectID+"/next-actions?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", projectID)
	w := httptest.NewRecorder()

	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200 instead of a whole-projection 503", w.Code, w.Body.String())
	}
	var body service.ContinuousDispatchShadowResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Sources.Organization {
		t.Fatalf("sources.organization = true, want false without a directory adapter")
	}
	if !body.Sources.Project || !body.Sources.Runtime || !body.Sources.Tasks {
		t.Fatalf("sources = %+v, want project/runtime/tasks to stay readable", body.Sources)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %+v, want the project issue to stay visible", body.Items)
	}
	action := body.Items[0].NextAction
	if action.State != continuousdispatch.StateBlocked || action.Selected != nil || len(action.Candidates) != 0 {
		t.Fatalf("next action = %+v, want blocked fail-closed dispatch without employees", action)
	}
}

func TestGetProjectNextActionsWorkConservingStaysSourceGapWhenDirectoryUnavailable(t *testing.T) {
	store, projectID := newGracefulShadowFixture(t)
	shadow := service.NewContinuousDispatchShadowService(store, nil, nil, gracefulLeaseReader{})
	goalPath := filepath.Join(t.TempDir(), "CHECKLIST.yaml")
	h := &Handler{
		ContinuousDispatchShadow: shadow,
		// The file provider itself must keep refusing to plan without the
		// employee directory, so the handler keeps the stable source_gap
		// projection instead of any suggestion.
		WorkConservingProjection: service.NewFileWorkConservingProjectionProvider(shadow, goalPath),
	}
	req := newRequest(http.MethodGet, "/api/projects/"+projectID+"/next-actions?workspace_id="+testWorkspaceID+"&projection=work_conserving", nil)
	req = withURLParam(req, "id", projectID)
	w := httptest.NewRecorder()

	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200 with a degraded read instead of 503", w.Code, w.Body.String())
	}
	var body service.ContinuousDispatchShadowResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Sources.Organization {
		t.Fatalf("sources.organization = true, want false without a directory adapter")
	}
	if body.WorkConserving == nil {
		t.Fatal("missing work_conserving projection")
	}
	projection := body.WorkConserving
	if projection.State != service.WorkConservingProjectionSourceGap || !projection.NoWrite ||
		len(projection.Suggestions) != 0 || len(projection.BlockedBacklog) != 0 {
		t.Fatalf("projection = %+v, want source_gap no-write projection without suggestions", *projection)
	}
}

func TestGetProjectNextActionsConfiguredDirectoryKeepsOrganizationSource(t *testing.T) {
	store, projectID := newGracefulShadowFixture(t)
	shadow := service.NewContinuousDispatchShadowService(store, gracefulDirectoryFixture{}, nil, gracefulLeaseReader{})
	h := &Handler{ContinuousDispatchShadow: shadow}
	req := newRequest(http.MethodGet, "/api/projects/"+projectID+"/next-actions?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", projectID)
	w := httptest.NewRecorder()

	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body service.ContinuousDispatchShadowResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Sources.Organization {
		t.Fatalf("sources.organization = false, want true with a configured directory")
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

// ─────────────────────────────────────────────────────────────────────────────
// Organization source state producer + provider gating (WO-P1-PRIME-PROJECT-AUTHORITY-SOURCE-UI-R6)
// ─────────────────────────────────────────────────────────────────────────────

// shadowResultWithOrganization builds a minimal shadow result whose only
// variable is the organization source state.
func shadowResultWithOrganization(organization bool, state service.OrganizationSourceState) *service.ContinuousDispatchShadowResult {
	return &service.ContinuousDispatchShadowResult{
		SchemaVersion: service.ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   testWorkspaceID,
		ProjectID:     "00000000-0000-0000-0000-000000000201",
		Sources: service.ContinuousDispatchShadowSources{
			Project: true, Runtime: true, Tasks: true,
			Organization: organization, OrganizationSourceState: state,
		},
		Items: []service.ContinuousDispatchShadowItem{}, Total: 0, Limit: 25, Offset: 0,
	}
}

// TestGetProjectNextActionsEmitsTopLevelOrganizationSourceState verifies the
// live API producer: the response envelope carries the sanitized
// sources.organization_source_state constant verbatim.
func TestGetProjectNextActionsEmitsTopLevelOrganizationSourceState(t *testing.T) {
	for _, state := range []service.OrganizationSourceState{
		service.OrganizationSourceBaseMissing,
		service.OrganizationSourceBaseInvalid,
		service.OrganizationSourceTokenUnavailable,
		service.OrganizationSourceTenantMissing,
		service.OrganizationSourceDirectoryConstructorError,
		service.OrganizationSourceDirectoryRequestError,
		service.OrganizationSourceEmptyAuthoritativeWorkforce,
		service.OrganizationSourceHealthy,
	} {
		t.Run(string(state), func(t *testing.T) {
			h := &Handler{ContinuousDispatchShadow: &shadowInspectorFixture{result: shadowResultWithOrganization(state == service.OrganizationSourceHealthy, state)}}
			req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID, nil)
			req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
			w := httptest.NewRecorder()

			h.GetProjectNextActions(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
			var body struct {
				Sources struct {
					Organization            bool   `json:"organization"`
					OrganizationSourceState string `json:"organization_source_state"`
				} `json:"sources"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Sources.OrganizationSourceState != string(state) {
				t.Fatalf("organization_source_state = %q, want %q", body.Sources.OrganizationSourceState, state)
			}
			if body.Sources.Organization != (state == service.OrganizationSourceHealthy) {
				t.Fatalf("sources.organization = %v for state %q", body.Sources.Organization, state)
			}
		})
	}
}

// TestGetProjectNextActionsWorkConservingSkipsProviderWhenOrganizationNotHealthy
// pins the fail-closed provider gate: a non-healthy top-level organization
// source never evaluates the work-conserving provider, and the response keeps
// the generic source-gap projection (empty suggestions/backlog, no-write).
func TestGetProjectNextActionsWorkConservingSkipsProviderWhenOrganizationNotHealthy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		org   bool
		state service.OrganizationSourceState
	}{
		{"base_missing", false, service.OrganizationSourceBaseMissing},
		{"base_invalid", false, service.OrganizationSourceBaseInvalid},
		{"token_unavailable", false, service.OrganizationSourceTokenUnavailable},
		{"tenant_missing", false, service.OrganizationSourceTenantMissing},
		{"directory_constructor_error", false, service.OrganizationSourceDirectoryConstructorError},
		{"directory_request_error", false, service.OrganizationSourceDirectoryRequestError},
		{"empty_authoritative_workforce", false, service.OrganizationSourceEmptyAuthoritativeWorkforce},
		{"state healthy but organization false", false, service.OrganizationSourceHealthy},
		{"organization true but state degraded", true, service.OrganizationSourceDirectoryRequestError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &workConservingProjectionFixture{result: validWorkConservingProjection()}
			h := &Handler{
				ContinuousDispatchShadow: &shadowInspectorFixture{result: shadowResultWithOrganization(tc.org, tc.state)},
				WorkConservingProjection: provider,
			}
			req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID+"&projection=work_conserving", nil)
			req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
			w := httptest.NewRecorder()

			h.GetProjectNextActions(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
			if got := provider.calls.Load(); got != 0 {
				t.Fatalf("provider called %d times, want 0 for a non-healthy top-level organization", got)
			}
			var body struct {
				WorkConserving *service.WorkConservingProjection `json:"work_conserving"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.WorkConserving == nil {
				t.Fatal("work_conserving projection missing")
			}
			p := body.WorkConserving
			if p.State != service.WorkConservingProjectionSourceGap || !p.NoWrite ||
				len(p.Suggestions) != 0 || len(p.BlockedBacklog) != 0 {
				t.Fatalf("projection = %+v, want generic source-gap no-write with empty suggestions/backlog", p)
			}
		})
	}
}

// TestGetProjectNextActionsWorkConservingCallsProviderWhenOrganizationHealthy
// proves the gate is not fail-everything: a top-level request-local healthy
// organization read still evaluates the provider exactly once.
func TestGetProjectNextActionsWorkConservingCallsProviderWhenOrganizationHealthy(t *testing.T) {
	provider := &workConservingProjectionFixture{result: validWorkConservingProjection()}
	h := &Handler{
		ContinuousDispatchShadow: &shadowInspectorFixture{result: shadowResultWithOrganization(true, service.OrganizationSourceHealthy)},
		WorkConservingProjection: provider,
	}
	req := newRequest(http.MethodGet, "/api/projects/00000000-0000-0000-0000-000000000201/next-actions?workspace_id="+testWorkspaceID+"&projection=work_conserving", nil)
	req = withURLParam(req, "id", "00000000-0000-0000-0000-000000000201")
	w := httptest.NewRecorder()

	h.GetProjectNextActions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("provider called %d times, want exactly 1", got)
	}
	var body struct {
		WorkConserving *service.WorkConservingProjection `json:"work_conserving"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.WorkConserving == nil || body.WorkConserving.State != service.WorkConservingProjectionReady {
		t.Fatalf("projection = %+v, want the provider's ready projection", body.WorkConserving)
	}
	if !body.WorkConserving.NoWrite {
		t.Fatal("ready projection must remain no-write")
	}
}
