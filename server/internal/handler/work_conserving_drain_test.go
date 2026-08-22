package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

const workConservingDrainTestProjectID = "00000000-0000-0000-0000-000000000805"

type workConservingDrainHandlerFixture struct {
	result service.WorkConservingDrainResult
	err    error
	req    service.WorkConservingDrainRequest
	calls  int
}

func (f *workConservingDrainHandlerFixture) Drain(_ context.Context, req service.WorkConservingDrainRequest) (service.WorkConservingDrainResult, error) {
	f.calls++
	f.req = req
	return f.result, f.err
}

func workConservingDrainTestRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+workConservingDrainTestProjectID+"/next-actions/drain?workspace_id="+testWorkspaceID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	return withURLParam(req, "id", workConservingDrainTestProjectID)
}

func TestDrainProjectNextActionsDerivesScopeDefaultsBatchAndSanitizesOutcomes(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fixture := &workConservingDrainHandlerFixture{result: service.WorkConservingDrainResult{
		State:     service.WorkConservingDrainStateReady,
		BatchSize: 1,
		Results: []service.WorkConservingDrainIssueResult{
			{IssueID: "issue-conflict", Outcome: service.WorkConservingDrainConflict, Reason: "raw postgres detail"},
			{IssueID: "issue-gap", Outcome: service.WorkConservingDrainSourceGap, Reason: "raw internal path"},
			{IssueID: "issue-blocked", Outcome: service.WorkConservingDrainBlocked, Reason: "issue_authority_missing", NotAttempted: true},
		},
	}}
	h := *testHandler
	h.WorkConservingDrain = fixture
	w := httptest.NewRecorder()
	h.DrainProjectNextActions(w, workConservingDrainTestRequest(`{}`))

	if w.Code != http.StatusOK || fixture.calls != 1 {
		t.Fatalf("status/calls = %d/%d body=%s", w.Code, fixture.calls, w.Body.String())
	}
	if fixture.req.BatchSize != 1 || uuidToString(fixture.req.WorkspaceID) != testWorkspaceID || uuidToString(fixture.req.ProjectID) != workConservingDrainTestProjectID || uuidToString(fixture.req.ActorUserID) != testUserID {
		t.Fatalf("derived request = %+v", fixture.req)
	}
	if strings.Contains(w.Body.String(), "postgres") || strings.Contains(w.Body.String(), "internal path") {
		t.Fatalf("response leaked internal error: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "continuous dispatch truth changed") || !strings.Contains(w.Body.String(), "issue_authority_missing") {
		t.Fatalf("response lost truthful public outcomes: %s", w.Body.String())
	}
}

func TestDrainProjectNextActionsAcceptsOnlyBoundedBatchSize(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fixture := &workConservingDrainHandlerFixture{result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateReady}}
	h := *testHandler
	h.WorkConservingDrain = fixture

	accepted := httptest.NewRecorder()
	h.DrainProjectNextActions(accepted, workConservingDrainTestRequest(`{"batch_size":20}`))
	if accepted.Code != http.StatusOK || fixture.req.BatchSize != 20 {
		t.Fatalf("accepted status/batch = %d/%d body=%s", accepted.Code, fixture.req.BatchSize, accepted.Body.String())
	}

	for _, body := range []string{
		`{"batch_size":0}`,
		`{"batch_size":21}`,
		`{"employee_id":"client-selected"}`,
		`{"runtime_id":"client-selected"}`,
		`{"issue_id":"client-selected"}`,
		`{"actor_user_id":"client-selected"}`,
		`{} {}`,
		`null`,
		`{"batch_size":null}`,
		``,
	} {
		t.Run(body, func(t *testing.T) {
			before := fixture.calls
			w := httptest.NewRecorder()
			h.DrainProjectNextActions(w, workConservingDrainTestRequest(body))
			if w.Code != http.StatusBadRequest || fixture.calls != before {
				t.Fatalf("status/calls = %d/%d body=%s", w.Code, fixture.calls, w.Body.String())
			}
		})
	}
	for _, rawQuery := range []string{
		"workspace_id=" + testWorkspaceID + "&model=client-selected",
		"workspace_id=" + testWorkspaceID + "&workspace_id=" + testWorkspaceID,
	} {
		before := fixture.calls
		req := workConservingDrainTestRequest(`{}`)
		req.URL.RawQuery = rawQuery
		w := httptest.NewRecorder()
		h.DrainProjectNextActions(w, req)
		if w.Code != http.StatusBadRequest || fixture.calls != before {
			t.Fatalf("query %q status/calls = %d/%d body=%s", rawQuery, w.Code, fixture.calls, w.Body.String())
		}
	}
}

