package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// pipeline_test.go — HIV-367 (P0-E) pipeline projection integration tests.
//
// These tests exercise the contract's required negative scenarios (§9):
//   1. in_progress with NO open Task -> "stale_awaiting_dispatch" card marker,
//      column.NoTask incremented.
//   2. in_review with NO Review Task -> "review_not_started" marker.
//   3. blocked with NO recovery Task -> "blocked_unhandled" marker.
//   4. latest task failed -> "failed" class, FailureReason surfaced, retry
//      hint in NextSystemAction.
//   5. unknown / cross-workspace -> honest 404 (not an enumeration oracle),
//      and an empty column set never silently fabricates a class.
//
// All tests run against the real migrated test Postgres at DATABASE_URL
// (loopback only). They skip cleanly when testHandler is nil so CI without
// a DB doesn't fail.

// seedPipelineProject creates a project in the test workspace and returns its
// id. One project per test keeps the projection's WHERE clause selective.
func seedPipelineProject(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, priority)
		VALUES ($1, $2, 'in_progress', 'high')
		RETURNING id
	`, testWorkspaceID, "Pipeline Test Project "+t.Name()).Scan(&id); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, id)
	})
	return id
}

// seedPipelineIssue creates an issue in the test workspace + project with the
// given status. Returns issue id.
func seedPipelineIssue(t *testing.T, projectID, status string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, project_id, title, status, priority, creator_type, creator_id)
		VALUES ($1, $2, $3, $4, 'medium', 'member', $5)
		RETURNING id
	`, testWorkspaceID, projectID, "issue-"+status, status, testUserID).Scan(&id); err != nil {
		t.Fatalf("seed issue (%s): %v", status, err)
	}
	// Issue rows cascade-delete with the project, but be explicit so a future
	// schema change can't leak test rows into shared state.
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id)
	})
	return id
}

