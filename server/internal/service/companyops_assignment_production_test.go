package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func newProductionCompanyOpsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping production CompanyOps assignment integration test")
	}
	requireIsolatedLoopbackDatabaseURL(t, databaseURL, "production CompanyOps assignment test")

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

type productionCompanyOpsFixture struct {
	workspaceID pgtype.UUID
	userID      pgtype.UUID
	runtimeID   pgtype.UUID
	agentID     pgtype.UUID
	issueID     pgtype.UUID
}

func cleanupProductionCompanyOps(t *testing.T, ctx context.Context, pool *pgxpool.Pool, statement string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, statement, args...); err != nil {
		t.Errorf("cleanup CompanyOps fixture: %v", err)
	}
}

func seedProductionCompanyOpsFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) productionCompanyOpsFixture {
	t.Helper()
	suffix := uuid.NewString()
	var fixture productionCompanyOpsFixture
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if fixture.workspaceID.Valid {
			cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM assignment_dispatch_receipt WHERE workspace_id = $1`, fixture.workspaceID)
			cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM execution_receipt WHERE workspace_id = $1`, fixture.workspaceID)
			cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1)`, fixture.workspaceID)
			cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM issue WHERE workspace_id = $1`, fixture.workspaceID)
			cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM agent WHERE workspace_id = $1`, fixture.workspaceID)
			cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM agent_runtime WHERE workspace_id = $1`, fixture.workspaceID)
			cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM member WHERE workspace_id = $1`, fixture.workspaceID)
			cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM workspace WHERE id = $1`, fixture.workspaceID)
		}
		if fixture.userID.Valid {
			cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM "user" WHERE id = $1`, fixture.userID)
		}
	})

	if err := pool.QueryRow(ctx,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"CompanyOps Production Test",
		fmt.Sprintf("companyops-production-%s@multica.test", suffix),
	).Scan(&fixture.userID); err != nil {
		t.Fatalf("seed CompanyOps user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`,
		"CompanyOps Production Test",
		"companyops-production-"+suffix,
	).Scan(&fixture.workspaceID); err != nil {
		t.Fatalf("seed CompanyOps workspace: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		fixture.workspaceID,
		fixture.userID,
	); err != nil {
		t.Fatalf("seed CompanyOps member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id
		) VALUES ($1, $2, 'cloud', 'codex', 'online', '', '{}'::jsonb, $3)
		RETURNING id`,
		fixture.workspaceID,
		"companyops-runtime-"+suffix,
		fixture.userID,
	).Scan(&fixture.runtimeID); err != nil {
		t.Fatalf("seed CompanyOps runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, runtime_mode, runtime_config, runtime_id, visibility,
			max_concurrent_tasks, owner_id, instructions, custom_env, custom_args
		) VALUES ($1, $2, 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id`,
		fixture.workspaceID,
		"companyops-agent-"+suffix,
		fixture.runtimeID,
		fixture.userID,
	).Scan(&fixture.agentID); err != nil {
		t.Fatalf("seed CompanyOps agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type, creator_id, number
		) VALUES ($1, $2, 'todo', 'medium', 'member', $3, 1)
		RETURNING id`,
		fixture.workspaceID,
		"CompanyOps production assignment "+suffix,
		fixture.userID,
	).Scan(&fixture.issueID); err != nil {
		t.Fatalf("seed CompanyOps issue: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE workspace SET issue_counter = GREATEST(issue_counter, 1) WHERE id = $1`,
		fixture.workspaceID,
	); err != nil {
		t.Fatalf("synchronize CompanyOps workspace issue counter: %v", err)
	}
	return fixture
}

