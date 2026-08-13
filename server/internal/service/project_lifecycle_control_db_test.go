package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// seedPausedProjectFixture seeds workspace/user/runtime/agent/issue and a
// project whose status is togglable for the control-service DB tests.
func seedPausedProjectFixture(t *testing.T, projectStatus string) (*pgxpool.Pool, string, string, string) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	workspaceID, _, agentID, issueID := seedAttributionFixture(t, pool)

	var projectID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, lead_type, lead_id)
		VALUES ($1, 'control project', $2, 'agent', $3)
		RETURNING id`, workspaceID, projectStatus, agentID).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET project_id = $1 WHERE id = $2`, projectID, issueID); err != nil {
		t.Fatalf("attach issue to project: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})
	return pool, workspaceID, projectID, issueID
}

func loadIssue(t *testing.T, pool *pgxpool.Pool, issueID string) db.Issue {
	t.Helper()
	var wsID, asgID, projID string
	if err := pool.QueryRow(context.Background(),
		`SELECT workspace_id::text, assignee_id::text, COALESCE(project_id::text,'') FROM issue WHERE id=$1`, issueID).Scan(&wsID, &asgID, &projID); err != nil {
		t.Fatalf("load issue: %v", err)
	}
	is := db.Issue{
		ID:           util.MustParseUUID(issueID),
		WorkspaceID:  util.MustParseUUID(wsID),
		AssigneeID:   util.MustParseUUID(asgID),
		AssigneeType: textValue("agent"),
		CreatorType:  "member",
		Priority:     "medium",
	}
	if projID != "" {
		is.ProjectID = util.MustParseUUID(projID)
	}
	return is
}

// Gauss re-review #2: a paused project must reject task enqueue at the shared
// prepare chokepoint (ErrProjectPausedDispatch) — pause must actually stop new
// dispatch.
func TestPausedProjectGatesEnqueue(t *testing.T) {
	pool, _, _, issueID := seedPausedProjectFixture(t, "paused")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}

	_, err := svc.EnqueueTaskForIssue(context.Background(), loadIssue(t, pool, issueID))
	if !errors.Is(err, ErrProjectPausedDispatch) {
		t.Fatalf("enqueue err = %v, want ErrProjectPausedDispatch", err)
	}
}

// Pause twice is idempotent: the second returns Replayed=true (references
// receipt.Replayed, closing the idempotent-replay gap).
func TestPauseDispatchReplay(t *testing.T) {
	pool, workspaceID, projectID, _ := seedPausedProjectFixture(t, "in_progress")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	ctrl := NewProjectLifecycleControlService(q, svc)
	ctx := context.Background()

	r1, err := ctrl.PauseDispatch(ctx, util.MustParseUUID(workspaceID), util.MustParseUUID(projectID), "k1")
	if err != nil {
		t.Fatalf("first pause: %v", err)
	}
	if !r1.Applied || r1.AfterStatus != "paused" {
		t.Fatalf("first pause receipt = %+v, want applied + paused", r1)
	}
	r2, err := ctrl.PauseDispatch(ctx, util.MustParseUUID(workspaceID), util.MustParseUUID(projectID), "k1")
	if err != nil {
		t.Fatalf("second pause: %v", err)
	}
	if !r2.Replayed || r2.Applied {
		t.Fatalf("second pause receipt = %+v, want replayed (not re-applied)", r2)
	}
}

// Gauss re-review #2 (mention path): paused project gates the mention enqueue.
func TestPausedProjectGatesMentionEnqueue(t *testing.T) {
	pool, _, _, issueID := seedPausedProjectFixture(t, "paused")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}

	is := loadIssue(t, pool, issueID)
	_, err := svc.EnqueueTaskForMention(context.Background(), is, is.AssigneeID, pgtype.UUID{})
	if !errors.Is(err, ErrProjectPausedDispatch) {
		t.Fatalf("mention enqueue err = %v, want ErrProjectPausedDispatch", err)
	}
}

// Gauss re-review #2 (quick-create path): paused project gates quick-create.
func TestPausedProjectGatesQuickCreate(t *testing.T) {
	pool, workspaceID, projectID, _ := seedPausedProjectFixture(t, "paused")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}

	_, _, agentID, _ := seedAttributionFixture(t, pool)
	_, err := svc.EnqueueQuickCreateTask(context.Background(),
		util.MustParseUUID(workspaceID), pgtype.UUID{}, util.MustParseUUID(agentID), pgtype.UUID{},
		"hi", "medium", "", util.MustParseUUID(projectID), pgtype.UUID{}, nil)
	if !errors.Is(err, ErrProjectPausedDispatch) {
		t.Fatalf("quick-create err = %v, want ErrProjectPausedDispatch", err)
	}
}

// C1 (Gauss evidence_gap #1): continue dispatches exactly one ready-frontier
// task and never duplicates it on a second continue.
func TestContinueCreatesSingleTaskAndNoDuplicate(t *testing.T) {
	pool, workspaceID, projectID, issueID := seedPausedProjectFixture(t, "in_progress")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	ctrl := NewProjectLifecycleControlService(q, svc)
	ctx := context.Background()
	wsUUID, pidUUID := util.MustParseUUID(workspaceID), util.MustParseUUID(projectID)

	r1, err := ctrl.Continue(ctx, wsUUID, pidUUID, "continue-k1")
	if err != nil {
		t.Fatalf("first continue: %v", err)
	}
	if !r1.Applied || r1.TaskID == nil {
		t.Fatalf("first continue receipt = %+v, want applied + task id", r1)
	}
	if countTasks(t, pool, issueID) != 1 {
		t.Fatalf("task count after first continue = %d, want 1", countTasks(t, pool, issueID))
	}

	r2, err := ctrl.Continue(ctx, wsUUID, pidUUID, "continue-k1")
	if err != nil {
		t.Fatalf("second continue: %v", err)
	}
	// Idempotent replay: the existing live task is returned, not re-applied.
	if !r2.Replayed || r2.Applied {
		t.Fatalf("second continue receipt = %+v, want Replayed=true Applied=false", r2)
	}
	if r2.TaskID == nil || r1.TaskID == nil || *r2.TaskID != *r1.TaskID {
		t.Fatalf("second continue task_id = %v, want same as first %v", r2.TaskID, r1.TaskID)
	}
	if countTasks(t, pool, issueID) != 1 {
		t.Fatalf("task count after second continue = %d, want 1 (no duplicate)", countTasks(t, pool, issueID))
	}
}

