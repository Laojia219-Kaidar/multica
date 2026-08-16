package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type drainFixture struct {
	pool        *pgxpool.Pool
	queries     *db.Queries
	workspaceID pgtype.UUID
	userID      pgtype.UUID
	implementer pgtype.UUID
	implRT      pgtype.UUID
	reviewer    pgtype.UUID
	reviewerRT  pgtype.UUID
}

func newDrainFixture(t *testing.T) drainFixture {
	t.Helper()
	pool := newReviewCellPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	queries := db.New(pool)
	suffix := uuid.NewString()
	f := drainFixture{pool: pool, queries: queries}

	var err error
	f.workspaceID, err = insertReturningUUID(ctx, pool,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`, "drain-"+suffix, "drain-"+suffix)
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	f.userID, err = insertReturningUUID(ctx, pool,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, "drain-owner-"+suffix, "drain-owner-"+suffix+"@example.invalid")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, f.workspaceID, f.userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	f.implementer, f.implRT = seedAgent(t, ctx, pool, f.workspaceID, "drain-impl-"+suffix)
	f.reviewer, f.reviewerRT = seedAgent(t, ctx, pool, f.workspaceID, "drain-reviewer-"+suffix)

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

// seedDrainIssue creates an in_review issue and, optionally, a candidate work
// task (with a delivery comment) of the given status. Returns issue + candidate
// task id (invalid when no candidate).
func (f drainFixture) seedDrainIssue(t *testing.T, ctx context.Context, candidateStatus string, number int) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	issueID, err := insertReturningUUID(ctx, f.pool,
		`INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, number) VALUES ($1, $2, 'in_review', 'member', $3, $4) RETURNING id`,
		f.workspaceID, "drain issue "+uuid.NewString(), f.userID, number)
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if candidateStatus == "" {
		return issueID, pgtype.UUID{}
	}
	taskID, err := insertReturningUUID(ctx, f.pool,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, task_kind, originator_source)
		 VALUES ($1, $2, $3, $4, 'work', 'unattributed') RETURNING id`,
		f.implementer, f.implRT, issueID, candidateStatus)
	if err != nil {
		t.Fatalf("seed candidate task: %v", err)
	}
	if candidateStatus == "completed" {
		if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET completed_at = now() WHERE id = $1`, taskID); err != nil {
			t.Fatalf("set completed_at: %v", err)
		}
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, source_task_id)
		 VALUES ($1, $2, 'agent', $3, 'delivered', 'comment', $4)`,
		issueID, f.workspaceID, f.implementer, taskID); err != nil {
		t.Fatalf("seed delivery comment: %v", err)
	}
	return issueID, taskID
}

func (f drainFixture) seedNewerWorkTask(t *testing.T, ctx context.Context, issueID pgtype.UUID) {
	t.Helper()
	taskID, err := insertReturningUUID(ctx, f.pool,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, task_kind, originator_source)
		 VALUES ($1, $2, $3, 'completed', 'work', 'unattributed') RETURNING id`,
		f.implementer, f.implRT, issueID)
	if err != nil {
		t.Fatalf("seed newer work task: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET created_at = now() + interval '1 hour', completed_at = now() + interval '1 hour' WHERE id = $1`, taskID); err != nil {
		t.Fatalf("bump newer task created_at: %v", err)
	}
}

func TestReviewDrain_ClassifyInReview(t *testing.T) {
	f := newDrainFixture(t)
	ctx := context.Background()

	direct, _ := f.seedDrainIssue(t, ctx, "completed", 1)
	needsRepair, _ := f.seedDrainIssue(t, ctx, "failed", 2)
	noCandidate, _ := f.seedDrainIssue(t, ctx, "", 3)
	missingEvidence, _ := f.seedDrainIssue(t, ctx, "running", 4)
	superseded, supersededCandidate := f.seedDrainIssue(t, ctx, "completed", 5)
	f.seedNewerWorkTask(t, ctx, superseded)
	_ = supersededCandidate

	drain := NewReviewDrainService(f.queries, nil)
	summary, err := drain.ClassifyInReview(ctx, f.workspaceID)
	if err != nil {
		t.Fatalf("ClassifyInReview: %v", err)
	}
	if summary.Total != 5 || summary.DirectlyReviewable != 1 || summary.NeedsRepair != 1 ||
		summary.NoCandidate != 1 || summary.MissingEvidence != 1 || summary.Superseded != 1 {
		t.Fatalf("unexpected classification summary: %+v", summary)
	}

	expect := map[pgtype.UUID]string{
		direct:          "pending",
		needsRepair:     "skipped",
		noCandidate:     "skipped",
		missingEvidence: "skipped",
		superseded:      "superseded",
	}
	for issueID, wantStatus := range expect {
		row, err := f.queries.GetDrainProgressForIssue(ctx, issueID)
		if err != nil {
			t.Fatalf("GetDrainProgressForIssue: %v", err)
		}
		if row.Status != wantStatus {
			t.Fatalf("issue %s status = %q, want %q", uuidString(issueID), row.Status, wantStatus)
		}
	}
}

