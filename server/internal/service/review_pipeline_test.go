package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// newReviewPipelineTestPool requires the isolated worktree database on
// 127.0.0.1:55432 and refuses every other target (no localhost:5432 default).
func newReviewPipelineTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; review pipeline integration test skipped")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if parsed.Port() == "5432" {
		t.Skip("refusing to connect review pipeline test to port 5432")
	}
	if parsed.Port() != "55432" {
		t.Skipf("review pipeline test requires isolated worktree port 55432, got %q", parsed.Port())
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		t.Skipf("review pipeline test requires a loopback database host, got %q", host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// reviewPipelineFixture is the minimal ReviewPipelineV2 environment:
// workspace, human owner, runtime, implementer agent (issue assignee + candidate
// implementer), reviewer agent (L1) and coordinator agent (Codex).
type reviewPipelineFixture struct {
	pool               *pgxpool.Pool
	workspaceID        string
	userID             string
	runtimeID          string
	implementerAgentID string
	reviewerAgentID    string
	coordinatorAgentID string
	issueID            string
	implementerAgent   db.Agent
	reviewerAgent      db.Agent
	coordinatorAgent   db.Agent
}

// seedReviewPipelineFixture creates the fixture rows. Every row is registered
// for cleanup on t.
func seedReviewPipelineFixture(t *testing.T, pool *pgxpool.Pool) reviewPipelineFixture {
	t.Helper()
	ctx := context.Background()

	var fixture reviewPipelineFixture
	fixture.pool = pool

	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('Review Pipeline User', 'review-pipeline-' || gen_random_uuid() || '@multica.test')
		RETURNING id::text
	`).Scan(&fixture.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, fixture.userID) })

	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug)
		VALUES ('review-pipeline-ws', 'review-pipeline-ws-' || gen_random_uuid())
		RETURNING id::text
	`).Scan(&fixture.workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, fixture.workspaceID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, fixture.workspaceID, fixture.userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id
		) VALUES ($1, 'review-runtime', 'cloud', 'codex', 'online', '', '{}'::jsonb, $2)
		RETURNING id::text
	`, fixture.workspaceID, fixture.userID).Scan(&fixture.runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, fixture.runtimeID)
	})

	seedAgent := func(name string) db.Agent {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, runtime_mode, runtime_config,
				runtime_id, visibility, max_concurrent_tasks, owner_id,
				instructions, custom_env, custom_args
			)
			VALUES ($1, $2, 'cloud', '{}'::jsonb,
			        $3, 'workspace', 4, $4, '', '{}'::jsonb, '[]'::jsonb)
			RETURNING id::text
		`, fixture.workspaceID, name, fixture.runtimeID, fixture.userID).Scan(&id); err != nil {
			t.Fatalf("seed agent %s: %v", name, err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, id) })
		agent, err := db.New(pool).GetAgent(ctx, util.MustParseUUID(id))
		if err != nil {
			t.Fatalf("load agent %s: %v", name, err)
		}
		return agent
	}

	fixture.implementerAgent = seedAgent("review-implementer")
	fixture.implementerAgentID = util.UUIDToString(fixture.implementerAgent.ID)
	fixture.reviewerAgent = seedAgent("review-quinn")
	fixture.reviewerAgentID = util.UUIDToString(fixture.reviewerAgent.ID)
	fixture.coordinatorAgent = seedAgent("review-codex")
	fixture.coordinatorAgentID = util.UUIDToString(fixture.coordinatorAgent.ID)

	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, assignee_type, assignee_id)
		VALUES ($1, 'review-pipeline-issue', 'in_review', 'member', $2, 'agent', $3)
		RETURNING id::text
	`, fixture.workspaceID, fixture.userID, fixture.implementerAgentID).Scan(&fixture.issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, fixture.issueID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, fixture.issueID)
	})

	return fixture
}

// seedCompletedCandidateTask inserts the implementer's completed delivery task
// plus the Task-linked delivery comment that pins it (C4-L).
func (f reviewPipelineFixture) seedCompletedCandidateTask(t *testing.T, ctx context.Context) string {
	t.Helper()
	var taskID string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, completed_at,
			originator_user_id, accountable_user_id
		)
		VALUES ($1, $2, $3, 'completed', 0, now(), $4, $4)
		RETURNING id::text
	`, f.implementerAgentID, f.runtimeID, f.issueID, f.userID).Scan(&taskID); err != nil {
		t.Fatalf("seed candidate task: %v", err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	if _, err := f.pool.Exec(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, source_task_id)
		VALUES ($1, $2, 'agent', $3, 'delivery receipt', $4)
	`, f.issueID, f.workspaceID, f.implementerAgentID, taskID); err != nil {
		t.Fatalf("seed delivery comment: %v", err)
	}
	return taskID
}

func (f reviewPipelineFixture) seedDeliveryComment(t *testing.T, ctx context.Context, taskID *string) string {
	t.Helper()
	var id string
	if taskID != nil {
		if err := f.pool.QueryRow(ctx, `
			INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, source_task_id)
			VALUES ($1, $2, 'agent', $3, 'delivery receipt', $4)
			RETURNING id::text
		`, f.issueID, f.workspaceID, f.implementerAgentID, *taskID).Scan(&id); err != nil {
			t.Fatalf("seed delivery comment: %v", err)
		}
	} else {
		if err := f.pool.QueryRow(ctx, `
			INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content)
			VALUES ($1, $2, 'agent', $3, 'delivery without lineage')
			RETURNING id::text
		`, f.issueID, f.workspaceID, f.implementerAgentID).Scan(&id); err != nil {
			t.Fatalf("seed lineage-less delivery comment: %v", err)
		}
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM comment WHERE id = $1`, id) })
	return id
}

