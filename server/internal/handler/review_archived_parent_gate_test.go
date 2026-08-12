package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// reviewPipelineTestHandler builds a handler whose ReviewPipelineService is
// wired exactly as cmd/server does when REVIEW_PIPELINE_V2=true (enabled) or
// left nil (flag off ⇒ legacy behavior). The reviewer/coordinator ids are
// inert for the parent-notification gate under test — it only reads
// Config.Enabled — so they stay unset. When enabled, the IssueUpdated
// left-in_review listener is also registered (mirroring registerReviewListeners
// in cmd/server): the bus is synchronous, so the listener runs BEFORE
// notifyParentOfChildDone in production and clears the canonical review_state
// column — the gate must still suppress using the post-write in-memory row.
func reviewPipelineTestHandler(t *testing.T, enabled bool) *Handler {
	t.Helper()
	bus := events.New()
	h := New(db.New(testPool), testPool, realtime.NewHub(), bus, service.NewEmailService(), nil, nil, analytics.NoopClient{}, Config{AllowSignup: true})
	if enabled {
		svc := service.NewReviewPipelineService(db.New(testPool), testPool, bus, service.ReviewPipelineConfig{
			Enabled:        true,
			ReviewWIPLimit: 10,
			ReviewPriority: 5,
		})
		h.ReviewPipelineService = svc
		bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
			payload, ok := e.Payload.(map[string]any)
			if !ok {
				return
			}
			statusChanged, _ := payload["status_changed"].(bool)
			if !statusChanged {
				return
			}
			issue, ok := payload["issue"].(IssueResponse)
			if !ok {
				return
			}
			prevStatus, _ := payload["prev_status"].(string)
			if issue.Status != "in_review" && prevStatus == "in_review" {
				_ = svc.OnIssueLeftReview(context.Background(), parseUUID(issue.ID))
			}
		})
	}
	return h
}

// childDoneArchivalFixture is a parent + children pair under the handler test
// workspace. `states` pairs each child's canonical review_state with its
// pre-transition status; the parent carries an agent assignee so the legacy
// child-done path WOULD enqueue a parent task (the side effect the gate must
// suppress).
type childDoneArchivalFixture struct {
	parentID string
	agentID  string
	childIDs []string
}

func newChildDoneArchivalFixture(t *testing.T, states []struct {
	reviewState string
	status      string
}) childDoneArchivalFixture {
	t.Helper()
	ctx := context.Background()

	// Parent: open, agent-assigned.
	agentID := createHandlerTestAgent(t, "archival-parent-agent-"+time.Now().Format(time.RFC3339Nano), nil)
	var parentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, number)
		VALUES ($1, 'archival parent', 'in_progress', 'member', $2,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id::text
	`, testWorkspaceID, testUserID).Scan(&parentID); err != nil {
		t.Fatalf("seed archival parent: %v", err)
	}
	setIssueAssigneeDirect(t, parentID, "agent", agentID)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, parentID)
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, parentID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, parentID)
	})

	var childIDs []string
	for _, s := range states {
		var childID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, creator_type, creator_id,
			                   parent_issue_id, review_state, number)
			VALUES ($1, 'archival child', $2, 'member', $3, $4, NULLIF($5, ''),
			        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
			RETURNING id::text
		`, testWorkspaceID, s.status, testUserID, parentID, s.reviewState).Scan(&childID); err != nil {
			t.Fatalf("seed archival child: %v", err)
		}
		childIDs = append(childIDs, childID)
		t.Cleanup(func() {
			testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, childID)
			testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, childID)
		})
	}

	return childDoneArchivalFixture{parentID: parentID, agentID: agentID, childIDs: childIDs}
}

// batchUpdateChildStatuses drives BatchUpdateIssues against the fixture
// children — the exact surface HIV-347 used for the historical dequeue.
func batchUpdateChildStatuses(t *testing.T, h *Handler, childIDs []string, status string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch", map[string]any{
		"issue_ids": childIDs,
		"updates":   map[string]any{"status": status},
	})
	h.BatchUpdateIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("BatchUpdateIssues -> %s: expected 200, got %d: %s", status, w.Code, w.Body.String())
	}
}

// singleUpdateChildStatus drives UpdateIssue against one fixture child.
func singleUpdateChildStatus(t *testing.T, h *Handler, childID, status string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+childID, map[string]any{"status": status})
	req = withURLParam(req, "id", childID)
	h.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue child -> %s: expected 200, got %d: %s", status, w.Code, w.Body.String())
	}
}

