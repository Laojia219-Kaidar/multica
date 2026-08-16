package service

import (
	"context"
	"encoding/json"
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
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
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

func TestReviewCell_AuthorityDispatchOnlyQueuesWithoutCreatingReviewTask(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	cfg := cfgWithReviewerAndCoordinator(f)
	cfg.AuthorityDispatchOnly = true
	svc := newReviewCellServiceForFixture(f, cfg)

	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview replay: %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if issue.ReviewState.String != ReviewStateQueued {
		t.Fatalf("review_state = %q, want queued", issue.ReviewState.String)
	}
	if _, err := f.queries.GetOpenReviewTaskForIssue(ctx, f.issueID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetOpenReviewTaskForIssue error = %v, want no local review task", err)
	}
	var reviewTaskCount int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, f.issueID).Scan(&reviewTaskCount); err != nil {
		t.Fatalf("count review tasks: %v", err)
	}
	if reviewTaskCount != 0 {
		t.Fatalf("review task count = %d, want 0", reviewTaskCount)
	}
}

func TestReviewCell_AuthorityDispatchOnlyStillFailsClosedForMissingLineage(t *testing.T) {
	f := newReviewCellFixture(t, false)
	ctx := context.Background()
	cfg := cfgWithReviewerAndCoordinator(f)
	cfg.AuthorityDispatchOnly = true
	svc := newReviewCellServiceForFixture(f, cfg)

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

func TestReviewCell_StaleEnterDoesNotQueueClosedIssue(t *testing.T) {
	for _, status := range []string{"done", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newReviewCellFixture(t, true)
			ctx := context.Background()
			if _, err := f.pool.Exec(ctx, `UPDATE issue SET status = $2 WHERE id = $1`, f.issueID, status); err != nil {
				t.Fatalf("set issue status: %v", err)
			}
			svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))
			if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
				t.Fatalf("stale OnIssueEnteredReview: %v", err)
			}
			issue := mustGetIssue(t, ctx, f)
			if issue.ReviewState.Valid {
				t.Fatalf("review_state = %#v, want NULL", issue.ReviewState)
			}
			var reviewTaskCount int64
			if err := f.pool.QueryRow(ctx,
				`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, f.issueID).Scan(&reviewTaskCount); err != nil {
				t.Fatalf("count review tasks: %v", err)
			}
			if reviewTaskCount != 0 {
				t.Fatalf("review task count = %d, want 0", reviewTaskCount)
			}
		})
	}
}

func TestReviewCell_StaleLeaveDoesNotClearCurrentReview(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	if err := svc.OnIssueLeftReview(ctx, f.issueID); err != nil {
		t.Fatalf("stale OnIssueLeftReview: %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if issue.Status != "in_review" || !issue.ReviewState.Valid || issue.ReviewState.String != ReviewStateQueued {
		t.Fatalf("current review after stale leave = status %q state %#v, want in_review/queued", issue.Status, issue.ReviewState)
	}
	if task := mustOpenReviewTask(t, ctx, f); task.TaskKind != TaskKindReview {
		t.Fatalf("open task kind = %q, want review", task.TaskKind)
	}
}

func TestReviewCell_AuthorityCASNonQueuedStateDoesNotReplay(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE issue SET review_state = 'owner_decision' WHERE id = $1`, f.issueID); err != nil {
		t.Fatalf("set owner_decision state: %v", err)
	}
	cfg := cfgWithReviewerAndCoordinator(f)
	cfg.AuthorityDispatchOnly = true
	svc := newReviewCellServiceForFixture(f, cfg)
	err := svc.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, f.issueID)
		if err != nil {
			return err
		}
		issue.ReviewState = pgtype.Text{}
		_, err = svc.handleFreshEntry(ctx, qtx, issue)
		return err
	})
	if !errors.Is(err, ErrAuthorityReviewStateTransition) {
		t.Fatalf("fresh CAS error = %v, want ErrAuthorityReviewStateTransition", err)
	}
	err = svc.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, f.issueID)
		if err != nil {
			return err
		}
		issue.ReviewState = pgtype.Text{String: ReviewStateQueued, Valid: true}
		_, err = svc.handleReentry(ctx, qtx, issue)
		return err
	})
	if !errors.Is(err, ErrAuthorityReviewStateTransition) {
		t.Fatalf("re-entry CAS error = %v, want ErrAuthorityReviewStateTransition", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE issue SET review_state = 'queued' WHERE id = $1`, f.issueID); err != nil {
		t.Fatalf("set queued state: %v", err)
	}
	var replay ReviewTaskEnsureResult
	err = svc.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, f.issueID)
		if err != nil {
			return err
		}
		issue.ReviewState = pgtype.Text{}
		replay, err = svc.handleFreshEntry(ctx, qtx, issue)
		return err
	})
	if err != nil || !replay.Replayed {
		t.Fatalf("queued/in_review CAS replay = result=%+v error=%v, want replay success", replay, err)
	}
}

func TestReviewCell_AuthorityDispatchOnlyKeepsReviseRequestedAfterRepairWithoutCreatingRereviewTask(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	legacy := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))
	if err := legacy.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("legacy OnIssueEnteredReview: %v", err)
	}
	verdict, err := legacy.WriteVerdict(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.reviewer}, VerdictInput{
		Verdict: "revise", Notes: "needs rework",
	})
	if err != nil {
		t.Fatalf("legacy WriteVerdict(revise): %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, verdict.RepairTaskID); err != nil {
		t.Fatalf("complete repair task: %v", err)
	}
	baseIdentity := continuousdispatch.DispatchIdentity{
		WorkspaceID: util.UUIDToString(f.workspaceID), IssueID: util.UUIDToString(f.issueID),
		Stage: "implementation", CandidateRevision: "candidate-authority-base", Generation: "1",
	}
	baseContext, err := json.Marshal(shadowTaskContext{ContinuousDispatch: baseIdentity})
	if err != nil {
		t.Fatalf("encode base identity: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE issue SET metadata = $2::jsonb WHERE id = $1`, f.issueID,
		`{"stage":"review","candidate_revision":"candidate-authority-base","generation":"1"}`); err != nil {
		t.Fatalf("stamp authority base metadata: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET context = $2::jsonb WHERE id = $1`, f.candidate.ID, baseContext); err != nil {
		t.Fatalf("stamp authority base task: %v", err)
	}
	repairMarker := repairCandidatePayload{
		RepairTaskID: util.UUIDToString(verdict.RepairTaskID), BaseTaskID: util.UUIDToString(f.candidate.ID),
		BaseCandidateRevision: "candidate-authority-base", BaseGeneration: "1",
		CandidateRevision: "candidate-authority-repaired", Generation: "2",
	}
	repairContext, err := json.Marshal(repairTaskPayload{
		Kind: TaskKindRepair, CandidateTaskID: util.UUIDToString(f.candidate.ID), ReviewTaskID: util.UUIDToString(verdict.ReviewTaskID),
	})
	if err != nil {
		t.Fatalf("encode repair context: %v", err)
	}
	repairResult, err := json.Marshal(repairCandidateRuntimeResult{Output: "repair evidence\n" + repairCandidateMarkerLine(repairMarker)})
	if err != nil {
		t.Fatalf("encode repair result: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET context = $2::jsonb, result = $3::jsonb WHERE id = $1`, verdict.RepairTaskID, repairContext, repairResult); err != nil {
		t.Fatalf("stamp authority repair evidence: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, source_task_id) VALUES ($1, $2, 'agent', $3, $4, 'comment', $5)`, f.issueID, f.workspaceID, f.implementer, repairCandidateMarkerLine(repairMarker), verdict.RepairTaskID); err != nil {
		t.Fatalf("seed authority repair source comment: %v", err)
	}

	cfg := cfgWithReviewerAndCoordinator(f)
	cfg.AuthorityDispatchOnly = true
	authorityOnly := newReviewCellServiceForFixture(f, cfg)
	if err := authorityOnly.OnRepairTaskCompleted(ctx, verdict.RepairTaskID); err != nil {
		t.Fatalf("authority-only OnRepairTaskCompleted: %v", err)
	}
	stampedRepair, err := f.queries.GetAgentTask(ctx, verdict.RepairTaskID)
	if err != nil {
		t.Fatalf("read stamped repair task: %v", err)
	}
	var stampedRepairContext shadowTaskContext
	if err := json.Unmarshal(stampedRepair.Context, &stampedRepairContext); err != nil ||
		!repairCandidateDispatchIdentityMatchesIssue(stampedRepairContext.ContinuousDispatch, mustGetIssue(t, ctx, f), "implementation", repairMarker.CandidateRevision, repairMarker.Generation) {
		t.Fatalf("fresh authority completion did not stamp exact repair identity: context=%s err=%v", stampedRepair.Context, err)
	}
	if err := authorityOnly.OnRepairTaskCompleted(ctx, verdict.RepairTaskID); err != nil {
		t.Fatalf("authority-only OnRepairTaskCompleted replay: %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if issue.ReviewState.String != ReviewStateReviseRequested {
		t.Fatalf("review_state after repair = %q, want revise_requested", issue.ReviewState.String)
	}
	if _, err := f.queries.GetOpenReviewTaskForIssue(ctx, f.issueID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetOpenReviewTaskForIssue after repair error = %v, want no local re-review task", err)
	}
	var reviewTaskCount int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, f.issueID).Scan(&reviewTaskCount); err != nil {
		t.Fatalf("count review tasks after repair: %v", err)
	}
	if reviewTaskCount != 1 {
		t.Fatalf("review task count after repair = %d, want historical task only", reviewTaskCount)
	}
}

func TestReviewCell_AuthorityRequeueClosedIssueDoesNotMutate(t *testing.T) {
	for _, status := range []string{"done", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newReviewCellFixture(t, false)
			ctx := context.Background()
			cfg := cfgWithReviewerAndCoordinator(f)
			cfg.AuthorityDispatchOnly = true
			svc := newReviewCellServiceForFixture(f, cfg)
			if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
				t.Fatalf("OnIssueEnteredReview: %v", err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE issue SET status = $2 WHERE id = $1`, f.issueID, status); err != nil {
				t.Fatalf("set issue status: %v", err)
			}
			res, err := svc.Requeue(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.coordinator})
			if !errors.Is(err, ErrReviewIssueNotInReview) {
				t.Fatalf("Requeue error = %v, want ErrReviewIssueNotInReview", err)
			}
			if res.ReviewState != ReviewStateOwnerDecision {
				t.Fatalf("Requeue result state = %q, want owner_decision", res.ReviewState)
			}
			issue := mustGetIssue(t, ctx, f)
			if issue.Status != status || !issue.ReviewState.Valid || issue.ReviewState.String != ReviewStateOwnerDecision {
				t.Fatalf("closed issue after Requeue = status %q state %#v, want %s/owner_decision", issue.Status, issue.ReviewState, status)
			}
			var reviewTaskCount int64
			if err := f.pool.QueryRow(ctx,
				`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, f.issueID).Scan(&reviewTaskCount); err != nil {
				t.Fatalf("count review tasks: %v", err)
			}
			if reviewTaskCount != 0 {
				t.Fatalf("review task count = %d, want 0", reviewTaskCount)
			}
		})
	}
}

func TestReviewCell_AuthorityRepairCompletionFailsClosedOnStateDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update string
	}{
		{name: "delivery status drift", update: `UPDATE issue SET status = 'in_progress' WHERE id = $1`},
		{name: "review state drift", update: `UPDATE issue SET review_state = 'queued' WHERE id = $1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviewCellFixture(t, true)
			ctx := context.Background()
			cfg := cfgWithReviewerAndCoordinator(f)
			cfg.AuthorityDispatchOnly = true
			svc := newReviewCellServiceForFixture(f, cfg)
			if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
				t.Fatalf("OnIssueEnteredReview: %v", err)
			}
			verdict, err := svc.WriteVerdict(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.reviewer}, VerdictInput{
				Verdict:            "revise",
				Notes:              "repair required",
				RepairRequirements: []string{"preserve the review state"},
			})
			if err != nil {
				t.Fatalf("WriteVerdict(revise): %v", err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, verdict.RepairTaskID); err != nil {
				t.Fatalf("complete repair task: %v", err)
			}
			if _, err := f.pool.Exec(ctx, tc.update, f.issueID); err != nil {
				t.Fatalf("create state drift: %v", err)
			}
			if err := svc.OnRepairTaskCompleted(ctx, verdict.RepairTaskID); !errors.Is(err, ErrAuthorityRepairStateDrift) {
				t.Fatalf("OnRepairTaskCompleted error = %v, want ErrAuthorityRepairStateDrift", err)
			}
		})
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

	var count, openCount int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'`, f.issueID).Scan(&count); err != nil {
		t.Fatalf("count review tasks: %v", err)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review' AND status IN ('queued','dispatched','running','waiting_local_directory')`, f.issueID).Scan(&openCount); err != nil {
		t.Fatalf("count open review tasks: %v", err)
	}
	// A completed review without a canonical verdict is invalid and may be
	// retried. All concurrent re-entry calls must collapse into exactly one
	// fresh open task alongside the single historical malformed completion.
	if count != 2 || openCount != 1 {
		t.Fatalf("review tasks after concurrent malformed reentry = total %d open %d, want 2/1", count, openCount)
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

func completeRuntimeReviewTask(t *testing.T, ctx context.Context, f reviewCellFixture, taskID pgtype.UUID, output string) {
	t.Helper()
	result := completedReviewResult(t, output)
	if _, err := f.pool.Exec(ctx,
		`UPDATE agent_task_queue SET status = 'completed', completed_at = now(), result = $2::jsonb WHERE id = $1`,
		taskID, result); err != nil {
		t.Fatalf("complete runtime review task: %v", err)
	}
}

func TestReviewCell_CompletedReviewReviseRepairReReview(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))

	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	reviewTask := mustOpenReviewTask(t, ctx, f)
	if !reviewTask.HandoffNote.Valid || !strings.Contains(reviewTask.HandoffNote.String, completedReviewVerdictMarkerV1) {
		t.Fatalf("review handoff note does not carry strict verdict contract: %#v", reviewTask.HandoffNote)
	}
	output := "review evidence\n" + completedReviewVerdictMarkerV1 + ` {"verdict":"revise","notes":"missing regression coverage","repair_requirements":["add the regression test"]}`
	completeRuntimeReviewTask(t, ctx, f, reviewTask.ID, output)

	if err := svc.OnReviewTaskCompleted(ctx, reviewTask.ID); err != nil {
		t.Fatalf("OnReviewTaskCompleted: %v", err)
	}
	if err := svc.OnReviewTaskCompleted(ctx, reviewTask.ID); err != nil {
		t.Fatalf("OnReviewTaskCompleted replay: %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if !issue.ReviewState.Valid || issue.ReviewState.String != ReviewStateReviseRequested {
		t.Fatalf("review_state = %#v, want revise_requested", issue.ReviewState)
	}

	completedReview, err := f.queries.GetAgentTask(ctx, reviewTask.ID)
	if err != nil {
		t.Fatalf("load completed review: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(completedReview.Result, &result); err != nil {
		t.Fatalf("decode completed review result: %v", err)
	}
	if result["output"] != output || result["verdict"] != "revise" || result["verdict_contract"] != completedReviewVerdictMarkerV1 {
		t.Fatalf("completed review result is not a structured, output-preserving verdict: %#v", result)
	}

	var verdictComments, repairTasks int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id = $1 AND source_task_id = $2`, f.issueID, reviewTask.ID).Scan(&verdictComments); err != nil {
		t.Fatalf("count verdict comments: %v", err)
	}
	if verdictComments != 1 {
		t.Fatalf("verdict comments = %d, want 1", verdictComments)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'repair' AND trigger_evidence_ref_id = $2`, f.issueID, reviewTask.ID).Scan(&repairTasks); err != nil {
		t.Fatalf("count repair tasks: %v", err)
	}
	if repairTasks != 1 {
		t.Fatalf("repair tasks = %d, want 1", repairTasks)
	}
	repairTask, err := f.queries.GetRepairTaskByEvidence(ctx, db.GetRepairTaskByEvidenceParams{IssueID: f.issueID, TriggerEvidenceRefID: reviewTask.ID})
	if err != nil {
		t.Fatalf("load repair task: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now(), result = '{"output":"repaired"}'::jsonb WHERE id = $1`, repairTask.ID); err != nil {
		t.Fatalf("complete repair task: %v", err)
	}
	if err := svc.OnRepairTaskCompleted(ctx, repairTask.ID); err != nil {
		t.Fatalf("OnRepairTaskCompleted: %v", err)
	}
	if err := svc.OnRepairTaskCompleted(ctx, repairTask.ID); err != nil {
		t.Fatalf("OnRepairTaskCompleted replay: %v", err)
	}
	reReviewTask := mustOpenReviewTask(t, ctx, f)
	if uuidEqual(reReviewTask.ID, reviewTask.ID) || !uuidEqual(reReviewTask.ReviewTargetTaskID, repairTask.ID) {
		t.Fatalf("fresh re-review lineage = task %s target %s, want new task targeting repair %s",
			uuidString(reReviewTask.ID), uuidString(reReviewTask.ReviewTargetTaskID), uuidString(repairTask.ID))
	}
	if !reReviewTask.HandoffNote.Valid || !strings.Contains(reReviewTask.HandoffNote.String, uuidString(repairTask.ID)) {
		t.Fatalf("re-review handoff note does not identify repaired candidate: %#v", reReviewTask.HandoffNote)
	}
}

func TestReviewCell_CompletedReviewMalformedFailsClosedAndCanRequeue(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	svc := newReviewCellServiceForFixture(f, cfgWithReviewerAndCoordinator(f))
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	reviewTask := mustOpenReviewTask(t, ctx, f)
	completeRuntimeReviewTask(t, ctx, f, reviewTask.ID, "REVISE in unstructured prose")
	if err := svc.OnReviewTaskCompleted(ctx, reviewTask.ID); err != nil {
		t.Fatalf("OnReviewTaskCompleted malformed: %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if issue.ReviewState.String != ReviewStateOwnerDecision || !strings.Contains(issue.ReviewStateReason.String, ReviewVerdictFailureMissingMarker) {
		t.Fatalf("malformed review did not fail closed: state=%#v reason=%#v", issue.ReviewState, issue.ReviewStateReason)
	}
	var repairTasks int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'repair'`, f.issueID).Scan(&repairTasks); err != nil {
		t.Fatalf("count repair tasks: %v", err)
	}
	if repairTasks != 0 {
		t.Fatalf("malformed review created %d repair tasks, want 0", repairTasks)
	}

	res, err := svc.Requeue(ctx, f.issueID, ReviewActor{ActorType: "agent", ActorID: f.coordinator})
	if err != nil {
		t.Fatalf("Requeue malformed review: %v", err)
	}
	if !res.ReviewTaskCreated || res.ReviewState != ReviewStateQueued {
		t.Fatalf("requeue result = %#v, want fresh queued review", res)
	}
	newReview := mustOpenReviewTask(t, ctx, f)
	if uuidEqual(newReview.ID, reviewTask.ID) || !uuidEqual(newReview.ReviewTargetTaskID, f.candidate.ID) {
		t.Fatalf("requeue did not create a fresh review for the same exact candidate")
	}
}

func TestReviewCell_CompletedReviewPassRequiresCoordinator(t *testing.T) {
	f := newReviewCellFixture(t, true)
	ctx := context.Background()
	cfg := cfgWithReviewerAndCoordinator(f)
	cfg.ReviewerAgentID = f.coordinator
	svc := newReviewCellServiceForFixture(f, cfg)
	if err := svc.OnIssueEnteredReview(ctx, f.issueID); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	reviewTask := mustOpenReviewTask(t, ctx, f)
	if !uuidEqual(reviewTask.AgentID, f.coordinator) {
		t.Fatalf("review task agent = %s, want coordinator", uuidString(reviewTask.AgentID))
	}
	output := completedReviewVerdictMarkerV1 + ` {"verdict":"pass","notes":"all checks passed","repair_requirements":[]}`
	completeRuntimeReviewTask(t, ctx, f, reviewTask.ID, output)
	if err := svc.OnReviewTaskCompleted(ctx, reviewTask.ID); err != nil {
		t.Fatalf("OnReviewTaskCompleted pass: %v", err)
	}
	if err := svc.OnReviewTaskCompleted(ctx, reviewTask.ID); err != nil {
		t.Fatalf("OnReviewTaskCompleted pass replay: %v", err)
	}
	issue := mustGetIssue(t, ctx, f)
	if issue.Status != "done" || issue.ReviewState.Valid {
		t.Fatalf("accepted issue = status %q review_state %#v, want done with cleared state", issue.Status, issue.ReviewState)
	}
	var verdictComments, repairTasks int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id = $1 AND source_task_id = $2`, f.issueID, reviewTask.ID).Scan(&verdictComments); err != nil {
		t.Fatalf("count verdict comments: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'repair'`, f.issueID).Scan(&repairTasks); err != nil {
		t.Fatalf("count repair tasks: %v", err)
	}
	if verdictComments != 1 || repairTasks != 0 {
		t.Fatalf("pass replay side effects: comments=%d repairs=%d, want 1/0", verdictComments, repairTasks)
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