func (f reviewPipelineFixture) reviewService(overrides func(*ReviewPipelineConfig)) *ReviewPipelineService {
	cfg := ReviewPipelineConfig{
		Enabled:             true,
		ReviewerAgentID:     util.MustParseUUID(f.reviewerAgentID),
		ReviewerAgentIDSet:  true,
		CoordinatorAgentID:  util.MustParseUUID(f.coordinatorAgentID),
		CoordinatorAgentSet: true,
		ReviewWIPLimit:      10,
		ReviewPriority:      5,
	}
	if overrides != nil {
		overrides(&cfg)
	}
	return NewReviewPipelineService(db.New(f.pool), f.pool, events.New(), cfg)
}

func (f reviewPipelineFixture) issueReviewState(t *testing.T, ctx context.Context) (pgtype.Text, pgtype.Text) {
	t.Helper()
	var state, reason pgtype.Text
	if err := f.pool.QueryRow(ctx, `
		SELECT review_state, review_state_reason FROM issue WHERE id = $1
	`, f.issueID).Scan(&state, &reason); err != nil {
		t.Fatalf("load issue review state: %v", err)
	}
	return state, reason
}

// withNewIssue clones the fixture onto a fresh issue in the SAME workspace, so
// several issues can share one workspace (backfill dry-run is workspace-scoped).
func (f reviewPipelineFixture) withNewIssue(t *testing.T) reviewPipelineFixture {
	t.Helper()
	ctx := context.Background()
	var issueID string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, assignee_type, assignee_id, number)
		VALUES ($1, 'review-pipeline-issue', 'in_review', 'member', $2, 'agent', $3,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id::text
	`, f.workspaceID, f.userID, f.implementerAgentID).Scan(&issueID); err != nil {
		t.Fatalf("seed sibling issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	clone := f
	clone.issueID = issueID
	return clone
}

func (f reviewPipelineFixture) openReviewTaskCount(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND task_kind = 'review'
		  AND status IN ('queued','dispatched','running','waiting_local_directory')
	`, f.issueID).Scan(&n); err != nil {
		t.Fatalf("count open review tasks: %v", err)
	}
	return n
}

func (f reviewPipelineFixture) currentReviewTask(t *testing.T, ctx context.Context) db.AgentTaskQueue {
	t.Helper()
	task, err := db.New(f.pool).GetOpenReviewTaskForIssue(ctx, util.MustParseUUID(f.issueID))
	if err != nil {
		t.Fatalf("load open review task: %v", err)
	}
	return task
}

func TestReviewOnEnteredReview_ValidLineage_CreatesReviewTask(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	candidateID := fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)

	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}

	state, reason := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateQueued {
		t.Fatalf("review_state = %v, want queued", state)
	}
	if reason.Valid {
		t.Fatalf("review_state_reason = %q, want empty for queued", reason.String)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks = %d, want exactly 1", n)
	}
	task := fixture.currentReviewTask(t, ctx)
	if !uuidEqual(task.ReviewTargetTaskID, util.MustParseUUID(candidateID)) {
		t.Fatalf("review task targets %s, want candidate %s",
			util.UUIDToString(task.ReviewTargetTaskID), candidateID)
	}
	if !uuidEqual(task.AgentID, util.MustParseUUID(fixture.reviewerAgentID)) {
		t.Fatalf("review task assigned to %s, want reviewer %s",
			util.UUIDToString(task.AgentID), fixture.reviewerAgentID)
	}
	if task.TaskKind != TaskKindReview {
		t.Fatalf("review task task_kind = %q, want %q", task.TaskKind, TaskKindReview)
	}
}

func TestReviewOnEnteredReview_DuplicateEvent_Idempotent(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)

	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("first OnIssueEnteredReview: %v", err)
	}
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("second (duplicate) OnIssueEnteredReview: %v", err)
	}
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("third (duplicate) OnIssueEnteredReview: %v", err)
	}

	state, _ := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateQueued {
		t.Fatalf("review_state = %v, want queued", state)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks = %d, want exactly 1 after duplicate events", n)
	}
}

