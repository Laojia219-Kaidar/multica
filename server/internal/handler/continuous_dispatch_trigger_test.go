package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/service"
)

type dispatchTriggerHandlerFixture struct {
	result      service.ContinuousDispatchTriggerResult
	err         error
	workspaceID pgtype.UUID
	projectID   pgtype.UUID
	issueID     pgtype.UUID
	actorUserID pgtype.UUID
	handoffNote string
	calls       int
}

func (f *dispatchTriggerHandlerFixture) DispatchIssue(
	_ context.Context,
	workspaceID, projectID, issueID, actorUserID pgtype.UUID,
	handoffNote string,
) (service.ContinuousDispatchTriggerResult, error) {
	f.calls++
	f.workspaceID, f.projectID, f.issueID, f.actorUserID = workspaceID, projectID, issueID, actorUserID
	f.handoffNote = handoffNote
	return f.result, f.err
}

func withContinuousDispatchParams(req *http.Request, projectID, issueID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID)
	rctx.URLParams.Add("issueId", issueID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func dispatchTriggerResultFixture() service.ContinuousDispatchTriggerResult {
	return service.ContinuousDispatchTriggerResult{
		Action: continuousdispatch.NextAction{
			State: continuousdispatch.StateReady,
			Selected: &continuousdispatch.CandidateDecision{
				EmployeeID: "EMP-HANDLER", AgentID: "00000000-0000-0000-0000-000000000211",
				RuntimeID: "00000000-0000-0000-0000-000000000212", Model: "glm-5.2", AccountRef: "glm-handler",
			},
		},
		Receipt: service.ContinuousDispatchReceipt{
			Identity: continuousdispatch.DispatchIdentity{
				WorkspaceID: testWorkspaceID, IssueID: "00000000-0000-0000-0000-000000000202",
				Stage: "implementation", CandidateRevision: "candidate-handler", Generation: "generation-handler-1",
			},
			TaskID:       pgtype.UUID{Bytes: [16]byte{15: 213}, Valid: true},
			EmployeeRef:  "hivecosm://employees/EMP-HANDLER",
			LocalAgentID: pgtype.UUID{Bytes: [16]byte{15: 211}, Valid: true},
			RuntimeID:    pgtype.UUID{Bytes: [16]byte{15: 212}, Valid: true},
			Model:        "glm-5.2", AccountRef: "glm-handler",
		},
	}
}

func TestDispatchProjectNextActionAcceptsOnlyOwnerControlledInputs(t *testing.T) {
	projectID := "00000000-0000-0000-0000-000000000201"
	issueID := "00000000-0000-0000-0000-000000000202"
	fixture := &dispatchTriggerHandlerFixture{result: dispatchTriggerResultFixture()}
	h := *testHandler
	h.ContinuousDispatchTrigger = fixture
	req := newRequest(http.MethodPost,
		"/api/projects/"+projectID+"/next-actions/"+issueID+"/dispatch?workspace_id="+testWorkspaceID,
		map[string]any{"handoff_note": "Owner-controlled exact dispatch"},
	)
	req = withContinuousDispatchParams(req, projectID, issueID)
	w := httptest.NewRecorder()
	h.DispatchProjectNextAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if fixture.calls != 1 || fixture.handoffNote != "Owner-controlled exact dispatch" ||
		uuidToString(fixture.workspaceID) != testWorkspaceID ||
		uuidToString(fixture.projectID) != projectID || uuidToString(fixture.issueID) != issueID ||
		uuidToString(fixture.actorUserID) != testUserID {
		t.Fatalf("trigger inputs are not exact: %+v", fixture)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["schema_version"] != continuousDispatchCommandSchema || body["state"] != "ready" || body["task_id"] == "" {
		t.Fatalf("response = %#v", body)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %v", w.Header())
	}
}

func TestDispatchProjectNextActionRejectsUnknownRouteFields(t *testing.T) {
	projectID := "00000000-0000-0000-0000-000000000201"
	issueID := "00000000-0000-0000-0000-000000000202"
	fixture := &dispatchTriggerHandlerFixture{}
	h := *testHandler
	h.ContinuousDispatchTrigger = fixture
	req := newRequest(http.MethodPost,
		"/api/projects/"+projectID+"/next-actions/"+issueID+"/dispatch?workspace_id="+testWorkspaceID,
		map[string]any{"handoff_note": "x", "agent_id": "attacker-selected"},
	)
	req = withContinuousDispatchParams(req, projectID, issueID)
	w := httptest.NewRecorder()
	h.DispatchProjectNextAction(w, req)
	if w.Code != http.StatusBadRequest || fixture.calls != 0 {
		t.Fatalf("status/calls = %d/%d body=%s", w.Code, fixture.calls, w.Body.String())
	}
}

func TestDispatchProjectNextActionMapsSourceGapWithoutDetails(t *testing.T) {
	projectID := "00000000-0000-0000-0000-000000000201"
	issueID := "00000000-0000-0000-0000-000000000202"
	fixture := &dispatchTriggerHandlerFixture{err: errors.Join(service.ErrContinuousDispatchSourceGap, errors.New("secret authority detail"))}
	h := *testHandler
	h.ContinuousDispatchTrigger = fixture
	req := newRequest(http.MethodPost,
		"/api/projects/"+projectID+"/next-actions/"+issueID+"/dispatch?workspace_id="+testWorkspaceID,
		map[string]any{"handoff_note": "x"},
	)
	req = withContinuousDispatchParams(req, projectID, issueID)
	w := httptest.NewRecorder()
	h.DispatchProjectNextAction(w, req)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "secret authority detail") {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
}

func TestDispatchProjectNextActionRejectsPlainWorkspaceMember(t *testing.T) {
	ctx := context.Background()
	var memberUserID pgtype.UUID
	email := "continuous-dispatch-member-" + uuid.NewString() + "@multica.test"
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Dispatch Member', $1) RETURNING id`, email).Scan(&memberUserID); err != nil {
		t.Fatalf("seed plain member user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, memberUserID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, memberUserID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, testWorkspaceID, memberUserID); err != nil {
		t.Fatalf("seed plain workspace member: %v", err)
	}

	projectID := "00000000-0000-0000-0000-000000000201"
	issueID := "00000000-0000-0000-0000-000000000202"
	fixture := &dispatchTriggerHandlerFixture{}
	h := *testHandler
	h.ContinuousDispatchTrigger = fixture
	req := newRequest(http.MethodPost,
		"/api/projects/"+projectID+"/next-actions/"+issueID+"/dispatch?workspace_id="+testWorkspaceID,
		map[string]any{"handoff_note": "x"},
	)
	req.Header.Set("X-User-ID", uuidToString(memberUserID))
	req = withContinuousDispatchParams(req, projectID, issueID)
	w := httptest.NewRecorder()
	h.DispatchProjectNextAction(w, req)
	if w.Code != http.StatusForbidden || fixture.calls != 0 {
		t.Fatalf("plain member status/calls = %d/%d body=%s", w.Code, fixture.calls, w.Body.String())
	}
}

var _ interface {
	DispatchIssue(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, string) (service.ContinuousDispatchTriggerResult, error)
} = (*dispatchTriggerHandlerFixture)(nil)
