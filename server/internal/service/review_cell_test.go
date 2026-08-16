package service

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// reviewCellFixture is a self-contained isolated-DB fixture for the Lane B
// review cell. Every test seeds its own workspace so tests never collide.
type reviewCellFixture struct {
	pool          *pgxpool.Pool
	queries       *db.Queries
	workspaceID   pgtype.UUID
	userID        pgtype.UUID
	implementer   pgtype.UUID
	implementerRT pgtype.UUID
	reviewer      pgtype.UUID
	reviewerRT    pgtype.UUID
	coordinator   pgtype.UUID
	coordinatorRT pgtype.UUID
	issueID       pgtype.UUID
	candidate     db.AgentTaskQueue
}

func newReviewCellPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping review cell integration test")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if parsed.Port() == "5432" {
		t.Skip("refusing to run review cell test against production port 5432")
	}
	if host := parsed.Hostname(); host != "127.0.0.1" && host != "localhost" && host != "::1" {
		t.Skipf("review cell test requires loopback database host, got %q", host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open isolated DATABASE_URL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping isolated DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newReviewCellFixture(t *testing.T, seedCandidate bool) reviewCellFixture {
	t.Helper()
	pool := newReviewCellPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := db.New(pool)
	suffix := uuid.NewString()
	f := reviewCellFixture{pool: pool, queries: queries}

	var err error
	f.workspaceID, err = insertReturningUUID(ctx, pool,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`, "review-cell-"+suffix, "review-cell-"+suffix)
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	f.userID, err = insertReturningUUID(ctx, pool,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, "owner-"+suffix, "owner-"+suffix+"@example.invalid")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, f.workspaceID, f.userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	f.implementer, f.implementerRT = seedAgent(t, ctx, pool, f.workspaceID, "implementer-"+suffix)
	f.reviewer, f.reviewerRT = seedAgent(t, ctx, pool, f.workspaceID, "reviewer-"+suffix)
	f.coordinator, f.coordinatorRT = seedAgent(t, ctx, pool, f.workspaceID, "coordinator-"+suffix)

	f.issueID, err = insertReturningUUID(ctx, pool,
		`INSERT INTO issue (workspace_id, title, status, creator_type, creator_id) VALUES ($1, $2, 'in_review', 'member', $3) RETURNING id`,
		f.workspaceID, "review-cell issue "+suffix, f.userID)
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	if seedCandidate {
		f.candidate = seedCompletedWorkTask(t, ctx, pool, f.issueID, f.workspaceID, f.implementer, f.implementerRT)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, stmt := range []string{
			`DELETE FROM review_drain_progress WHERE workspace_id = $1`,
			`DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1)`,
			`DELETE FROM comment WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1)`,
			`DELETE FROM issue WHERE workspace_id = $1`,
			`DELETE FROM agent WHERE workspace_id = $1`,
			`DELETE FROM agent_runtime WHERE workspace_id = $1`,
			`DELETE FROM member WHERE workspace_id = $1`,
			`DELETE FROM workspace WHERE id = $1`,
			`DELETE FROM "user" WHERE id = $1`,
		} {
			_, _ = pool.Exec(cleanupCtx, stmt, f.workspaceID)
		}
	})
	return f
}

func insertReturningUUID(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		return pgtype.UUID{}, err
	}
	return id, nil
}

func seedAgent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID pgtype.UUID, name string) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	rtID, err := insertReturningUUID(ctx, pool,
		`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider) VALUES ($1, $2, 'local', 'local') RETURNING id`,
		workspaceID, name+"-runtime")
	if err != nil {
		t.Fatalf("seed agent_runtime: %v", err)
	}
	agentID, err := insertReturningUUID(ctx, pool,
		`INSERT INTO agent (workspace_id, name, runtime_mode, runtime_id) VALUES ($1, $2, 'local', $3) RETURNING id`,
		workspaceID, name, rtID)
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return agentID, rtID
}

func seedCompletedWorkTask(t *testing.T, ctx context.Context, pool *pgxpool.Pool, issueID, workspaceID, agentID, runtimeID pgtype.UUID) db.AgentTaskQueue {
	t.Helper()
	taskID, err := insertReturningUUID(ctx, pool,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, task_kind, originator_source)
		 VALUES ($1, $2, $3, 'completed', 'work', 'unattributed')
		 RETURNING id`,
		agentID, runtimeID, issueID)
	if err != nil {
		t.Fatalf("seed completed work task: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET completed_at = now() WHERE id = $1`, taskID); err != nil {
		t.Fatalf("set work task completed_at: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, source_task_id)
		 VALUES ($1, $2, 'agent', $3, 'delivered', 'comment', $4)`,
		issueID, workspaceID, agentID, taskID); err != nil {
		t.Fatalf("seed delivery comment: %v", err)
	}
	task, err := db.New(pool).GetAgentTask(ctx, taskID)
	if err != nil {
		t.Fatalf("load seeded work task: %v", err)
	}
	return task
}

func newReviewCellServiceForFixture(f reviewCellFixture, cfg ReviewCellConfig) *ReviewCellService {
	return NewReviewCellService(f.queries, f.pool, events.New(), cfg)
}

func cfgWithReviewerAndCoordinator(f reviewCellFixture) ReviewCellConfig {
	return ReviewCellConfig{
		Enabled:             true,
		ReviewerAgentID:     f.reviewer,
		ReviewerAgentIDSet:  true,
		CoordinatorAgentID:  f.coordinator,
		CoordinatorAgentSet: true,
		ReviewPriority:      5,
		RepairPriority:      5,
	}
}

func mustOpenReviewTask(t *testing.T, ctx context.Context, f reviewCellFixture) db.AgentTaskQueue {
	t.Helper()
	task, err := f.queries.GetOpenReviewTaskForIssue(ctx, f.issueID)
	if err != nil {
		t.Fatalf("GetOpenReviewTaskForIssue: %v", err)
	}
	return task
}

func TestReviewCell_FullChain(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))

	// 1. Candidate enters review -> review task created (reviewer != writer).
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if issue.ReviewState.String != ReviewStateQueued {
		t.Fatalf("review_state after entry = %q, want queued", issue.ReviewState.String)
	}
	reviewTask := mustOpenReviewTask(t, ctx, f)
	if reviewTask.TaskKind != TaskKindReview {
		t.Fatalf("review task kind = %q, want review", reviewTask.TaskKind)
	}
	if !uuidEqual(reviewTask.AgentID, f.reviewer) {
		t.Fatalf("review task agent = %s, want reviewer", uuidString(reviewTask.AgentID))
	}
	if !uuidEqual(reviewTask.ReviewTargetTaskID, f.candidate.ID) {
		t.Fatalf("review target = %s, want candidate %s", uuidString(reviewTask.ReviewTargetTaskID), uuidString(f.candidate.ID))
	}

	// 2. REVISE by the assigned reviewer -> revise_requested + repair task.
	revise, err := svc.WriteVerdict(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.reviewer}, VerdictInput{
		Verdict:            "revise",
		Notes:              "needs rework",
		RepairRequirements: []string{"fix the bug"},
	})
	if err != nil {
		t.Fatalf("WriteVerdict(revise): %v", err)
	}
	if revise.ReviewState != ReviewStateReviseRequested {
		t.Fatalf("review_state after revise = %q, want revise_requested", revise.ReviewState)
	}
	if !revise.RepairTaskID.Valid {
		t.Fatalf("revise did not create a repair task")
	}
	repairTask, err := f.queries.GetAgentTask(ctx, revise.RepairTaskID)
	if err != nil {
		t.Fatalf("load repair task: %v", err)
	}
	if repairTask.TaskKind != TaskKindRepair {
		t.Fatalf("repair task kind = %q, want repair", repairTask.TaskKind)
	}
	if !uuidEqual(repairTask.AgentID, f.implementer) {
		t.Fatalf("repair task agent = %s, want implementer", uuidString(repairTask.AgentID))
	}

	// 3. Repair completes -> independent re-review (fresh review task).
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, repairTask.ID); err != nil {
		t.Fatalf("complete repair task: %v", err)
	}
	if err := svc.OnRepairTaskCompleted(ctx, repairTask.ID); err != nil {
		t.Fatalf("OnRepairTaskCompleted: %v", err)
	}
	reReviewTask := mustOpenReviewTask(t, ctx, f)
	if uuidEqual(reReviewTask.ID, reviewTask.ID) {
		t.Fatalf("re-review reused the old review task")
	}
	if !uuidEqual(reReviewTask.AgentID, f.reviewer) {
		t.Fatalf("re-review task agent = %s, want reviewer", uuidString(reReviewTask.AgentID))
	}
	issue = mustGetIssue(t, ctx, f)
	if issue.ReviewState.String != ReviewStateQueued {
		t.Fatalf("review_state after re-review = %q, want queued", issue.ReviewState.String)
	}

	// 4. PASS by the coordinator -> accepted + delivery outcome (done).
	passRes, err := svc.WriteVerdict(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.coordinator}, VerdictInput{
		Verdict: "pass",
		Notes:   "looks good",
	})
	if err != nil {
		t.Fatalf("WriteVerdict(pass): %v", err)
	}
	if passRes.ReviewState != ReviewStateAccepted {
		t.Fatalf("review_state after pass = %q, want accepted", passRes.ReviewState)
	}
	issue = mustGetIssue(t, ctx, f)
	if issue.Status != "done" {
		t.Fatalf("issue status after pass = %q, want done", issue.Status)
	}
}

// TestReviewCell_ReviseRequestedReentryDoesNotPreemptRepair guards the
// production regression where an IssueUpdated re-entry fired while an issue was
// in revise_requested (repair pending), and handleReentry overwrote
// revise_requested -> queued with a review of the in-progress repair. That
// orphaned the issue: when the repair completed, OnRepairTaskCompleted saw
// queued (not revise_requested) and skipped the independent re-review.
func TestReviewCell_ReviseRequestedReentryDoesNotPreemptRepair(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))

	// 1. Enter review.
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	// 2. REVISE -> revise_requested + repair task.
	if _, err := svc.WriteVerdict(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.reviewer}, VerdictInput{
		Verdict:            "revise",
		Notes:              "needs rework",
		RepairRequirements: []string{"fix the bug"},
	}); err != nil {
		t.Fatalf("WriteVerdict(revise): %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if issue.ReviewState.String != ReviewStateReviseRequested {
		t.Fatalf("review_state after revise = %q, want revise_requested", issue.ReviewState.String)
	}
	// 3. A spurious IssueUpdated re-entry must NOT create a review task nor
	// overwrite revise_requested while repair is pending.
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview re-entry: %v", err)
	}
	issue = mustGetIssue(t, ctx, f)
	if issue.ReviewState.String != ReviewStateReviseRequested {
		t.Fatalf("review_state after re-entry = %q, want revise_requested (unchanged)", issue.ReviewState.String)
	}
	var openReviewCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review' AND status IN ('queued','in_progress')`, f.issueID).Scan(&openReviewCount); err != nil {
		t.Fatalf("count open review tasks: %v", err)
	}
	if openReviewCount != 0 {
		t.Fatalf("open review tasks after revise_requested re-entry = %d, want 0", openReviewCount)
	}
}

func TestReviewCell_ReviewerIsImplementerFailsClosed(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	cfg := ReviewCellConfig{
		Enabled:             true,
		ReviewerAgentID:     f.implementer, // reviewer == writer
		ReviewerAgentIDSet:  true,
		CoordinatorAgentID:  f.coordinator,
		CoordinatorAgentSet: true,
	}
	svc := newReviewCellServiceForFixture(f, cfg)
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if issue.ReviewState.String != ReviewStateOwnerDecision {
		t.Fatalf("review_state = %q, want owner_decision", issue.ReviewState.String)
	}
	if !containsString(issue.ReviewStateReason.String, LineageFailureReviewerIsImplementer) {
		t.Fatalf("review_state_reason = %q, want reviewer_is_implementer", issue.ReviewStateReason.String)
	}
	if _, err := f.queries.GetOpenReviewTaskForIssue(ctx, f.issueID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected no open review task, got %v", err)
	}
}

func TestReviewCell_LineageMissingFailsClosed(t *testing.T) {
	f := newReviewCellFixture(t, false) // no delivery comment
	ctx := context.Background()
	svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if issue.ReviewState.String != ReviewStateOwnerDecision {
		t.Fatalf("review_state = %q, want owner_decision", issue.ReviewState.String)
	}
	if !containsString(issue.ReviewStateReason.String, LineageFailureNoSourceTaskID) {
		t.Fatalf("review_state_reason = %q, want no_source_task_id", issue.ReviewStateReason.String)
	}
}

func TestReviewCell_IdempotentEntry(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("first OnIssueEnteredReview: %v", err)
	}
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("second OnIssueEnteredReview: %v", err)
	}
	_ = mustOpenReviewTask(t, ctx, f)
	var count int64
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, f.issueID).Scan(&count); err != nil {
		t.Fatalf("count review tasks: %v", err)
	}
	if count != 1 {
		t.Fatalf("open review task count = %d, want 1", count)
	}
}