func TestReviewDrain_BatchBounded(t *testing.T) {
	f := newDrainFixture(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		f.seedDrainIssue(t, ctx, "completed", i+1)
	}

	cell := NewReviewCellService(f.queries, f.pool, nil, ReviewCellConfig{
		Enabled:             true,
		ReviewerAgentID:     f.reviewer,
		ReviewerAgentIDSet:  true,
		CoordinatorAgentID:  f.implementer,
		CoordinatorAgentSet: true,
	})
	drain := NewReviewDrainService(f.queries, cell)
	if _, err := drain.ClassifyInReview(ctx, f.workspaceID); err != nil {
		t.Fatalf("ClassifyInReview: %v", err)
	}

	// First batch processes only 2 of 5.
	receipt, err := drain.DrainBatch(ctx, f.workspaceID, 2)
	if err != nil {
		t.Fatalf("DrainBatch(2): %v", err)
	}
	if receipt.Processed != 2 || receipt.ReviewTasks != 2 {
		t.Fatalf("first batch receipt = %+v, want 2 processed", receipt)
	}

	// Second batch processes 2 more; 1 remains pending.
	receipt, err = drain.DrainBatch(ctx, f.workspaceID, 2)
	if err != nil {
		t.Fatalf("DrainBatch(2) second: %v", err)
	}
	if receipt.Processed != 2 {
		t.Fatalf("second batch receipt = %+v, want 2 processed", receipt)
	}

	var reviewTaskCount int64
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE task_kind = 'review' AND issue_id IN (SELECT id FROM issue WHERE workspace_id = $1)`, f.workspaceID).Scan(&reviewTaskCount); err != nil {
		t.Fatalf("count review tasks: %v", err)
	}
	if reviewTaskCount != 4 {
		t.Fatalf("review task count = %d, want 4 (bounded, not 5)", reviewTaskCount)
	}
}

func TestReviewDrain_AuthorityDispatchOnlyDoesNotCreateReviewTask(t *testing.T) {
	f := newDrainFixture(t)
	ctx := context.Background()
	issueID, _ := f.seedDrainIssue(t, ctx, "completed", 1)
	cell := NewReviewCellService(f.queries, f.pool, nil, ReviewCellConfig{
		Enabled:               true,
		AuthorityDispatchOnly: true,
		ReviewerAgentID:       f.reviewer,
		ReviewerAgentIDSet:    true,
		CoordinatorAgentID:    f.implementer,
		CoordinatorAgentSet:   true,
	})
	drain := NewReviewDrainService(f.queries, cell)
	if _, err := drain.ClassifyInReview(ctx, f.workspaceID); err != nil {
		t.Fatalf("ClassifyInReview: %v", err)
	}
	receipt, err := drain.DrainBatch(ctx, f.workspaceID, 1)
	if err != nil {
		t.Fatalf("DrainBatch: %v", err)
	}
	if receipt.ReviewTasks != 0 {
		t.Fatalf("drain receipt = %+v, want no review task", receipt)
	}
	var reviewTaskCount int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, issueID).Scan(&reviewTaskCount); err != nil {
		t.Fatalf("count review tasks: %v", err)
	}
	if reviewTaskCount != 0 {
		t.Fatalf("review task count = %d, want 0", reviewTaskCount)
	}
}

func TestReviewDrain_DoesNotRedispatchCompletedCandidate(t *testing.T) {
	f := newDrainFixture(t)
	ctx := context.Background()
	issueID, candidateID := f.seedDrainIssue(t, ctx, "completed", 1)

	cell := NewReviewCellService(f.queries, f.pool, nil, ReviewCellConfig{
		Enabled:             true,
		ReviewerAgentID:     f.reviewer,
		ReviewerAgentIDSet:  true,
		CoordinatorAgentID:  f.implementer,
		CoordinatorAgentSet: true,
	})
	drain := NewReviewDrainService(f.queries, cell)
	if _, err := drain.ClassifyInReview(ctx, f.workspaceID); err != nil {
		t.Fatalf("first ClassifyInReview: %v", err)
	}
	if receipt, err := drain.DrainBatch(ctx, f.workspaceID, 1); err != nil {
		t.Fatalf("first DrainBatch: %v", err)
	} else if receipt.ReviewTasks != 1 {
		t.Fatalf("first drain receipt = %+v, want one review task", receipt)
	}

	var reviewTaskID pgtype.UUID
	if err := f.pool.QueryRow(ctx,
		`SELECT id FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review' AND review_target_task_id = $2`,
		issueID, candidateID).Scan(&reviewTaskID); err != nil {
		t.Fatalf("load review task: %v", err)
	}
	var progressTaskID pgtype.UUID
	if err := f.pool.QueryRow(ctx,
		`SELECT review_task_id FROM review_drain_progress WHERE issue_id = $1`, issueID).Scan(&progressTaskID); err != nil {
		t.Fatalf("load persisted review task id: %v", err)
	}
	if !uuidEqual(progressTaskID, reviewTaskID) {
		t.Fatalf("persisted review task id = %s, want %s", uuidString(progressTaskID), uuidString(reviewTaskID))
	}
	// A row written by the pre-fix drain may have no task binding. Replaying
	// that row must recover the existing task id without counting a new task.
	if _, err := f.pool.Exec(ctx,
		`UPDATE review_drain_progress SET review_task_id = NULL, status = 'pending', reason = '' WHERE issue_id = $1`,
		issueID); err != nil {
		t.Fatalf("simulate legacy unbound progress: %v", err)
	}
	if receipt, err := drain.DrainBatch(ctx, f.workspaceID, 1); err != nil {
		t.Fatalf("replay unbound DrainBatch: %v", err)
	} else if receipt.Processed != 1 || receipt.ReviewTasks != 0 || receipt.Skipped != 0 {
		t.Fatalf("replay unbound drain receipt = %+v, want one processed replay and zero new tasks", receipt)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT review_task_id FROM review_drain_progress WHERE issue_id = $1`, issueID).Scan(&progressTaskID); err != nil {
		t.Fatalf("reload replayed review task id: %v", err)
	}
	if !uuidEqual(progressTaskID, reviewTaskID) {
		t.Fatalf("replayed review task id = %s, want %s", uuidString(progressTaskID), uuidString(reviewTaskID))
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE agent_task_queue SET status = 'completed', completed_at = now(), result = '{}'::jsonb WHERE id = $1`,
		reviewTaskID); err != nil {
		t.Fatalf("complete review task: %v", err)
	}
	// Review comments also carry source_task_id. They must never become the
	// next implementation candidate and trigger a review-of-review chain.
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, source_task_id)
		 VALUES ($1, $2, 'agent', $3, 'review decision without verdict', 'comment', $4)`,
		issueID, f.workspaceID, f.reviewer, reviewTaskID); err != nil {
		t.Fatalf("record review comment: %v", err)
	}
	repairTaskID, err := insertReturningUUID(ctx, f.pool,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, task_kind, originator_source)
		 VALUES ($1, $2, $3, 'completed', 'repair', 'unattributed') RETURNING id`,
		f.implementer, f.implRT, issueID)
	if err != nil {
		t.Fatalf("seed repair task: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE agent_task_queue SET completed_at = now() + interval '2 seconds' WHERE id = $1`, repairTaskID); err != nil {
		t.Fatalf("complete repair task: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, source_task_id)
		 VALUES ($1, $2, 'agent', $3, 'repair delivery comment', 'comment', $4)`,
		issueID, f.workspaceID, f.implementer, repairTaskID); err != nil {
		t.Fatalf("record repair comment: %v", err)
	}

	// The drain reclassifies legacy in_review rows on every tick. The completed
	// review is historical evidence for the same candidate and must not fan out
	// another task.
	if _, err := drain.ClassifyInReview(ctx, f.workspaceID); err != nil {
		t.Fatalf("second ClassifyInReview: %v", err)
	}
	progress, err := f.queries.GetDrainProgressForIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("load second classification progress: %v", err)
	}
	if progress.Status != "skipped" || progress.Reason != DrainReasonReviewVerdictMissing {
		t.Fatalf("second classification progress = status %q reason %q, want observable missing verdict", progress.Status, progress.Reason)
	}
	if !uuidEqual(progress.ReviewTaskID, reviewTaskID) {
		t.Fatalf("second classification review task id = %s, want %s", uuidString(progress.ReviewTaskID), uuidString(reviewTaskID))
	}
	if receipt, err := drain.DrainBatch(ctx, f.workspaceID, 1); err != nil {
		t.Fatalf("second DrainBatch: %v", err)
	} else if receipt.Processed != 0 || receipt.Skipped != 0 || receipt.ReviewTasks != 0 {
		t.Fatalf("second drain receipt = %+v, want no replay after missing verdict disposition", receipt)
	}

	var count int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, issueID).Scan(&count); err != nil {
		t.Fatalf("count review tasks: %v", err)
	}
	if count != 1 {
		t.Fatalf("review task count = %d, want one historical task", count)
	}

	// A new delivery lineage is a new review round even when the previous
	// candidate already has a completed review task.
	newCandidateID, err := insertReturningUUID(ctx, f.pool,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, task_kind, originator_source)
		 VALUES ($1, $2, $3, 'completed', 'work', 'unattributed') RETURNING id`,
		f.implementer, f.implRT, issueID)
	if err != nil {
		t.Fatalf("seed new candidate: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE agent_task_queue SET completed_at = now() WHERE id = $1`, newCandidateID); err != nil {
		t.Fatalf("complete new candidate: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, source_task_id)
		 VALUES ($1, $2, 'agent', $3, 'new delivery', 'comment', $4)`,
		issueID, f.workspaceID, f.implementer, newCandidateID); err != nil {
		t.Fatalf("record new delivery: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE comment SET created_at = now() + interval '1 second' WHERE issue_id = $1 AND source_task_id = $2`,
		issueID, newCandidateID); err != nil {
		t.Fatalf("order new delivery: %v", err)
	}
	if _, err := drain.ClassifyInReview(ctx, f.workspaceID); err != nil {
		t.Fatalf("third ClassifyInReview: %v", err)
	}
	if receipt, err := drain.DrainBatch(ctx, f.workspaceID, 1); err != nil {
		t.Fatalf("third DrainBatch: %v", err)
	} else if receipt.Processed != 1 {
		t.Fatalf("third drain receipt = %+v, want new candidate processed", receipt)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, issueID).Scan(&count); err != nil {
		t.Fatalf("recount review tasks: %v", err)
	}
	if count != 2 {
		t.Fatalf("review task count after new candidate = %d, want two rounds", count)
	}
}

func TestReviewDrain_DoesNotCreateReviewForClosedProject(t *testing.T) {
	f := newDrainFixture(t)
	ctx := context.Background()
	issueID, _ := f.seedDrainIssue(t, ctx, "completed", 1)
	projectID, err := insertReturningUUID(ctx, f.pool,
		`INSERT INTO project (workspace_id, title, status) VALUES ($1, $2, 'completed') RETURNING id`,
		f.workspaceID, "closed project "+uuid.NewString())
	if err != nil {
		t.Fatalf("seed closed project: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE issue SET project_id = $1 WHERE id = $2`, projectID, issueID); err != nil {
		t.Fatalf("link issue to closed project: %v", err)
	}

	cell := NewReviewCellService(f.queries, f.pool, nil, ReviewCellConfig{
		Enabled:             true,
		ReviewerAgentID:     f.reviewer,
		ReviewerAgentIDSet:  true,
		CoordinatorAgentID:  f.implementer,
		CoordinatorAgentSet: true,
	})
	drain := NewReviewDrainService(f.queries, cell)
	if _, err := drain.ClassifyInReview(ctx, f.workspaceID); err != nil {
		t.Fatalf("ClassifyInReview: %v", err)
	}
	if _, err := drain.DrainBatch(ctx, f.workspaceID, 1); err != nil {
		t.Fatalf("DrainBatch: %v", err)
	}

	var count int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, issueID).Scan(&count); err != nil {
		t.Fatalf("count review tasks: %v", err)
	}
	if count != 0 {
		t.Fatalf("review task count for closed project = %d, want zero", count)
	}
}
