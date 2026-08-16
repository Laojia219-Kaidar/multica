package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCompanyOpsOutcomeListCountAndCursorRejectCrossWorkspaceJoins(t *testing.T) {
	pool := newOutcomePaginationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	queries := db.New(pool)
	svc := NewCompanyOpsOutcomeCenterService(queries)
	workspaceA := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	workspaceB := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	issueB := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	agentB := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	taskB := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	commandPolluted := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	commandPlain := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	for id, name := range map[pgtype.UUID]string{
		workspaceA: "Outcome Boundary A " + uuid.NewString(),
		workspaceB: "Outcome Boundary B " + uuid.NewString(),
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO workspace (id, name, slug) VALUES ($1, $2, $3)`, id, name, "boundary-"+uuid.NewString()); err != nil {
			t.Fatalf("insert workspace %s: %v", name, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM execution_receipt WHERE workspace_id IN ($1, $2)`, workspaceA, workspaceB)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM assignment_dispatch_receipt WHERE workspace_id IN ($1, $2)`, workspaceA, workspaceB)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM agent_task_queue WHERE id = $1`, taskB)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM agent WHERE id = $1`, agentB)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM issue WHERE id = $1`, issueB)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspace WHERE id IN ($1, $2)`, workspaceA, workspaceB)
	})

	runtimeB := "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0d"
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_runtime (id, workspace_id, name, runtime_mode, provider, status)
		VALUES ($1, $2, 'foreign-runtime-only', 'local', 'test', 'offline')`, runtimeB, workspaceB); err != nil {
		t.Fatalf("insert foreign runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeB)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent (id, workspace_id, name, runtime_mode, status, runtime_id)
		VALUES ($1, $2, 'foreign-agent-only', 'local', 'offline', $3)`, agentB, workspaceB, runtimeB); err != nil {
		t.Fatalf("insert foreign agent: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO issue (id, workspace_id, title, status, creator_type, creator_id)
		VALUES ($1, $2, 'foreign-only-title', 'backlog', 'agent', $3)`, issueB, workspaceB, agentB); err != nil {
		t.Fatalf("insert foreign issue: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue (id, agent_id, issue_id, status, runtime_id)
		VALUES ($1, $2, $3, 'completed', $4)`, taskB, agentB, issueB, runtimeB); err != nil {
		t.Fatalf("insert foreign task: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_receipt (
			task_id, workspace_id, issue_id, assignment_command_id,
			work_order_ref, work_order_revision, work_order_digest, input_digest,
			employee_ref, employee_revision, employee_digest,
			binding_ref, binding_revision, binding_digest,
			agent_ref, agent_revision, agent_digest,
			runtime_snapshot, runtime_digest, claimed_at,
			terminal_status, completed_at, finalized_at
		) VALUES (
			$1, $2, $3, $4,
			'WO-FOREIGN', 'r1', $5, $6,
			'hivecosm://employees/FOREIGN', 'r1', $7,
			'hivecosm://identity-bindings/FOREIGN', 'r1', $8,
			'/api/agents/foreign', 'r1', $9,
			'{}', $10, now(), 'completed', now(), now()
		)`, taskB, workspaceB, issueB, commandPolluted,
		sha256Digest(1), sha256Digest(2), sha256Digest(3), sha256Digest(4), sha256Digest(5), sha256Digest(6)); err != nil {
		t.Fatalf("insert foreign execution receipt: %v", err)
	}

	insertAssignment := func(commandID, issueID, agentID, taskID pgtype.UUID, createdAt time.Time, ref string) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO assignment_dispatch_receipt (
				command_id, workspace_id, issue_id, local_agent_id, initial_task_id,
				work_order_ref, work_order_revision, work_order_digest, input_digest,
				employee_ref, employee_revision, employee_digest,
				binding_ref, binding_revision, binding_digest,
				agent_ref, agent_revision, agent_digest, created_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, 'r1', $7, $8,
				'hivecosm://employees/BOUNDARY', 'r1', $9,
				'hivecosm://identity-bindings/BOUNDARY', 'r1', $10,
				$11, 'r1', $12, $13
			)`, commandID, workspaceA, issueID, agentID, taskID,
			ref, sha256Digest(11), sha256Digest(12), sha256Digest(13),
			sha256Digest(14), fmt.Sprintf("/api/agents/%s", uuid.NewString()), sha256Digest(15), createdAt)
		if err != nil {
			t.Fatalf("insert assignment %s: %v", ref, err)
		}
	}
	base := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	insertAssignment(commandPolluted, issueB, agentB, taskB, base.Add(2*time.Minute), "WO-POLLUTED")
	insertAssignment(commandPlain, pgtype.UUID{Bytes: uuid.New(), Valid: true}, pgtype.UUID{Bytes: uuid.New(), Valid: true}, pgtype.UUID{Bytes: uuid.New(), Valid: true}, base, "WO-PLAIN")

	listReq := CompanyOpsOutcomeListRequest{WorkspaceID: workspaceA, Limit: 10}
	summaries, total, err := svc.ListOutcomes(ctx, listReq)
	if err != nil {
		t.Fatalf("ListOutcomes() error = %v", err)
	}
	if len(summaries) != 2 || total != 2 {
		t.Fatalf("unfiltered list = len=%d total=%d, want 2/2", len(summaries), total)
	}
	for _, summary := range summaries {
		if summary.Issue.Title == "foreign-only-title" || summary.CurrentAgentDisplay.Name == "foreign-agent-only" {
			t.Fatalf("cross-workspace display leaked: %+v", summary)
		}
	}

	searchReq := listReq
	searchReq.Q = "foreign-only-title"
	search, searchTotal, err := svc.ListOutcomes(ctx, searchReq)
	if err != nil {
		t.Fatalf("foreign search ListOutcomes() error = %v", err)
	}
	if len(search) != 0 || searchTotal != 0 {
		t.Fatalf("foreign search = len=%d total=%d, want 0/0", len(search), searchTotal)
	}

	pageReq := CompanyOpsOutcomeListRequest{WorkspaceID: workspaceA, Limit: 1}
	page1, err := svc.ListOutcomesPage(ctx, pageReq)
	if err != nil || len(page1.Summaries) != 1 || page1.Total != 2 || !page1.HasMore || page1.NextCursor == nil {
		t.Fatalf("cursor page1 = %+v err=%v, want one row and next cursor", page1, err)
	}
	page2Req := pageReq
	page2Req.Cursor = *page1.NextCursor
	page2, err := svc.ListOutcomesPage(ctx, page2Req)
	if err != nil || len(page2.Summaries) != 1 || page2.Total != 2 || page2.HasMore {
		t.Fatalf("cursor page2 = %+v err=%v, want final one row", page2, err)
	}
	for _, page := range []CompanyOpsOutcomePage{page1, page2} {
		for _, summary := range page.Summaries {
			if summary.Issue.Title == "foreign-only-title" || summary.CurrentAgentDisplay.Name == "foreign-agent-only" {
				t.Fatalf("cross-workspace cursor display leaked: %+v", summary)
			}
		}
	}
}