// TestReviewOnEnteredReview_DuplicateWhileWaitingLocalDirectory guards the
// HIV-350 gap: the daemon parks a claimed review task in
// waiting_local_directory while its workdir is prepared, and 258's unique
// index (queued/dispatched/running only) let a duplicate EventIssueUpdated
// delivery mint a SECOND open review task for the same (issue, candidate) at
// that moment. Migration 260 extends the unique key (and the CreateReviewTask
// arbiter) over waiting_local_directory, so the duplicate delivery must still
// collapse into a single open review task.
func TestReviewOnEnteredReview_DuplicateWhileWaitingLocalDirectory(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)

	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("first OnIssueEnteredReview: %v", err)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks after first event = %d, want 1", n)
	}

	// Park the review task the way the daemon does while preparing the workdir.
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue SET status = 'waiting_local_directory'
		WHERE id = $1
	`, fixture.currentReviewTask(t, ctx).ID); err != nil {
		t.Fatalf("park review task in waiting_local_directory: %v", err)
	}

	// A duplicate delivery arriving while the first task is parked must not
	// create a second open review task.
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("duplicate OnIssueEnteredReview while waiting_local_directory: %v", err)
	}
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("second duplicate OnIssueEnteredReview while waiting_local_directory: %v", err)
	}

	state, _ := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateQueued {
		t.Fatalf("review_state = %v, want queued", state)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks = %d, want exactly 1 after duplicate events while waiting_local_directory", n)
	}
	task := fixture.currentReviewTask(t, ctx)
	if task.Status != "waiting_local_directory" {
		t.Fatalf("review task status = %q, want waiting_local_directory preserved", task.Status)
	}
}

// TestReviewCreateReviewTask_IdempotentAcrossWaitingLocalDirectory drives the
// generated CreateReviewTask query directly (the ON CONFLICT arbiter path) to
// prove the unique key itself — not just the service guard — rejects a second
// open review task once the first is parked in waiting_local_directory. This
// is the regression the migration 260 index predicate exists for.
func TestReviewCreateReviewTask_IdempotentAcrossWaitingLocalDirectory(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	candidateID := fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)

	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	task := fixture.currentReviewTask(t, ctx)
	if !uuidEqual(task.ReviewTargetTaskID, util.MustParseUUID(candidateID)) {
		t.Fatalf("review task targets %s, want candidate %s",
			util.UUIDToString(task.ReviewTargetTaskID), candidateID)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue SET status = 'waiting_local_directory'
		WHERE id = $1
	`, task.ID); err != nil {
		t.Fatalf("park review task in waiting_local_directory: %v", err)
	}

	// Re-run createReviewTask with the exact same (issue, candidate): the
	// arbiter must resolve to idx_agent_task_review_open_unique_v2 and return
	// "not created" (no rows) instead of inserting a duplicate.
	issue, err := db.New(pool).GetIssue(ctx, util.MustParseUUID(fixture.issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	candidate, err := db.New(pool).GetAgentTask(ctx, util.MustParseUUID(candidateID))
	if err != nil {
		t.Fatalf("load candidate task: %v", err)
	}
	created, _, err := svc.createReviewTask(ctx, db.New(pool), issue, candidate)
	if err != nil {
		t.Fatalf("createReviewTask while waiting_local_directory: %v", err)
	}
	if created {
		t.Fatal("createReviewTask reported a new insert while the first task is waiting_local_directory — duplicate open review task created")
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks = %d, want exactly 1", n)
	}
}

func TestReviewOnEnteredReview_ConcurrentDuplicateEvents(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)

	const workers = 4
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID))
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent OnIssueEnteredReview: %v", err)
		}
	}

	state, _ := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateQueued {
		t.Fatalf("review_state = %v, want queued", state)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks = %d, want exactly 1 after concurrent events", n)
	}
}

func TestReviewOnEnteredReview_FailClosedLineage(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		setup      func(t *testing.T, issue reviewPipelineFixture)
		wantState  string
		wantReason string
	}{
		{
			name: "no_source_task_id",
			setup: func(t *testing.T, issue reviewPipelineFixture) {
				issue.seedDeliveryComment(t, ctx, nil)
			},
			wantState:  ReviewStateOwnerDecision,
			wantReason: ReviewEscalationReasonMissingLineage + "/" + LineageFailureNoSourceTaskID,
		},
		{
			name: "task_not_found",
			setup: func(t *testing.T, issue reviewPipelineFixture) {
				// A UUID that no agent_task_queue row will ever carry.
				dangling := "00000000-0000-0000-0000-00000000dead"
				issue.seedDeliveryComment(t, ctx, &dangling)
			},
			wantState:  ReviewStateOwnerDecision,
			wantReason: ReviewEscalationReasonMissingLineage + "/" + LineageFailureTaskNotFound,
		},
		{
			name: "cross_issue_reference",
			setup: func(t *testing.T, issue reviewPipelineFixture) {
				// Candidate task on a DIFFERENT issue (its own fixture).
				other := seedReviewPipelineFixture(t, pool)
				candidateID := other.seedCompletedCandidateTask(t, ctx)
				issue.seedDeliveryComment(t, ctx, &candidateID)
			},
			wantState:  ReviewStateOwnerDecision,
			wantReason: ReviewEscalationReasonMissingLineage + "/" + LineageFailureCrossIssueReference,
		},
		{
			name: "candidate_not_terminal",
			setup: func(t *testing.T, issue reviewPipelineFixture) {
				var taskID string
				if err := pool.QueryRow(ctx, `
					INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
					VALUES ($1, $2, $3, 'running', 0)
					RETURNING id::text
				`, issue.implementerAgentID, issue.runtimeID, issue.issueID).Scan(&taskID); err != nil {
					t.Fatalf("seed running candidate task: %v", err)
				}
				t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
				issue.seedDeliveryComment(t, ctx, &taskID)
			},
			wantState:  ReviewStateOwnerDecision,
			wantReason: ReviewEscalationReasonMissingLineage + "/" + LineageFailureCandidateNotTerminal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := seedReviewPipelineFixture(t, pool)
			tc.setup(t, issue)
			svc := issue.reviewService(nil)
			if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(issue.issueID)); err != nil {
				t.Fatalf("OnIssueEnteredReview: %v", err)
			}
			state, reason := issue.issueReviewState(t, ctx)
			if !state.Valid || state.String != tc.wantState {
				t.Fatalf("review_state = %v, want %q", state, tc.wantState)
			}
			if !reason.Valid || reason.String != tc.wantReason {
				t.Fatalf("review_state_reason = %v, want %q", reason, tc.wantReason)
			}
			if n := issue.openReviewTaskCount(t, ctx); n != 0 {
				t.Fatalf("fail-closed must not create review tasks, got %d", n)
			}
			// The issue row must still be queryable in the review queue as
			// owner_decision (no review task attached).
			queue, err := svc.ListReviewQueue(ctx, util.MustParseUUID(issue.workspaceID))
			if err != nil {
				t.Fatalf("ListReviewQueue: %v", err)
			}
			found := false
			for _, row := range queue {
				if uuidEqual(row.IssueID, util.MustParseUUID(issue.issueID)) {
					found = true
					if !row.ReviewState.Valid || row.ReviewState.String != ReviewStateOwnerDecision {
						t.Fatalf("queue row review_state = %v, want owner_decision", row.ReviewState)
					}
					if row.ReviewTaskID.Valid {
						t.Fatalf("queue row must not carry an open review task, got %s",
							util.UUIDToString(row.ReviewTaskID))
					}
				}
			}
			if !found {
				t.Fatal("owner_decision issue missing from review queue")
			}
		})
	}
}