func TestDrainProjectNextActionsRequiresOwnerOrAdmin(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	createRoleUser := func(role string) string {
		t.Helper()
		ctx := t.Context()
		var userID string
		email := fmt.Sprintf("drain-%s-%d@multica.test", role, time.Now().UnixNano())
		if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, "Drain "+role, email).Scan(&userID); err != nil {
			t.Fatalf("create %s user: %v", role, err)
		}
		if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`, testWorkspaceID, userID, role); err != nil {
			t.Fatalf("create %s member: %v", role, err)
		}
		t.Cleanup(func() {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, userID)
			_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
		})
		return userID
	}

	fixture := &workConservingDrainHandlerFixture{result: service.WorkConservingDrainResult{State: service.WorkConservingDrainStateReady}}
	h := *testHandler
	h.WorkConservingDrain = fixture

	memberReq := workConservingDrainTestRequest(`{}`)
	memberReq.Header.Set("X-User-ID", createRoleUser("member"))
	memberW := httptest.NewRecorder()
	h.DrainProjectNextActions(memberW, memberReq)
	if memberW.Code != http.StatusForbidden || fixture.calls != 0 {
		t.Fatalf("member status/calls = %d/%d body=%s", memberW.Code, fixture.calls, memberW.Body.String())
	}

	adminReq := workConservingDrainTestRequest(`{}`)
	adminReq.Header.Set("X-User-ID", createRoleUser("admin"))
	adminW := httptest.NewRecorder()
	h.DrainProjectNextActions(adminW, adminReq)
	if adminW.Code != http.StatusOK || fixture.calls != 1 {
		t.Fatalf("admin status/calls = %d/%d body=%s", adminW.Code, fixture.calls, adminW.Body.String())
	}
}

func TestDrainProjectNextActionsFailsClosedForNilAndSourceGap(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	h := *testHandler
	h.WorkConservingDrain = nil
	w := httptest.NewRecorder()
	h.DrainProjectNextActions(w, workConservingDrainTestRequest(`{}`))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil status = %d body=%s", w.Code, w.Body.String())
	}

	h.WorkConservingDrain = &workConservingDrainHandlerFixture{
		result: service.WorkConservingDrainResult{
			State:      service.WorkConservingDrainStateSourceGap,
			ReasonCode: "projection_source_gap",
			Results:    []service.WorkConservingDrainIssueResult{},
		},
		err: nil,
	}
	w = httptest.NewRecorder()
	h.DrainProjectNextActions(w, workConservingDrainTestRequest(`{}`))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "authority adapter") || !strings.Contains(w.Body.String(), "projection_source_gap") {
		t.Fatalf("source-gap status/body = %d/%s", w.Code, w.Body.String())
	}

	h.WorkConservingDrain = &workConservingDrainHandlerFixture{err: errors.New("raw authority adapter failure")}
	w = httptest.NewRecorder()
	h.DrainProjectNextActions(w, workConservingDrainTestRequest(`{}`))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "authority adapter") {
		t.Fatalf("error status/body = %d/%s", w.Code, w.Body.String())
	}
}