// assertParentSilent verifies an archived dequeue left the parent untouched:
// zero system comments and zero parent-agent tasks. It also verifies the
// children really reached their terminal status, so a silent parent cannot be
// a false positive from a failed status write.
func assertParentSilent(t *testing.T, fx childDoneArchivalFixture, wantStatus string) {
	t.Helper()
	for _, childID := range fx.childIDs {
		var status string
		if err := testPool.QueryRow(context.Background(),
			`SELECT status FROM issue WHERE id = $1`, childID).Scan(&status); err != nil {
			t.Fatalf("load child status: %v", err)
		}
		if status != wantStatus {
			t.Fatalf("child %s status = %q, want %q (parent-silence assertion would be vacuous)", childID, status, wantStatus)
		}
	}
	if got := countSystemCommentsOn(t, fx.parentID); got != 0 {
		t.Errorf("archived dequeue produced %d system comments on parent, want 0", got)
	}
	if got := countPendingTasksForAgent(t, fx.parentID, fx.agentID); got != 0 {
		t.Errorf("archived dequeue enqueued %d parent-agent tasks, want 0", got)
	}
}

// TestReviewPipeline_ArchivedBatchDequeue_NoParentNotification is the HIV-349
// incident regression: a batch dequeue of already-accepted historical children
// (canonical review_state = archived_history / superseded) moving to a
// terminal status must NOT wake the parent — no system comment, no
// parent-assignee Task/Run (HIV-20/21/22 → HIV-19, HIV-30/31/32 → HIV-29).
// The gate keys only on the canonical review_state column, never on title,
// metadata temp keys or author strings.
func TestReviewPipeline_ArchivedBatchDequeue_NoParentNotification(t *testing.T) {
	h := reviewPipelineTestHandler(t, true)
	fx := newChildDoneArchivalFixture(t, []struct {
		reviewState string
		status      string
	}{
		{reviewState: service.ReviewStateArchivedHistory, status: "in_review"},
		{reviewState: service.ReviewStateArchivedHistory, status: "in_review"},
		{reviewState: service.ReviewStateSuperseded, status: "in_review"},
	})

	// Batch dequeue to cancelled (HIV-347 shape: children → cancelled).
	batchUpdateChildStatuses(t, h, fx.childIDs, "cancelled")

	assertParentSilent(t, fx, "cancelled")
}

// TestReviewPipeline_ArchivedSingleDone_Silent covers the single-update path
// (notifyParentOfChildDone) for both structured historical terminal states
// and both terminal statuses. The single path publishes prev_status, so the
// synchronous left-in_review listener runs BEFORE notifyParentOfChildDone and
// clears the canonical review_state column — the gate must still suppress
// using the post-write in-memory row (the production ordering hazard).
func TestReviewPipeline_ArchivedSingleDone_Silent(t *testing.T) {
	h := reviewPipelineTestHandler(t, true)
	for _, tc := range []struct {
		reviewState string
		status      string
	}{
		{reviewState: service.ReviewStateArchivedHistory, status: "done"},
		{reviewState: service.ReviewStateSuperseded, status: "cancelled"},
	} {
		t.Run(tc.reviewState+"/"+tc.status, func(t *testing.T) {
			fx := newChildDoneArchivalFixture(t, []struct {
				reviewState string
				status      string
			}{
				{reviewState: tc.reviewState, status: "in_review"},
			})
			singleUpdateChildStatus(t, h, fx.childIDs[0], tc.status)
			assertParentSilent(t, fx, tc.status)

			var reviewState *string
			if err := testPool.QueryRow(context.Background(),
				`SELECT review_state FROM issue WHERE id = $1`, fx.childIDs[0]).Scan(&reviewState); err != nil {
				t.Fatalf("load child review_state: %v", err)
			}
			if reviewState != nil {
				t.Fatalf("child review_state = %q after single dequeue, want NULL (cleared by the synchronous left-in_review listener before the gate ran)", *reviewState)
			}
		})
	}
}

// TestReviewPipeline_ArchivedChildDuplicateTerminalEvent_NoIncrement guards
// idempotency: re-saving the same terminal status must stay silent (0
// increment), and the first transition must also have produced nothing.
func TestReviewPipeline_ArchivedChildDuplicateTerminalEvent_NoIncrement(t *testing.T) {
	h := reviewPipelineTestHandler(t, true)
	fx := newChildDoneArchivalFixture(t, []struct {
		reviewState string
		status      string
	}{
		{reviewState: service.ReviewStateArchivedHistory, status: "in_review"},
	})

	singleUpdateChildStatus(t, h, fx.childIDs[0], "done")
	singleUpdateChildStatus(t, h, fx.childIDs[0], "done")
	singleUpdateChildStatus(t, h, fx.childIDs[0], "done")

	assertParentSilent(t, fx, "done")
}