func TestReviewOnEnteredReview_ReviewerIsImplementer_FailClosed(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)

	// The reviewer is the SAME agent as the implementer → creation must fail
	// closed to owner_decision without a review task (C3).
	svc := fixture.reviewService(func(cfg *ReviewPipelineConfig) {
		cfg.ReviewerAgentID = util.MustParseUUID(fixture.implementerAgentID)
	})
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	state, reason := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateOwnerDecision {
		t.Fatalf("review_state = %v, want owner_decision", state)
	}
	if !reason.Valid || reason.String != ReviewEscalationReasonMissingLineage+"/"+LineageFailureReviewerIsImplementer {
		t.Fatalf("review_state_reason = %v", reason)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 0 {
		t.Fatalf("reviewer==implementer must not create review tasks, got %d", n)
	}
}

func TestReviewVerdict_Revise_FlowsToRepair(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	candidateID := fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	reviewTask := fixture.currentReviewTask(t, ctx)

	actor := ReviewActor{ActorType: "agent", ActorID: util.MustParseUUID(fixture.reviewerAgentID)}
	result, err := svc.WriteVerdict(ctx, util.MustParseUUID(fixture.issueID), actor, VerdictInput{
		Verdict:            "revise",
		Notes:              "receipt incomplete",
		RepairRequirements: []string{"add test receipt", "pin exact commit"},
	})
	if err != nil {
		t.Fatalf("WriteVerdict(revise): %v", err)
	}
	if result.ReviewState != ReviewStateReviseRequested {
		t.Fatalf("verdict result review_state = %q, want revise_requested", result.ReviewState)
	}
	state, _ := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateReviseRequested {
		t.Fatalf("issue review_state = %v, want revise_requested (issue.status stays in_review)", state)
	}

	// Review task completed with the structured verdict receipt.
	var completedStatus string
	var resultJSON []byte
	if err := pool.QueryRow(ctx, `
		SELECT status, result FROM agent_task_queue WHERE id = $1
	`, reviewTask.ID).Scan(&completedStatus, &resultJSON); err != nil {
		t.Fatalf("load completed review task: %v", err)
	}
	if completedStatus != "completed" {
		t.Fatalf("review task status = %q, want completed", completedStatus)
	}
	var receipt map[string]any
	if err := json.Unmarshal(resultJSON, &receipt); err != nil {
		t.Fatalf("parse verdict receipt: %v", err)
	}
	if receipt["verdict"] != "revise" {
		t.Fatalf("verdict receipt verdict = %v, want revise", receipt["verdict"])
	}

	// Verdict lands as a Task-linked comment on the issue.
	var commentCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND source_task_id = $2
	`, fixture.issueID, reviewTask.ID).Scan(&commentCount); err != nil {
		t.Fatalf("count verdict comments: %v", err)
	}
	if commentCount != 1 {
		t.Fatalf("verdict comments = %d, want exactly 1 Task-linked verdict comment", commentCount)
	}

	// REVISE → repair → re-review: implementer reworks, leaves in_review
	// (reset), then re-enters with a new candidate → new review task.
	if err := svc.OnIssueLeftReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueLeftReview (repair rework): %v", err)
	}
	state, _ = fixture.issueReviewState(t, ctx)
	if state.Valid {
		t.Fatalf("review_state after leaving in_review = %v, want NULL", state)
	}

	repairCandidate := fixture.seedCompletedCandidateTask(t, ctx)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview after repair: %v", err)
	}
	state, _ = fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateQueued {
		t.Fatalf("review_state after repair re-delivery = %v, want queued", state)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks after repair = %d, want 1", n)
	}
	repairTask := fixture.currentReviewTask(t, ctx)
	if !uuidEqual(repairTask.ReviewTargetTaskID, util.MustParseUUID(repairCandidate)) {
		t.Fatalf("repair review task targets %s, want repair candidate %s",
			util.UUIDToString(repairTask.ReviewTargetTaskID), repairCandidate)
	}
	_ = candidateID
}

func TestReviewVerdict_Pass_CoordinatorOnly(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}

	// Non-coordinator reviewer cannot accept (C6).
	if _, err := svc.WriteVerdict(ctx, util.MustParseUUID(fixture.issueID),
		ReviewActor{ActorType: "agent", ActorID: util.MustParseUUID(fixture.reviewerAgentID)},
		VerdictInput{Verdict: "pass", Notes: "looks good"}); !errors.Is(err, ErrNotCoordinator) {
		t.Fatalf("pass by non-coordinator: err = %v, want ErrNotCoordinator", err)
	}

	// Coordinator can accept.
	result, err := svc.WriteVerdict(ctx, util.MustParseUUID(fixture.issueID),
		ReviewActor{ActorType: "agent", ActorID: util.MustParseUUID(fixture.coordinatorAgentID)},
		VerdictInput{Verdict: "pass", Notes: "accepted after evidence check"})
	if err != nil {
		t.Fatalf("pass by coordinator: %v", err)
	}
	if result.ReviewState != ReviewStateAccepted {
		t.Fatalf("result review_state = %q, want accepted", result.ReviewState)
	}
	state, _ := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateAccepted {
		t.Fatalf("issue review_state = %v, want accepted", state)
	}
	// accepted → status done: review_state resets, History lives in the task.
	if err := svc.OnIssueLeftReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueLeftReview after accepted: %v", err)
	}
	state, _ = fixture.issueReviewState(t, ctx)
	if state.Valid {
		t.Fatalf("review_state after accepted→done = %v, want NULL", state)
	}
}

func TestReviewVerdict_WrongReviewer_Rejected(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}

	// The implementer (or any agent that is not the assigned reviewer) is 403.
	if _, err := svc.WriteVerdict(ctx, util.MustParseUUID(fixture.issueID),
		ReviewActor{ActorType: "agent", ActorID: util.MustParseUUID(fixture.implementerAgentID)},
		VerdictInput{Verdict: "revise"}); !errors.Is(err, ErrNotAssignedReviewer) {
		t.Fatalf("verdict by non-reviewer: err = %v, want ErrNotAssignedReviewer", err)
	}
}

func TestReviewVerdict_ReviewerIsImplementer_Rejected(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}

	// Craft the impossible-in-production shape directly: review task assigned to
	// the implementer. The verdict write must still reject reviewer==implementer
	// (C3; §5-6), independent of the creation-time guard.
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue SET agent_id = $1
		WHERE issue_id = $2 AND task_kind = 'review' AND status IN ('queued','dispatched','running')
	`, fixture.implementerAgentID, fixture.issueID); err != nil {
		t.Fatalf("reassign review task to implementer: %v", err)
	}

	_, err := svc.WriteVerdict(ctx, util.MustParseUUID(fixture.issueID),
		ReviewActor{ActorType: "agent", ActorID: util.MustParseUUID(fixture.implementerAgentID)},
		VerdictInput{Verdict: "revise"})
	if !errors.Is(err, ErrReviewerIsImplementer) {
		t.Fatalf("self-review verdict: err = %v, want ErrReviewerIsImplementer", err)
	}
}