func countTasks(t *testing.T, pool *pgxpool.Pool, issueID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1`, issueID).Scan(&n); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	return n
}

// Gauss re-review #4 (phase_critical #1): EnqueueTaskForIssue now returns the
// ErrDuplicatePendingTask sentinel on the unique-index violation (issue-prepare
// path aligned with the mention path), so the continue replay branch is reachable.
func TestEnqueueTaskForIssueReturnsDuplicateSentinel(t *testing.T) {
	pool, _, _, issueID := seedPausedProjectFixture(t, "in_progress")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	ctx := context.Background()
	is := loadIssue(t, pool, issueID)

	if _, err := svc.EnqueueTaskForIssue(ctx, is); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if _, err := svc.EnqueueTaskForIssue(ctx, is); !errors.Is(err, ErrDuplicatePendingTask) {
		t.Fatalf("second enqueue err = %v, want ErrDuplicatePendingTask", err)
	}
}

// Gauss re-review #5 regression: resume must NOT duplicate a task when the
// frontier already has a running task (pause does not stop running tasks).
func TestResumeDoesNotDuplicateRunningTask(t *testing.T) {
	pool, workspaceID, projectID, issueID := seedPausedProjectFixture(t, "in_progress")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	ctrl := NewProjectLifecycleControlService(q, svc)
	ctx := context.Background()
	wsUUID, pidUUID := util.MustParseUUID(workspaceID), util.MustParseUUID(projectID)

	// 1. dispatch the frontier, then mark it running (simulate daemon claim).
	if _, err := ctrl.Continue(ctx, wsUUID, pidUUID, "c1"); err != nil {
		t.Fatalf("continue: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='running' WHERE issue_id=$1`, issueID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	// 2. pause (does not stop running task), then resume.
	if _, err := ctrl.PauseDispatch(ctx, wsUUID, pidUUID, "p1"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	r, err := ctrl.Resume(ctx, wsUUID, pidUUID, "r1")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	// The project status transition applies; the task must be replayed (existing
	// running task returned, no duplicate) — never a new task.
	if !r.Replayed {
		t.Fatalf("resume receipt = %+v, want Replayed=true (existing running task returned)", r)
	}
	if countTasks(t, pool, issueID) != 1 {
		t.Fatalf("task count after resume = %d, want 1 (no duplicate)", countTasks(t, pool, issueID))
	}
}

// Slice 4 contract: close fails closed (OUTCOME_COVERAGE_INCOMPLETE) when the
// project has all-terminal issues but no confirmed outcomes; status unchanged.
func TestCloseFailsClosedWithoutOutcomes(t *testing.T) {
	pool, workspaceID, projectID, issueID := seedPausedProjectFixture(t, "in_progress")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	ctrl := NewProjectLifecycleControlService(q, svc)
	ctx := context.Background()
	// make the only issue terminal (done) so "all terminal" holds but outcome=0.
	if _, err := pool.Exec(ctx, `UPDATE issue SET status='done' WHERE id=$1`, issueID); err != nil {
		t.Fatalf("mark issue done: %v", err)
	}

	pkg, err := ctrl.GenerateClosurePackage(ctx, util.MustParseUUID(workspaceID), util.MustParseUUID(projectID), "pkg-k1")
	if err != nil {
		t.Fatalf("generate package: %v", err)
	}
	if !pkg.ReviewRequired {
		t.Fatalf("package review_required = false, want true")
	}
	if pkg.Digest == "" {
		t.Fatalf("package digest empty")
	}

	r, err := ctrl.Close(ctx, util.MustParseUUID(workspaceID), util.MustParseUUID(projectID), "close-k1")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if !containsStr(r.Blockers, "OUTCOME_COVERAGE_INCOMPLETE") || !containsStr(r.Blockers, "CLOSURE_PACKAGE_MISSING") {
		t.Fatalf("close blockers = %v, want OUTCOME_COVERAGE_INCOMPLETE + CLOSURE_PACKAGE_MISSING", r.Blockers)
	}
	if r.Applied {
		t.Fatalf("close applied a terminal write despite unmet gates: %+v", r)
	}
	// status must be unchanged.
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM project WHERE id=$1`, projectID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("close mutated status to %q, want in_progress", status)
	}
}

// digest determinism: same package state yields the same sha256 fingerprint.
func TestClosurePackageDigestDeterministic(t *testing.T) {
	p1 := &ClosurePackage{ProjectID: "p1", Status: "in_progress", TerminalIssueCount: 4, ReviewRequired: true}
	p2 := &ClosurePackage{ProjectID: "p1", Status: "in_progress", TerminalIssueCount: 4, ReviewRequired: true}
	if closurePackageDigest(p1) != closurePackageDigest(p2) {
		t.Fatalf("digest not deterministic: %s vs %s", closurePackageDigest(p1), closurePackageDigest(p2))
	}
	p2.TerminalIssueCount = 5
	if closurePackageDigest(p1) == closurePackageDigest(p2) {
		t.Fatalf("digest did not change on different terminal count")
	}
}
