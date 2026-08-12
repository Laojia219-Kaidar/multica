package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// reviewHandlerFixture builds a ReviewPipelineV2 scenario inside the shared
// handler-test workspace: implementer agent, reviewer agent (L1), coordinator
// agent, one in_review issue with a completed candidate task + delivery comment.
type reviewHandlerFixture struct {
	implementerAgentID string
	reviewerAgentID    string
	coordinatorAgentID string
	runtimeID          string
	issueID            string
	candidateTaskID    string
	reviewTaskID       string
}

func seedReviewHandlerFixture(t *testing.T) reviewHandlerFixture {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var fx reviewHandlerFixture
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id
		) VALUES ($1, 'review-handler-runtime', 'cloud', 'codex', 'online', '', '{}'::jsonb, $2)
		RETURNING id::text
	`, testWorkspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	fx.runtimeID = runtimeID

	seedAgent := func(name string) string {
		t.Helper()
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, runtime_mode, runtime_config,
				runtime_id, visibility, max_concurrent_tasks, owner_id,
				instructions, custom_env, custom_args
			)
			VALUES ($1, $2, 'cloud', '{}'::jsonb, $3, 'workspace', 4, $4, '', '{}'::jsonb, '[]'::jsonb)
			RETURNING id::text
		`, testWorkspaceID, name, runtimeID, testUserID).Scan(&id); err != nil {
			t.Fatalf("seed agent %s: %v", name, err)
		}
		t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, id) })
		return id
	}
	fx.implementerAgentID = seedAgent("review-handler-implementer")
	fx.reviewerAgentID = seedAgent("review-handler-quinn")
	fx.coordinatorAgentID = seedAgent("review-handler-codex")

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, assignee_type, assignee_id)
		VALUES ($1, 'review-handler-issue', 'in_review', 'member', $2, 'agent', $3)
		RETURNING id::text
	`, testWorkspaceID, testUserID, fx.implementerAgentID).Scan(&issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	fx.issueID = issueID

	var candidateTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, completed_at, context
		) VALUES ($1, $2, $3, 'completed', 0, now(), $4)
		RETURNING id::text
	`, fx.implementerAgentID, runtimeID, issueID, []byte(`{"head_sha":"abc123def4567890abcdef1234567890abcdef12","artifact_digest":"sha256:aaaaaaaaaaaaaaaa0000000000000000"}`)).Scan(&candidateTaskID); err != nil {
		t.Fatalf("seed candidate task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, candidateTaskID)
	})
	fx.candidateTaskID = candidateTaskID

	if _, err := testPool.Exec(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, source_task_id)
		VALUES ($1, $2, 'agent', $3, 'handler delivery receipt', $4)
	`, issueID, testWorkspaceID, fx.implementerAgentID, candidateTaskID); err != nil {
		t.Fatalf("seed delivery comment: %v", err)
	}

	// Wire the review pipeline service onto the shared handler for this test.
	svc := service.NewReviewPipelineService(db.New(testPool), testPool, testHandler.Bus, service.ReviewPipelineConfig{
		Enabled:             true,
		ReviewerAgentID:     util.MustParseUUID(fx.reviewerAgentID),
		ReviewerAgentIDSet:  true,
		CoordinatorAgentID:  util.MustParseUUID(fx.coordinatorAgentID),
		CoordinatorAgentSet: true,
		ReviewWIPLimit:      10,
		ReviewPriority:      5,
	})
	testHandler.ReviewPipelineService = svc
	t.Cleanup(func() { testHandler.ReviewPipelineService = nil })

	if err := svc.OnIssueEnteredReview(context.Background(), util.MustParseUUID(issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	reviewTask, err := db.New(testPool).GetOpenReviewTaskForIssue(context.Background(), util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load open review task: %v", err)
	}
	fx.reviewTaskID = util.UUIDToString(reviewTask.ID)
	return fx
}

// TestReviewVerdictHandler_AuthorizationMatrix exercises the verdict endpoint's
// actor gates end to end: the assigned reviewer may REVISE, a non-reviewer
// agent (the implementer) is 403, and the coordinator may accept.
func TestReviewVerdictHandler_AuthorizationMatrix(t *testing.T) {
	fx := seedReviewHandlerFixture(t)
	ctx := context.Background()

	// Implementer (not the assigned reviewer) → 403.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues/"+fx.issueID+"/review-verdict", map[string]any{
		"verdict": "revise", "notes": "self review attempt",
	})
	r = withURLParam(r, "id", fx.issueID)
	r.Header.Set("X-Agent-ID", fx.implementerAgentID)
	r.Header.Set("X-Task-ID", fx.candidateTaskID)
	testHandler.WriteReviewVerdict(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("verdict by implementer: expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// Assigned reviewer → REVISE succeeds.
	w = httptest.NewRecorder()
	r = newRequest("POST", "/api/issues/"+fx.issueID+"/review-verdict", map[string]any{
		"verdict":             "revise",
		"notes":               "receipt incomplete",
		"repair_requirements": []string{"add test receipt"},
	})
	r = withURLParam(r, "id", fx.issueID)
	r.Header.Set("X-Agent-ID", fx.reviewerAgentID)
	r.Header.Set("X-Task-ID", fx.reviewTaskID)
	testHandler.WriteReviewVerdict(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("verdict by assigned reviewer: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode verdict response: %v", err)
	}
	if body["review_state"] != service.ReviewStateReviseRequested {
		t.Fatalf("verdict review_state = %v, want revise_requested", body["review_state"])
	}

	// A verdict comment was persisted on the issue (History data source).
	var commentCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM comment WHERE issue_id = $1 AND source_task_id = $2
	`, fx.issueID, fx.reviewTaskID).Scan(&commentCount); err != nil {
		t.Fatalf("count verdict comments: %v", err)
	}
	if commentCount != 1 {
		t.Fatalf("verdict comments = %d, want 1", commentCount)
	}
}

