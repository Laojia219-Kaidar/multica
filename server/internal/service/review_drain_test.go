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