func TestReviewVerdict_NoOpenReviewTask_Rejected(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	reviewTask := fixture.currentReviewTask(t, ctx)

	// Cancel the review task (Owner/coordinator cancellation, supersede…), then
	// a late verdict must be rejected without resurrecting state (§5-5).
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue SET status = 'cancelled', completed_at = now() WHERE id = $1
	`, reviewTask.ID); err != nil {
		t.Fatalf("cancel review task: %v", err)
	}
	_, err := svc.WriteVerdict(ctx, util.MustParseUUID(fixture.issueID),
		ReviewActor{ActorType: "agent", ActorID: util.MustParseUUID(fixture.reviewerAgentID)},
		VerdictInput{Verdict: "revise"})
	if !errors.Is(err, ErrNoOpenReviewTask) {
		t.Fatalf("verdict after cancellation: err = %v, want ErrNoOpenReviewTask", err)
	}
	state, _ := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateQueued {
		t.Fatalf("cancelled verdict must not move review_state; got %v", state)
	}
}

func TestReviewRequeue_LateLineage_Recovers(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	svc := fixture.reviewService(nil)

	// Fail closed first: no lineage at all.
	fixture.seedDeliveryComment(t, ctx, nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	state, _ := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateOwnerDecision {
		t.Fatalf("review_state = %v, want owner_decision", state)
	}

	// Non-coordinator agent cannot requeue.
	if _, err := svc.Requeue(ctx, util.MustParseUUID(fixture.issueID),
		ReviewActor{ActorType: "agent", ActorID: util.MustParseUUID(fixture.reviewerAgentID)}); !errors.Is(err, ErrNotCoordinator) {
		t.Fatalf("requeue by non-coordinator: err = %v, want ErrNotCoordinator", err)
	}

	// Late lineage: implementer re-delivers with a valid candidate.
	candidateID := fixture.seedCompletedCandidateTask(t, ctx)
	result, err := svc.Requeue(ctx, util.MustParseUUID(fixture.issueID),
		ReviewActor{ActorType: "agent", ActorID: util.MustParseUUID(fixture.coordinatorAgentID)})
	if err != nil {
		t.Fatalf("Requeue with late lineage: %v", err)
	}
	if result.ReviewState != ReviewStateQueued || !result.ReviewTaskCreated {
		t.Fatalf("requeue result = %+v, want queued + created", result)
	}
	state, _ = fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateQueued {
		t.Fatalf("review_state after requeue = %v, want queued", state)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks after requeue = %d, want 1", n)
	}
	requeued := fixture.currentReviewTask(t, ctx)
	if !uuidEqual(requeued.ReviewTargetTaskID, util.MustParseUUID(candidateID)) {
		t.Fatalf("requeue review task targets %s, want candidate %s",
			util.UUIDToString(requeued.ReviewTargetTaskID), candidateID)
	}

	// Idempotent retry: second requeue from queued is a no-op.
	result, err = svc.Requeue(ctx, util.MustParseUUID(fixture.issueID),
		ReviewActor{ActorType: "agent", ActorID: util.MustParseUUID(fixture.coordinatorAgentID)})
	if err != nil {
		t.Fatalf("second Requeue: %v", err)
	}
	if result.ReviewState != ReviewStateQueued || result.ReviewTaskCreated {
		t.Fatalf("second requeue = %+v, want no-op queued", result)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks after requeue retry = %d, want 1", n)
	}
}

func TestReviewRequeue_StillInvalid_KeepsOwnerDecision(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	svc := fixture.reviewService(nil)

	fixture.seedDeliveryComment(t, ctx, nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}

	result, err := svc.Requeue(ctx, util.MustParseUUID(fixture.issueID),
		ReviewActor{ActorType: "member", ActorID: util.MustParseUUID(fixture.userID)})
	if err != nil {
		t.Fatalf("Requeue still invalid: %v", err)
	}
	if result.ReviewState != ReviewStateOwnerDecision {
		t.Fatalf("requeue result = %+v, want owner_decision", result)
	}
	if result.ReviewTaskCreated {
		t.Fatalf("requeue must not create a review task while lineage is invalid")
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 0 {
		t.Fatalf("open review tasks = %d, want 0 while lineage invalid", n)
	}
}

func TestReviewOnIssueLeftReview_ResetsAndCancels(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	reviewTask := fixture.currentReviewTask(t, ctx)

	// Implementer pulls the issue back into rework (in_review → in_progress).
	if err := svc.OnIssueLeftReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueLeftReview: %v", err)
	}
	state, _ := fixture.issueReviewState(t, ctx)
	if state.Valid {
		t.Fatalf("review_state after rollback = %v, want NULL", state)
	}
	var cancelledStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, reviewTask.ID).Scan(&cancelledStatus); err != nil {
		t.Fatalf("load review task after rollback: %v", err)
	}
	if cancelledStatus != "cancelled" {
		t.Fatalf("review task status after rollback = %q, want cancelled (§5-8)", cancelledStatus)
	}
}

func TestReviewBackfillDryRun_ZeroWrites(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	svc := fixture.reviewService(nil)

	// All entries must live in ONE workspace (the dry-run is workspace-scoped),
	// so the extra issues share the base fixture's workspace.
	validIssue := fixture.withNewIssue(t)
	validIssue.seedCompletedCandidateTask(t, ctx)
	invalidIssue := fixture.withNewIssue(t)
	invalidIssue.seedDeliveryComment(t, ctx, nil)
	archivedIssue := fixture.withNewIssue(t)
	archivedIssue.seedCompletedCandidateTask(t, ctx)

	before := reviewStateSnapshot(t, pool, []string{fixture.issueID, validIssue.issueID, invalidIssue.issueID, archivedIssue.issueID})

	items, summary, err := svc.BackfillDryRun(ctx, util.MustParseUUID(fixture.workspaceID), []BackfillEntry{
		{IssueID: util.MustParseUUID(validIssue.issueID), IntendedReviewState: ReviewStateQueued},
		{IssueID: util.MustParseUUID(invalidIssue.issueID), IntendedReviewState: ReviewStateQueued},
		{IssueID: util.MustParseUUID(archivedIssue.issueID), IntendedReviewState: ReviewStateArchivedHistory},
		{IssueID: util.MustParseUUID(fixture.issueID), IntendedReviewState: ReviewStateAccepted},
	})
	if err != nil {
		t.Fatalf("BackfillDryRun: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("dry-run items = %d, want 4", len(items))
	}
	byIssue := make(map[string]BackfillItem, len(items))
	for _, item := range items {
		byIssue[util.UUIDToString(item.IssueID)] = item
	}

	if got := byIssue[validIssue.issueID]; got.ProposedReviewState != ReviewStateQueued || !got.LineageValid {
		t.Fatalf("valid lineage dry-run = %+v, want queued", got)
	}
	if got := byIssue[invalidIssue.issueID]; got.ProposedReviewState != ReviewStateOwnerDecision || got.LineageValid {
		t.Fatalf("invalid lineage dry-run = %+v, want owner_decision downgrade", got)
	}
	if got := byIssue[archivedIssue.issueID]; got.ProposedReviewState != ReviewStateArchivedHistory {
		t.Fatalf("archived dry-run = %+v, want archived_history", got)
	}
	// accepted must never be inferred from backfill (C12).
	if got := byIssue[fixture.issueID]; got.ProposedReviewState == ReviewStateAccepted {
		t.Fatalf("dry-run proposed accepted: %+v", got)
	}
	if summary.ByState[ReviewStateOwnerDecision] != 2 {
		t.Fatalf("owner_decision count = %d, want 2 (invalid lineage + accepted downgrade)", summary.ByState[ReviewStateOwnerDecision])
	}

	// Zero writes: every review_state still NULL, no review tasks, no comments.
	after := reviewStateSnapshot(t, pool, []string{fixture.issueID, validIssue.issueID, invalidIssue.issueID, archivedIssue.issueID})
	if before != after {
		t.Fatalf("dry-run mutated review_state: before=%q after=%q", before, after)
	}
	for _, id := range []string{validIssue.issueID, invalidIssue.issueID} {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND task_kind = 'review'
		`, id).Scan(&n); err != nil {
			t.Fatalf("count review tasks: %v", err)
		}
		if n != 0 {
			t.Fatalf("dry-run created %d review tasks for issue %s, want 0", n, id)
		}
	}
}