func insertProductionCompanyOpsIssue(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture productionCompanyOpsFixture,
	status, title string,
) pgtype.UUID {
	t.Helper()
	var issueID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number)
		SELECT $1, $2, $3, 'medium', 'member', $4, COALESCE(MAX(number), 0) + 1
		FROM issue WHERE workspace_id = $1
		RETURNING id`,
		fixture.workspaceID,
		title,
		status,
		fixture.userID,
	).Scan(&issueID); err != nil {
		t.Fatalf("seed CompanyOps %s issue: %v", status, err)
	}
	return issueID
}

type productionCompanyOpsWakeup struct {
	effects    *[]string
	runtimeIDs []string
	taskIDs    []string
}

func (w *productionCompanyOpsWakeup) NotifyTaskAvailable(runtimeID, taskID string) {
	*w.effects = append(*w.effects, "notify")
	w.runtimeIDs = append(w.runtimeIDs, runtimeID)
	w.taskIDs = append(w.taskIDs, taskID)
}

func productionCompanyOpsRequest(
	fixture productionCompanyOpsFixture,
	issueID, commandID pgtype.UUID,
) CompanyOpsAssignmentRequest {
	req := validCompanyOpsAssignmentRequest()
	req.CommandID = commandID
	req.WorkspaceID = fixture.workspaceID
	req.IssueID = issueID
	req.LocalAgentID = fixture.agentID
	req.LocalAgentSourceRef = "/api/agents/" + util.UUIDToString(fixture.agentID)
	req.ActorUserID = fixture.userID
	req.Bindings[0].AgentRef = req.LocalAgentSourceRef
	req.Agents[0].SourceRef = req.LocalAgentSourceRef
	req.WorkOrder.DisplayName = "CompanyOps production assignment"
	return req
}

func TestProductionCompanyOpsAssignmentBackend_CanonicalTransactionAndReplay(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := seedProductionCompanyOpsFixture(t, ctx, pool)
	queries := db.New(pool)

	var (
		effects       []string
		publishedTask events.Event
	)
	bus := events.New()
	bus.Subscribe(protocol.EventTaskQueued, func(event events.Event) {
		effects = append(effects, "broadcast")
		publishedTask = event
	})
	wakeup := &productionCompanyOpsWakeup{effects: &effects}
	taskService := &TaskService{Queries: queries, TxStarter: pool, Bus: bus, Wakeup: wakeup}
	backend, err := NewProductionCompanyOpsAssignmentBackend(queries, pool, taskService)
	if err != nil {
		t.Fatalf("NewProductionCompanyOpsAssignmentBackend: %v", err)
	}
	assignmentService := NewCompanyOpsAssignmentService(backend)
	commandID := util.MustParseUUID(uuid.NewString())
	req := productionCompanyOpsRequest(fixture, fixture.issueID, commandID)

	receipt, err := assignmentService.Dispatch(ctx, req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if want := []string{"broadcast", "notify"}; fmt.Sprint(effects) != fmt.Sprint(want) {
		t.Fatalf("post-commit effects = %v, want %v", effects, want)
	}
	storedTask, err := queries.GetAgentTask(ctx, receipt.InitialTaskID)
	if err != nil {
		t.Fatalf("GetAgentTask: %v", err)
	}
	if storedTask.ID != receipt.InitialTaskID || storedTask.AgentID != fixture.agentID ||
		storedTask.IssueID != fixture.issueID || storedTask.RuntimeID != fixture.runtimeID ||
		storedTask.Status != "queued" || storedTask.Priority != priorityToInt("medium") ||
		storedTask.HandoffNote != (pgtype.Text{String: req.HandoffNote, Valid: true}) ||
		storedTask.AccountableUserID != fixture.userID ||
		storedTask.TriggerEvidenceKind != (pgtype.Text{String: assignmentDispatchEvidenceKind, Valid: true}) ||
		storedTask.TriggerEvidenceRefID != commandID {
		t.Fatalf("stored canonical task is incomplete or mismatched: %+v", storedTask)
	}
	if publishedTask.WorkspaceID != util.UUIDToString(fixture.workspaceID) {
		t.Fatalf("published task workspace = %q, want %q", publishedTask.WorkspaceID, util.UUIDToString(fixture.workspaceID))
	}
	payload, ok := publishedTask.Payload.(map[string]any)
	if !ok || payload["task_id"] != util.UUIDToString(storedTask.ID) ||
		payload["agent_id"] != util.UUIDToString(storedTask.AgentID) ||
		payload["issue_id"] != util.UUIDToString(storedTask.IssueID) || payload["status"] != "queued" {
		t.Fatalf("published complete committed task identity payload = %#v", publishedTask.Payload)
	}
	if len(wakeup.runtimeIDs) != 1 || wakeup.runtimeIDs[0] != util.UUIDToString(storedTask.RuntimeID) ||
		len(wakeup.taskIDs) != 1 || wakeup.taskIDs[0] != util.UUIDToString(storedTask.ID) {
		t.Fatalf("wakeup runtime/task = %v/%v, want %s/%s", wakeup.runtimeIDs, wakeup.taskIDs, util.UUIDToString(storedTask.RuntimeID), util.UUIDToString(storedTask.ID))
	}

	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT assignee_type, assignee_id FROM issue WHERE workspace_id = $1 AND id = $2`,
		fixture.workspaceID,
		fixture.issueID,
	).Scan(&assigneeType, &assigneeID); err != nil {
		t.Fatalf("read assigned issue: %v", err)
	}
	if assigneeType != (pgtype.Text{String: "agent", Valid: true}) || assigneeID != fixture.agentID {
		t.Fatalf("assigned issue = %+v/%v, want agent/%v", assigneeType, assigneeID, fixture.agentID)
	}

	replay, err := assignmentService.Dispatch(ctx, req)
	if err != nil {
		t.Fatalf("exact replay Dispatch: %v", err)
	}
	if replay != receipt {
		t.Fatalf("exact replay receipt = %+v, want %+v", replay, receipt)
	}
	if len(effects) != 2 {
		t.Fatalf("exact replay effects = %v, want original broadcast/notify only", effects)
	}
	var taskCount, receiptCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`,
		fixture.issueID,
		fixture.agentID,
	).Scan(&taskCount); err != nil {
		t.Fatalf("count assignment tasks: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM assignment_dispatch_receipt WHERE workspace_id = $1 AND command_id = $2`,
		fixture.workspaceID,
		commandID,
	).Scan(&receiptCount); err != nil {
		t.Fatalf("count assignment receipts: %v", err)
	}
	if taskCount != 1 || receiptCount != 1 {
		t.Fatalf("exact replay task/receipt counts = %d/%d, want 1/1", taskCount, receiptCount)
	}

	conflict := req
	conflict.HandoffNote = "A different production handoff must conflict."
	conflict.InputDigest = CompanyOpsHandoffInputDigest(conflict.HandoffNote)
	if _, err := assignmentService.Dispatch(ctx, conflict); !errors.Is(err, ErrCompanyOpsAssignmentConflict) {
		t.Fatalf("different-payload replay error = %v, want %v", err, ErrCompanyOpsAssignmentConflict)
	}
	if len(effects) != 2 {
		t.Fatalf("different-payload replay effects = %v, want unchanged", effects)
	}

	for name, failedReq := range map[string]CompanyOpsAssignmentRequest{
		"already assigned": productionCompanyOpsRequest(
			fixture,
			fixture.issueID,
			util.MustParseUUID(uuid.NewString()),
		),
		"cross workspace": func() CompanyOpsAssignmentRequest {
			candidate := productionCompanyOpsRequest(fixture, fixture.issueID, util.MustParseUUID(uuid.NewString()))
			candidate.WorkspaceID = util.MustParseUUID(uuid.NewString())
			return candidate
		}(),
		"terminal": productionCompanyOpsRequest(
			fixture,
			insertProductionCompanyOpsIssue(t, ctx, pool, fixture, "done", "CompanyOps terminal "+uuid.NewString()),
			util.MustParseUUID(uuid.NewString()),
		),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := assignmentService.Dispatch(ctx, failedReq); !errors.Is(err, ErrCompanyOpsIssueNotAssignable) {
				t.Fatalf("Dispatch error = %v, want %v", err, ErrCompanyOpsIssueNotAssignable)
			}
			if len(effects) != 2 {
				t.Fatalf("failed exact CAS effects = %v, want unchanged", effects)
			}
		})
	}

	rollbackIssueID := insertProductionCompanyOpsIssue(
		t,
		ctx,
		pool,
		fixture,
		"todo",
		"CompanyOps rollback "+uuid.NewString(),
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			originator_user_id, accountable_user_id, originator_source,
			trigger_evidence_kind, trigger_evidence_ref_id
		) VALUES ($1, $2, $3, 'queued', 2, $4, $4, 'direct_human', 'issue_assignment', $3)`,
		fixture.agentID,
		fixture.runtimeID,
		rollbackIssueID,
		fixture.userID,
	); err != nil {
		t.Fatalf("seed duplicate pending task: %v", err)
	}
	rollbackCommandID := util.MustParseUUID(uuid.NewString())
	rollbackReq := productionCompanyOpsRequest(fixture, rollbackIssueID, rollbackCommandID)
	if _, err := assignmentService.Dispatch(ctx, rollbackReq); err == nil {
		t.Fatal("Dispatch with task insert conflict error = nil")
	}
	if err := pool.QueryRow(ctx,
		`SELECT assignee_type, assignee_id FROM issue WHERE workspace_id = $1 AND id = $2`,
		fixture.workspaceID,
		rollbackIssueID,
	).Scan(&assigneeType, &assigneeID); err != nil {
		t.Fatalf("read rolled-back issue: %v", err)
	}
	if assigneeType.Valid || assigneeID.Valid {
		t.Fatalf("rolled-back issue assignee = %+v/%v, want NULL/NULL", assigneeType, assigneeID)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM assignment_dispatch_receipt WHERE workspace_id = $1 AND command_id = $2`,
		fixture.workspaceID,
		rollbackCommandID,
	).Scan(&receiptCount); err != nil {
		t.Fatalf("count rolled-back receipt: %v", err)
	}
	if receiptCount != 0 || len(effects) != 2 {
		t.Fatalf("rollback receipt/effects = %d/%v, want 0/original effects", receiptCount, effects)
	}
}