// seedPipelineTask inserts a single agent_task_queue row for the issue with
// the given status and an optional failure reason. The test creates its own
// agent + runtime (mirroring createClaimReclaimRuntime / AgentAndIssue) so
// the fixture is self-contained and does not depend on a global agent id.
func seedPipelineTask(t *testing.T, issueID, status, failureReason string) string {
	t.Helper()
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, $2, 'cloud', 'pipeline_test_runtime', 'online',
		        'pipeline fixture', '{}'::jsonb, now(), 'private', $3)
		RETURNING id
	`, testWorkspaceID, "pipeline runtime "+issueID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
		RETURNING id
	`, testWorkspaceID, "pipeline agent "+issueID, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })

	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status, failure_reason)
		VALUES ($1, $2, $3, $4, $5::text)
		RETURNING id
	`, agentID, issueID, runtimeID, status, nullableText(failureReason)).Scan(&id); err != nil {
		t.Fatalf("seed task (%s): %v", status, err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, id)
	})
	return id
}

// nullableText returns a NULLable text value for SQL insertion: nil when the
// string is empty (so the column stays SQL NULL rather than the empty string,
// matching failure_reason's nullable semantics), the string verbatim
// otherwise.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// callGetProjectPipeline invokes the handler with the right URL param and
// returns the decoded response. Fails the test on non-200.
func callGetProjectPipeline(t *testing.T, projectID string) ProjectPipelineResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/projects/"+projectID+"/pipeline", nil)
	req = withURLParam(req, "id", projectID)
	testHandler.GetProjectPipeline(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetProjectPipeline: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ProjectPipelineResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode pipeline response: %v", err)
	}
	return resp
}

// TestPipeline_InProgress_NoOpenTask_MarksStale verifies §9 negative case 1:
// an in_progress issue with zero agent_task_queue rows must surface as
// stale_awaiting_dispatch on the card and increment the column's NoTask
// counter — not silently look "active".
func TestPipeline_InProgress_NoOpenTask_MarksStale(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	seedPipelineIssue(t, projectID, "in_progress")

	resp := callGetProjectPipeline(t, projectID)
	col := resp.Columns["in_progress"]
	if col.Total != 1 || col.NoTask != 1 {
		t.Fatalf("in_progress column: expected total=1 no_task=1, got %+v", col)
	}
	if col.Running != 0 || col.Failed != 0 || col.Unknown != 0 {
		t.Fatalf("in_progress column should have zero active/failed/unknown, got %+v", col)
	}
	// Exactly one issue and it must carry the stale marker.
	if len(resp.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(resp.Issues))
	}
	for _, issue := range resp.Issues {
		if issue.ProcessingState != "stale_awaiting_dispatch" {
			t.Fatalf("in_progress+no_task should be stale_awaiting_dispatch, got %q (issue %+v)", issue.ProcessingState, issue)
		}
		if issue.TaskClass != "no_task" {
			t.Fatalf("expected task_class=no_task, got %q", issue.TaskClass)
		}
		if issue.NextSystemAction != "dispatch" {
			t.Fatalf("expected next_system_action=dispatch, got %q", issue.NextSystemAction)
		}
		if issue.TaskID != "" {
			t.Fatalf("expected empty task_id for no_task, got %q", issue.TaskID)
		}
	}
}

// TestPipeline_InReview_NoReviewTask_MarksNotStarted verifies §9 negative
// case 2: an in_review issue with no Review Task must be marked
// review_not_started.
func TestPipeline_InReview_NoReviewTask_MarksNotStarted(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	seedPipelineIssue(t, projectID, "in_review")

	resp := callGetProjectPipeline(t, projectID)
	col := resp.Columns["in_review"]
	if col.Total != 1 || col.NoTask != 1 {
		t.Fatalf("in_review column: expected total=1 no_task=1, got %+v", col)
	}
	for _, issue := range resp.Issues {
		if issue.ProcessingState != "review_not_started" {
			t.Fatalf("in_review+no_task should be review_not_started, got %q", issue.ProcessingState)
		}
		if issue.NextSystemAction != "open_review_task" {
			t.Fatalf("expected next_system_action=open_review_task, got %q", issue.NextSystemAction)
		}
	}
}

// TestPipeline_Blocked_NoRecoveryTask_MarksUnhandled verifies §9 negative
// case 3: a blocked issue with no recovery Task must be marked
// blocked_unhandled.
func TestPipeline_Blocked_NoRecoveryTask_MarksUnhandled(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	seedPipelineIssue(t, projectID, "blocked")

	resp := callGetProjectPipeline(t, projectID)
	col := resp.Columns["blocked"]
	if col.Total != 1 || col.NoTask != 1 {
		t.Fatalf("blocked column: expected total=1 no_task=1, got %+v", col)
	}
	for _, issue := range resp.Issues {
		if issue.ProcessingState != "blocked_unhandled" {
			t.Fatalf("blocked+no_task should be blocked_unhandled, got %q", issue.ProcessingState)
		}
		if issue.NextSystemAction != "dispatch_recovery" {
			t.Fatalf("expected next_system_action=dispatch_recovery, got %q", issue.NextSystemAction)
		}
	}
}

// TestPipeline_FailedTask_SurfacesFailureReason verifies §9 negative case 4:
// the latest task on an issue is failed — class=failed, failure_reason
// surfaced verbatim, and NextSystemAction=retry_or_diagnose.
func TestPipeline_FailedTask_SurfacesFailureReason(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	issueID := seedPipelineIssue(t, projectID, "in_progress")
	const wantReason = "runtime crashed: OOM"
	seedPipelineTask(t, issueID, "failed", wantReason)

	resp := callGetProjectPipeline(t, projectID)
	col := resp.Columns["in_progress"]
	if col.Total != 1 || col.Failed != 1 || col.NoTask != 0 {
		t.Fatalf("in_progress column: expected total=1 failed=1 no_task=0, got %+v", col)
	}
	issue := resp.Issues[issueID]
	if issue.TaskClass != "failed" {
		t.Fatalf("expected task_class=failed, got %q", issue.TaskClass)
	}
	if issue.FailureReason != wantReason {
		t.Fatalf("expected failure_reason=%q, got %q", wantReason, issue.FailureReason)
	}
	if issue.NextSystemAction != "retry_or_diagnose" {
		t.Fatalf("expected next_system_action=retry_or_diagnose, got %q", issue.NextSystemAction)
	}
	if issue.TaskStatus != "failed" {
		t.Fatalf("expected task_status=failed, got %q", issue.TaskStatus)
	}
}

// TestPipeline_TerminalNoWriteback_CountsHonestly verifies §9 case 5: a
// terminal (completed) task with NO task-linked comment (comment.source_task_id
// IS NULL) increments the column's TerminalNoWriteback counter, and the card
// shows TaskClass=terminal.
func TestPipeline_TerminalNoWriteback_CountsHonestly(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	issueID := seedPipelineIssue(t, projectID, "in_review")
	seedPipelineTask(t, issueID, "completed", "")

	resp := callGetProjectPipeline(t, projectID)
	col := resp.Columns["in_review"]
	if col.Total != 1 || col.Terminal != 1 {
		t.Fatalf("in_review column: expected total=1 terminal=1, got %+v", col)
	}
	if col.TerminalNoWriteback != 1 {
		t.Fatalf("expected terminal_no_writeback=1 (no task-linked comment), got %d", col.TerminalNoWriteback)
	}
	issue := resp.Issues[issueID]
	if issue.TaskClass != "terminal" {
		t.Fatalf("expected task_class=terminal, got %q", issue.TaskClass)
	}
	if issue.LatestReceiptCommentID != "" {
		t.Fatalf("expected empty latest_receipt_comment_id, got %q", issue.LatestReceiptCommentID)
	}
}

// TestPipeline_RunningTask_ClassifiedRunning verifies the positive case: an
// in_progress issue with a running task shows class=running and does NOT
// increment NoTask/Failed.
func TestPipeline_RunningTask_ClassifiedRunning(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	issueID := seedPipelineIssue(t, projectID, "in_progress")
	seedPipelineTask(t, issueID, "running", "")

	resp := callGetProjectPipeline(t, projectID)
	col := resp.Columns["in_progress"]
	if col.Total != 1 || col.Running != 1 || col.NoTask != 0 || col.Failed != 0 {
		t.Fatalf("in_progress column: expected total=1 running=1, got %+v", col)
	}
	issue := resp.Issues[issueID]
	if issue.TaskClass != "running" {
		t.Fatalf("expected task_class=running, got %q", issue.TaskClass)
	}
	if issue.ProcessingState != "active" {
		t.Fatalf("in_progress+running should be active, got %q", issue.ProcessingState)
	}
}

// TestPipeline_QueuedTask_ClassifiedQueued verifies HIV-383 repair item 1:
// a queued task must be classified as "queued" (NOT folded into "running"),
// column.Queued must be incremented, and column.Running must stay at 0.
// Before the repair, queued was folded into running, making column.Queued
// dead code and showing queued cards with a running spinner.
func TestPipeline_QueuedTask_ClassifiedQueued(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	issueID := seedPipelineIssue(t, projectID, "in_progress")
	seedPipelineTask(t, issueID, "queued", "")

	resp := callGetProjectPipeline(t, projectID)
	col := resp.Columns["in_progress"]
	if col.Total != 1 || col.Queued != 1 {
		t.Fatalf("in_progress column: expected total=1 queued=1, got %+v", col)
	}
	if col.Running != 0 {
		t.Fatalf("queued task must NOT increment running, got running=%d", col.Running)
	}
	issue := resp.Issues[issueID]
	if issue.TaskClass != "queued" {
		t.Fatalf("expected task_class=queued (not folded into running), got %q", issue.TaskClass)
	}
	if issue.TaskStatus != "queued" {
		t.Fatalf("expected task_status=queued, got %q", issue.TaskStatus)
	}
	if issue.ProcessingState != "active" {
		t.Fatalf("in_progress+queued should be active, got %q", issue.ProcessingState)
	}
}

// TestPipeline_CrossWorkspaceProject_Returns404 verifies §9 cross-tenant
// case: a member of workspace A cannot read workspace B's project pipeline —
// must return 404, never an enumeration oracle.
func TestPipeline_CrossWorkspaceProject_Returns404(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	// Reuse the foreign-workspace fixture helper, which creates an isolated
	// workspace + project-less issue + task. We add a project there.
	ctx := context.Background()
	var foreignWSID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Pipeline Foreign WS", "pipeline-foreign-ws", "cross-tenant pipeline test", "PFX").Scan(&foreignWSID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWSID) })

	var foreignProjectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, priority)
		VALUES ($1, 'foreign project', 'in_progress', 'medium')
		RETURNING id
	`, foreignWSID).Scan(&foreignProjectID); err != nil {
		t.Fatalf("seed foreign project: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/projects/"+foreignProjectID+"/pipeline", nil)
	req = withURLParam(req, "id", foreignProjectID)
	testHandler.GetProjectPipeline(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace pipeline: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestPipeline_InvalidProjectID_Returns400 verifies a malformed project UUID
// fails fast with 400, not a 500.
func TestPipeline_InvalidProjectID_Returns400(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/projects/not-a-uuid/pipeline", nil)
	req = withURLParam(req, "id", "not-a-uuid")
	testHandler.GetProjectPipeline(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid project id: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestPipeline_CapabilityFlags_AllWired verifies §6: all capability flags
// are true now that HIV-355 (dispatch) and HIV-405 (project start) have landed.
func TestPipeline_CapabilityFlags_AllWired(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	resp := callGetProjectPipeline(t, projectID)
	if !resp.CapabilityFlags.DispatchPreview || !resp.CapabilityFlags.Dispatch || !resp.CapabilityFlags.ProjectStart {
		t.Fatalf("dispatch/dispatch_preview/project_start must be true now that HIV-355/HIV-405 landed, got %+v", resp.CapabilityFlags)
	}
	if !resp.CapabilityFlags.CancelTask || !resp.CapabilityFlags.RerunIssue || !resp.CapabilityFlags.UpdateStatus {
		t.Fatalf("existing canonical actions (cancel/rerun/status) must be true, got %+v", resp.CapabilityFlags)
	}
}

// TestPipeline_EmptyProject_ReturnsZeroedColumns verifies a project with no
// non-terminal issues returns all-zero columns and an empty issue map — never
// nil maps, never fabricated classes.
func TestPipeline_EmptyProject_ReturnsZeroedColumns(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := seedPipelineProject(t)
	resp := callGetProjectPipeline(t, projectID)
	if len(resp.Issues) != 0 {
		t.Fatalf("empty project: expected 0 issues, got %d", len(resp.Issues))
	}
	if len(resp.Columns) != len(pipelineIssueStatuses) {
		t.Fatalf("expected %d columns, got %d", len(pipelineIssueStatuses), len(resp.Columns))
	}
	for status, col := range resp.Columns {
		if col.Total != 0 || col.Running != 0 || col.Unknown != 0 {
			t.Fatalf("empty project column %s: expected all-zero, got %+v", status, col)
		}
	}
}