// TestReviewPipeline_ActiveStageChildrenDone_ExactlyOneNotification proves the
// normal stage-barrier behavior is preserved when the flag is on: three active
// children completing together produce exactly one parent system comment and
// exactly one parent-agent task (requirement 2).
func TestReviewPipeline_ActiveStageChildrenDone_ExactlyOneNotification(t *testing.T) {
	h := reviewPipelineTestHandler(t, true)
	fx := newChildDoneArchivalFixture(t, []struct {
		reviewState string
		status      string
	}{
		{reviewState: "", status: "in_progress"},
		{reviewState: "", status: "in_progress"},
		{reviewState: "", status: "in_progress"},
	})
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET stage = 1 WHERE id = ANY($1::uuid[])`, fx.childIDs); err != nil {
		t.Fatalf("stage children: %v", err)
	}

	batchUpdateChildStatuses(t, h, fx.childIDs, "done")

	if got := countSystemCommentsOn(t, fx.parentID); got != 1 {
		t.Errorf("active stage completion produced %d system comments on parent, want exactly 1", got)
	}
	if got := countPendingTasksForAgent(t, fx.parentID, fx.agentID); got != 1 {
		t.Errorf("active stage completion enqueued %d parent-agent tasks, want exactly 1", got)
	}
}

// TestReviewPipeline_MixedBatch_ArchivedChildDoesNotDoubleNotify mixes an
// archived child into a normal batch: the archived child must be filtered from
// the barrier evaluation so it neither wakes the parent itself nor inflates
// the notification count of its active siblings.
func TestReviewPipeline_MixedBatch_ArchivedChildDoesNotDoubleNotify(t *testing.T) {
	h := reviewPipelineTestHandler(t, true)
	fx := newChildDoneArchivalFixture(t, []struct {
		reviewState string
		status      string
	}{
		{reviewState: service.ReviewStateArchivedHistory, status: "in_review"},
		{reviewState: "", status: "in_progress"},
		{reviewState: "", status: "in_progress"},
	})
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET stage = 1 WHERE id = ANY($1::uuid[])`, fx.childIDs); err != nil {
		t.Fatalf("stage children: %v", err)
	}

	batchUpdateChildStatuses(t, h, fx.childIDs, "done")

	if got := countSystemCommentsOn(t, fx.parentID); got != 1 {
		t.Errorf("mixed batch produced %d system comments on parent, want exactly 1", got)
	}
	if got := countPendingTasksForAgent(t, fx.parentID, fx.agentID); got != 1 {
		t.Errorf("mixed batch enqueued %d parent-agent tasks, want exactly 1", got)
	}
}

// TestReviewPipeline_FlagOff_ArchivedChildNotifiesUnchanged is the feature-flag
// contract: with REVIEW_PIPELINE_V2 off (nil service), an archived child's
// completion must keep the legacy wake — the gate is fully flag-gated.
func TestReviewPipeline_FlagOff_ArchivedChildNotifiesUnchanged(t *testing.T) {
	h := reviewPipelineTestHandler(t, false)
	fx := newChildDoneArchivalFixture(t, []struct {
		reviewState string
		status      string
	}{
		{reviewState: service.ReviewStateArchivedHistory, status: "in_review"},
	})

	singleUpdateChildStatus(t, h, fx.childIDs[0], "done")

	if got := countSystemCommentsOn(t, fx.parentID); got != 1 {
		t.Errorf("flag-off archived completion produced %d system comments on parent, want legacy 1", got)
	}
	if got := countPendingTasksForAgent(t, fx.parentID, fx.agentID); got != 1 {
		t.Errorf("flag-off archived completion enqueued %d parent-agent tasks, want legacy 1", got)
	}
}

// TestReviewPipeline_ArchivedChild_BatchThenSingle_MixedDrive guards the batch
// + single mixed drive (partial batch failure / retry shape): after a batch
// dequeue of two archived children and a subsequent single dequeue of a third,
// the parent stays silent end to end.
func TestReviewPipeline_ArchivedChild_BatchThenSingle_MixedDrive(t *testing.T) {
	h := reviewPipelineTestHandler(t, true)
	fx := newChildDoneArchivalFixture(t, []struct {
		reviewState string
		status      string
	}{
		{reviewState: service.ReviewStateArchivedHistory, status: "in_review"},
		{reviewState: service.ReviewStateArchivedHistory, status: "in_review"},
		{reviewState: service.ReviewStateSuperseded, status: "in_review"},
	})

	batchUpdateChildStatuses(t, h, fx.childIDs[:2], "cancelled")
	singleUpdateChildStatus(t, h, fx.childIDs[2], "cancelled")

	assertParentSilent(t, fx, "cancelled")
}