func TestReviewClaim_WIPGate_BlocksOverLimit(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()

	// Reviewer WIP limit = 1. One candidate issue first.
	first := seedReviewPipelineFixture(t, pool)
	first.seedCompletedCandidateTask(t, ctx)

	svc := fixture.reviewService(func(cfg *ReviewPipelineConfig) { cfg.ReviewWIPLimit = 1 })
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(first.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview first: %v", err)
	}
	if n := fixture.countOpenReviewTasksForAgent(t, ctx, fixture.reviewerAgentID); n != 1 {
		t.Fatalf("queued review tasks = %d, want 1", n)
	}

	taskSvc := NewTaskService(db.New(pool), pool, nil, events.New())
	taskSvc.ReviewWIPLimit = 1

	// First claim passes the WIP gate (open count excluding candidate = 0 < 1).
	task1, err := taskSvc.ClaimTask(ctx, util.MustParseUUID(fixture.reviewerAgentID))
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if task1 == nil || task1.TaskKind != TaskKindReview {
		t.Fatalf("first claim = %+v, want a review task", task1)
	}

	// A second review candidate queues while the first is still open (now
	// dispatched): the WIP gate must refuse the dispatch (open count = 1 >= 1).
	second := seedReviewPipelineFixture(t, pool)
	second.seedCompletedCandidateTask(t, ctx)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(second.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview second: %v", err)
	}
	task2, err := taskSvc.ClaimTask(ctx, util.MustParseUUID(fixture.reviewerAgentID))
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if task2 != nil {
		t.Fatalf("second claim = %+v, want WIP-gated nil (reviewer at cap)", task2)
	}

	// A non-review work task for the same agent stays claimable (gate is
	// review-kind-scoped). max_concurrent_tasks=4 so capacity is not the issue.
	var workTaskID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
		VALUES ($1, $2, 'queued', 0)
		RETURNING id::text
	`, fixture.reviewerAgentID, fixture.runtimeID).Scan(&workTaskID); err != nil {
		t.Fatalf("seed work task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, workTaskID)
	})
	workTask, err := taskSvc.ClaimTask(ctx, util.MustParseUUID(fixture.reviewerAgentID))
	if err != nil {
		t.Fatalf("work task claim: %v", err)
	}
	if workTask == nil || workTask.TaskKind != TaskKindWork {
		t.Fatalf("work task claim = %+v, want a work task despite review WIP cap", workTask)
	}
}

func TestReviewClaim_WIPGate_ConcurrentDoesNotOvershoot(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()

	// Two queued review tasks for the reviewer at WIP limit 1: the gate counts
	// queued-but-unclaimed review work toward the cap, so neither may be
	// dispatched — two concurrent claims must both come back empty instead of
	// overshooting the limit (count + dispatch in the same statement, C10).
	first := seedReviewPipelineFixture(t, pool)
	first.seedCompletedCandidateTask(t, ctx)
	second := seedReviewPipelineFixture(t, pool)
	second.seedCompletedCandidateTask(t, ctx)

	svc := fixture.reviewService(func(cfg *ReviewPipelineConfig) { cfg.ReviewWIPLimit = 1 })
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(first.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview first: %v", err)
	}
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(second.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview second: %v", err)
	}
	if n := fixture.countOpenReviewTasksForAgent(t, ctx, fixture.reviewerAgentID); n != 2 {
		t.Fatalf("queued review tasks = %d, want 2", n)
	}

	taskSvc := NewTaskService(db.New(pool), pool, nil, events.New())
	taskSvc.ReviewWIPLimit = 1

	const workers = 2
	start := make(chan struct{})
	results := make(chan *db.AgentTaskQueue, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			task, err := taskSvc.ClaimTask(ctx, util.MustParseUUID(fixture.reviewerAgentID))
			if err != nil {
				errs <- err
				return
			}
			results <- task
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent claim: %v", err)
	}
	for task := range results {
		if task != nil {
			t.Fatalf("concurrent claim dispatched a review task despite WIP cap: %+v", task)
		}
	}
	if n := fixture.countOpenReviewTasksForAgent(t, ctx, fixture.reviewerAgentID); n != 2 {
		t.Fatalf("open review tasks after concurrent claims = %d, want 2 (none dispatched)", n)
	}
}

func TestReviewSupersede_NewCandidateCancelsOldReview(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	candidateA := fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	oldReview := fixture.currentReviewTask(t, ctx)

	// A newer candidate re-enters while the first round is open (C9): the old
	// review task is cancelled, the new candidate gets the only open review task.
	candidateB := fixture.seedCompletedCandidateTask(t, ctx)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview (new candidate): %v", err)
	}

	var oldStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, oldReview.ID).Scan(&oldStatus); err != nil {
		t.Fatalf("load old review task: %v", err)
	}
	if oldStatus != "cancelled" {
		t.Fatalf("superseded review task status = %q, want cancelled", oldStatus)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("after supersede: open review tasks = %d, want only 1", n)
	}
	newReview := fixture.currentReviewTask(t, ctx)
	if !uuidEqual(newReview.ReviewTargetTaskID, util.MustParseUUID(candidateB)) {
		t.Fatalf("after supersede: open review task targets %s, want candidate %s",
			util.UUIDToString(newReview.ReviewTargetTaskID), candidateB)
	}
	state, _ := fixture.issueReviewState(t, ctx)
	if !state.Valid || state.String != ReviewStateQueued {
		t.Fatalf("review_state after supersede = %v, want queued for candidate B", state)
	}
	_ = candidateA
}

func TestReviewSupersede_SameCandidate_NoOp(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedCompletedCandidateTask(t, ctx)
	svc := fixture.reviewService(nil)
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	oldReview := fixture.currentReviewTask(t, ctx)

	// Re-entry resolving to the SAME candidate must not cancel/recreate.
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview (same candidate): %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, oldReview.ID).Scan(&status); err != nil {
		t.Fatalf("load review task: %v", err)
	}
	if status != "queued" {
		t.Fatalf("same-candidate re-entry cancelled the open review task: status = %q", status)
	}
	if n := fixture.openReviewTaskCount(t, ctx); n != 1 {
		t.Fatalf("open review tasks = %d, want 1", n)
	}
}

func TestReviewEscalatedEventPublishedOnFailClosed(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()
	fixture.seedDeliveryComment(t, ctx, nil)

	bus := events.New()
	received := make(chan events.Event, 4)
	bus.Subscribe(protocol.EventReviewEscalated, func(e events.Event) { received <- e })
	svc := NewReviewPipelineService(db.New(pool), pool, bus, ReviewPipelineConfig{
		Enabled:             true,
		ReviewerAgentID:     util.MustParseUUID(fixture.reviewerAgentID),
		ReviewerAgentIDSet:  true,
		CoordinatorAgentID:  util.MustParseUUID(fixture.coordinatorAgentID),
		CoordinatorAgentSet: true,
		ReviewWIPLimit:      10,
		ReviewPriority:      5,
	})
	if err := svc.OnIssueEnteredReview(ctx, util.MustParseUUID(fixture.issueID)); err != nil {
		t.Fatalf("OnIssueEnteredReview: %v", err)
	}
	select {
	case e := <-received:
		payload, _ := e.Payload.(map[string]any)
		if payload["reason"] != ReviewEscalationReasonMissingLineage {
			t.Fatalf("escalated payload reason = %v", payload["reason"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fail-closed did not publish review:escalated")
	}
}

// TestReviewPipelineAutopilotGate verifies §3 row 7: while the review pipeline
// is enabled, an in_review issue only finalizes an autopilot run once the
// acceptance axis says accepted; with the flag off, in_review stays terminal
// (legacy behavior unchanged).
func TestReviewPipelineAutopilotGate(t *testing.T) {
	pool := newReviewPipelineTestPool(t)
	fixture := seedReviewPipelineFixture(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		UPDATE issue SET origin_type = 'autopilot' WHERE id = $1
	`, fixture.issueID); err != nil {
		t.Fatalf("mark issue autopilot-origin: %v", err)
	}
	var apID, runID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO autopilot (
			workspace_id, title, assignee_id, status, execution_mode,
			issue_title_template, created_by_type, created_by_id
		) VALUES ($1, 'review-gate-autopilot', $2, 'active', 'create_issue', 't', 'member', $3)
		RETURNING id::text
	`, fixture.workspaceID, fixture.implementerAgentID, fixture.userID).Scan(&apID); err != nil {
		t.Fatalf("seed autopilot: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, apID) })
	if err := pool.QueryRow(ctx, `
		INSERT INTO autopilot_run (autopilot_id, source, status, issue_id)
		VALUES ($1, 'manual', 'running', $2)
		RETURNING id::text
	`, apID, fixture.issueID).Scan(&runID); err != nil {
		t.Fatalf("seed autopilot run: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE id = $1`, runID) })

	queries := db.New(pool)

	// Flag ON + in_review + queued (not accepted) → run stays running.
	enabled := NewAutopilotService(queries, pool, events.New(), nil)
	enabled.ReviewPipelineEnabled = true
	if _, err := pool.Exec(ctx, `
		UPDATE issue SET status = 'in_review', review_state = 'queued' WHERE id = $1
	`, fixture.issueID); err != nil {
		t.Fatalf("set in_review/queued: %v", err)
	}
	issue, err := queries.GetIssue(ctx, util.MustParseUUID(fixture.issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	enabled.SyncRunFromIssue(ctx, issue)
	run, err := queries.GetAutopilotRun(ctx, util.MustParseUUID(runID))
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.Status != "running" {
		t.Fatalf("flag-on in_review(queued) finalized the run: status = %q, want running", run.Status)
	}

	// Flag ON + accepted → run completes.
	if _, err := pool.Exec(ctx, `
		UPDATE issue SET review_state = 'accepted' WHERE id = $1
	`, fixture.issueID); err != nil {
		t.Fatalf("set accepted: %v", err)
	}
	issue, err = queries.GetIssue(ctx, util.MustParseUUID(fixture.issueID))
	if err != nil {
		t.Fatalf("reload issue after accepted: %v", err)
	}
	enabled.SyncRunFromIssue(ctx, issue)
	run, err = queries.GetAutopilotRun(ctx, util.MustParseUUID(runID))
	if err != nil {
		t.Fatalf("load run after accepted: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("flag-on accepted did not complete the run: status = %q", run.Status)
	}

	// Flag OFF + in_review (any state) → legacy terminal behavior.
	if _, err := pool.Exec(ctx, `
		UPDATE autopilot_run SET status = 'running' WHERE id = $1
	`, runID); err != nil {
		t.Fatalf("reset run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE issue SET review_state = 'queued' WHERE id = $1
	`, fixture.issueID); err != nil {
		t.Fatalf("set queued: %v", err)
	}
	issue, err = queries.GetIssue(ctx, util.MustParseUUID(fixture.issueID))
	if err != nil {
		t.Fatalf("reload issue for legacy path: %v", err)
	}
	legacy := NewAutopilotService(queries, pool, events.New(), nil) // ReviewPipelineEnabled = false
	legacy.SyncRunFromIssue(ctx, issue)
	run, err = queries.GetAutopilotRun(ctx, util.MustParseUUID(runID))
	if err != nil {
		t.Fatalf("load run (legacy): %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("flag-off in_review must stay terminal: status = %q, want completed", run.Status)
	}
}

// --- helpers ---

func (f reviewPipelineFixture) countOpenReviewTasksForAgent(t *testing.T, ctx context.Context, agentID string) int64 {
	t.Helper()
	n, err := db.New(f.pool).CountOpenReviewTasks(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("count open review tasks: %v", err)
	}
	return n
}

func reviewStateSnapshot(t *testing.T, pool *pgxpool.Pool, issueIDs []string) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id, review_state FROM issue WHERE id = ANY($1::uuid[])
	`, issueIDs)
	if err != nil {
		t.Fatalf("snapshot review states: %v", err)
	}
	defer rows.Close()
	snapshot := make(map[string]string)
	for rows.Next() {
		var id string
		var state pgtype.Text
		if err := rows.Scan(&id, &state); err != nil {
			t.Fatalf("scan review state: %v", err)
		}
		if state.Valid {
			snapshot[id] = state.String
		} else {
			snapshot[id] = "NULL"
		}
	}
	encoded, _ := json.Marshal(snapshot)
	return string(encoded)
}