func TestProductionCompanyOpsAssignmentBackend_ProjectBoundIssueTaskAtomic(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedProductionCompanyOpsFixture(t, ctx, pool)
	queries := db.New(pool)

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO project (workspace_id, title, status, priority) VALUES ($1, $2, 'planned', 'none') RETURNING id`,
		fixture.workspaceID,
		"CompanyOps atomic project "+uuid.NewString(),
	).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM project WHERE id = $1 AND workspace_id = $2`, projectID, fixture.workspaceID)
	})

	bus := events.New()
	var issueCreated int
	bus.Subscribe(protocol.EventIssueCreated, func(event events.Event) {
		if event.WorkspaceID == util.UUIDToString(fixture.workspaceID) {
			issueCreated++
		}
	})
	wakeup := &productionCompanyOpsWakeup{effects: new([]string)}
	taskService := &TaskService{Queries: queries, TxStarter: pool, Bus: bus, Wakeup: wakeup}
	projectionService := newWorkOrderProjectionService(t, queries, pool, bus)
	backend, err := NewProductionCompanyOpsAssignmentBackend(queries, pool, taskService, projectionService)
	if err != nil {
		t.Fatalf("NewProductionCompanyOpsAssignmentBackend: %v", err)
	}
	req := productionCompanyOpsRequest(fixture, pgtype.UUID{}, util.MustParseUUID(uuid.NewString()))
	req.ProjectID = projectID
	req.IssueID = pgtype.UUID{}

	receipt, err := NewCompanyOpsAssignmentService(backend).Dispatch(ctx, req)
	if err != nil {
		t.Fatalf("project-bound Dispatch: %v", err)
	}
	var issueProjectID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT project_id FROM issue WHERE workspace_id = $1 AND id = $2`, fixture.workspaceID, receipt.IssueID).Scan(&issueProjectID); err != nil {
		t.Fatalf("read project-bound issue: %v", err)
	}
	if issueProjectID != projectID {
		t.Fatalf("issue project_id = %v, want %v", issueProjectID, projectID)
	}
	var taskIssueID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT issue_id FROM agent_task_queue WHERE id = $1`, receipt.InitialTaskID).Scan(&taskIssueID); err != nil {
		t.Fatalf("read project-bound task: %v", err)
	}
	if taskIssueID != receipt.IssueID || issueCreated != 1 {
		t.Fatalf("task/issue-created side effects = %v/%d, want %v/1", taskIssueID, issueCreated, receipt.IssueID)
	}
	var taskCount, receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, receipt.IssueID).Scan(&taskCount); err != nil {
		t.Fatalf("count project-bound tasks: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM assignment_dispatch_receipt WHERE workspace_id = $1 AND command_id = $2`, fixture.workspaceID, req.CommandID).Scan(&receiptCount); err != nil {
		t.Fatalf("count project-bound receipts: %v", err)
	}
	if taskCount != 1 || receiptCount != 1 {
		t.Fatalf("project-bound task/receipt count = %d/%d, want 1/1", taskCount, receiptCount)
	}
	assertNoPartialWrite := func(label string, failedReq CompanyOpsAssignmentRequest) {
		t.Helper()
		var issueCount, linkCount, failedTaskCount, failedReceiptCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2`, failedReq.WorkspaceID, strings.TrimSpace(failedReq.WorkOrder.DisplayName)).Scan(&issueCount); err != nil {
			t.Fatalf("%s count projected issues: %v", label, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM external_work_order_link WHERE workspace_id = $1 AND work_order_ref = $2`, failedReq.WorkspaceID, failedReq.WorkOrder.SourceRef).Scan(&linkCount); err != nil {
			t.Fatalf("%s count WorkOrder links: %v", label, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE trigger_evidence_kind = $1 AND trigger_evidence_ref_id = $2`, assignmentDispatchEvidenceKind, failedReq.CommandID).Scan(&failedTaskCount); err != nil {
			t.Fatalf("%s count assignment tasks: %v", label, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM assignment_dispatch_receipt WHERE workspace_id = $1 AND command_id = $2`, failedReq.WorkspaceID, failedReq.CommandID).Scan(&failedReceiptCount); err != nil {
			t.Fatalf("%s count assignment receipts: %v", label, err)
		}
		if issueCount != 0 || linkCount != 0 || failedTaskCount != 0 || failedReceiptCount != 0 {
			t.Fatalf("%s partial writes = issue:%d link:%d task:%d receipt:%d, want all zero", label, issueCount, linkCount, failedTaskCount, failedReceiptCount)
		}
	}

	missing := productionCompanyOpsRequest(fixture, pgtype.UUID{}, util.MustParseUUID(uuid.NewString()))
	missing.ProjectID = util.MustParseUUID(uuid.NewString())
	missing.WorkOrder.SourceRef = "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-MISSING-" + uuid.NewString()
	missing.WorkOrder.DisplayName = "CompanyOps missing project " + uuid.NewString()
	if _, err := NewCompanyOpsAssignmentService(backend).Dispatch(ctx, missing); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("missing project error = %v, want %v", err, ErrProjectNotFound)
	}
	assertNoPartialWrite("missing project", missing)
	var foreignWorkspaceID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`,
		"CompanyOps foreign workspace",
		"companyops-foreign-"+uuid.NewString(),
	).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	var foreignProjectID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO project (workspace_id, title, status, priority) VALUES ($1, $2, 'planned', 'none') RETURNING id`,
		foreignWorkspaceID,
		"CompanyOps foreign project "+uuid.NewString(),
	).Scan(&foreignProjectID); err != nil {
		t.Fatalf("seed foreign project: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM project WHERE id = $1`, foreignProjectID)
		cleanupProductionCompanyOps(t, cleanupCtx, pool, `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})
	crossWorkspace := productionCompanyOpsRequest(fixture, pgtype.UUID{}, util.MustParseUUID(uuid.NewString()))
	crossWorkspace.ProjectID = foreignProjectID
	crossWorkspace.WorkOrder.SourceRef = "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-CROSS-" + uuid.NewString()
	crossWorkspace.WorkOrder.DisplayName = "CompanyOps cross-workspace project " + uuid.NewString()
	if _, err := NewCompanyOpsAssignmentService(backend).Dispatch(ctx, crossWorkspace); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("cross-workspace project error = %v, want %v", err, ErrProjectNotFound)
	}
	assertNoPartialWrite("cross-workspace project", crossWorkspace)
}