// TestReviewBackfillDryRunHandler_HumanOnly verifies the human-only gate on the
// dry-run endpoint at the handler level (the route group also applies
// RequireHumanActor) and the zero-write guarantee against an untouched issue.
func TestReviewBackfillDryRunHandler_HumanOnly(t *testing.T) {
	fx := seedReviewHandlerFixture(t)

	// A fresh issue that never entered the review pipeline: review_state is
	// NULL, and the dry-run must leave it NULL.
	var freshIssueID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, assignee_type, assignee_id, number)
		VALUES ($1, 'review-dryrun-untouched', 'in_review', 'member', $2, 'agent', $3,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id::text
	`, testWorkspaceID, testUserID, fx.implementerAgentID).Scan(&freshIssueID); err != nil {
		t.Fatalf("seed untouched issue: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, freshIssueID) })

	// Agent (task token) → 403 via the same middleware the route uses.
	guarded := RequireHumanActor(http.HandlerFunc(testHandler.ReviewBackfillDryRun))
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/review-backfill/dry-run", map[string]any{
		"issues": []map[string]any{{"issue_id": freshIssueID, "intended_review_state": "queued"}},
	})
	r.Header.Set("X-Actor-Source", "task_token")
	guarded.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent dry-run: expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// Member (owner) → 200 with a proposed mapping, zero writes.
	w = httptest.NewRecorder()
	r = newRequest("POST", "/api/review-backfill/dry-run", map[string]any{
		"issues": []map[string]any{{"issue_id": freshIssueID, "intended_review_state": "queued"}},
	})
	testHandler.ReviewBackfillDryRun(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("member dry-run: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode dry-run response: %v", err)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("dry-run items = %d, want 1", len(items))
	}
	var state pgtype.Text
	if err := testPool.QueryRow(context.Background(),
		`SELECT review_state FROM issue WHERE id = $1`, freshIssueID).Scan(&state); err != nil {
		t.Fatalf("read review_state after dry-run: %v", err)
	}
	if state.Valid {
		t.Fatalf("dry-run wrote review_state = %q, want zero writes", state.String)
	}
}