func TestReviewCell_CompletedCandidateConcurrentReentry(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("initial OnIssueEnteredReview: %v", err)
	}
	reviewTask := mustOpenReviewTask(t, ctx, f)
	if _, err := f.pool.Exec(ctx,
		`UPDATE agent_task_queue SET status = 'completed', completed_at = now(), result = '{}'::jsonb WHERE id = $1`,
		reviewTask.ID); err != nil {
		t.Fatalf("complete review task: %v", err)
	}

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.OnIssueEnteredReview(ctx, f.issueID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reentry: %v", err)
		}
	}

	var count int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, f.issueID).Scan(&count); err != nil {
		t.Fatalf("count review tasks: %v", err)
	}
	if count != 1 {
		t.Fatalf("review task count after concurrent reentry = %d, want one historical task", count)
	}
}

func TestReviewCell_VerdictAuthorization(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}

	// A third party agent is not the assigned reviewer.
	if _, err := svc.WriteVerdict(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.implementer}, VerdictInput{Verdict: "revise", Notes: "x"}); !errors.Is(err, ErrNotAssignedReviewer) {
		t.Fatalf("expected ErrNotAssignedReviewer, got %v", err)
	}
	// The assigned reviewer cannot PASS (coordinator/member only).
	if _, err := svc.WriteVerdict(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.reviewer}, VerdictInput{Verdict: "pass"}); !errors.Is(err, ErrNotCoordinator) {
		t.Fatalf("expected ErrNotCoordinator, got %v", err)
	}
	// After the only open review task is cancelled, a verdict has no target.
	if _, err := f.queries.CancelOpenReviewTasksForIssue(ctx, f.issueID); err != nil {
		t.Fatalf("cancel review tasks: %v", err)
	}
	if _, err := svc.WriteVerdict(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.reviewer}, VerdictInput{Verdict: "revise", Notes: "x"}); !errors.Is(err, ErrNoOpenReviewTask) {
		t.Fatalf("expected ErrNoOpenReviewTask, got %v", err)
	}
}

func TestReviewCell_AutoSelectReviewer(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	cfg := ReviewCellConfig{
		Enabled:             true,
		CoordinatorAgentID:  f.coordinator,
		CoordinatorAgentSet: true,
	}
	svc := newReviewCellServiceForFixture(f, cfg)
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	task := mustOpenReviewTask(t, ctx, f)
	if uuidEqual(task.AgentID, f.implementer) {
		t.Fatalf("auto-selected reviewer is the implementer")
	}
	if !task.ReviewTargetTaskID.Valid {
		t.Fatalf("review target task missing")
	}
}

func mustGetIssue(t *testing.T, ctx context.Context, f reviewCellFixture) db.Issue {
	t.Helper()
	issue, err := f.queries.GetIssue(ctx, f.issueID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	return issue
}

// uuidString is defined in continuous_dispatch_shadow_test.go.

func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
