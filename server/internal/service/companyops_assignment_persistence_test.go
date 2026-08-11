package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/companyops"
)

func persistenceUUID(seed byte) pgtype.UUID {
	var value [16]byte
	value[0] = 0xc0
	value[1] = 0x5a
	value[15] = seed
	return pgtype.UUID{Bytes: value, Valid: true}
}

func persistenceTarget() companyops.ExecutionTargetSnapshot {
	return companyops.ExecutionTargetSnapshot{
		WorkOrderRef:      "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-PERSISTENCE-001",
		WorkOrderRevision: "wo-rev-3",
		WorkOrderDigest:   assignmentDigest("1"),
		InputDigest:       assignmentDigest("2"),
		EmployeeRef:       "hivecosm://employees/EMP-PERSISTENCE-001",
		EmployeeRevision:  "employee-rev-4",
		EmployeeDigest:    assignmentDigest("3"),
		BindingRef:        "hivecosm://identity-bindings/BIND-PERSISTENCE-001",
		BindingRevision:   "binding-rev-5",
		BindingDigest:     assignmentDigest("4"),
		AgentRef:          "/api/agents/00000000-0000-4000-8000-000000000004",
		AgentRevision:     "agent-rev-6",
		AgentDigest:       assignmentDigest("5"),
	}
}

func beginCompanyOpsPersistenceTestTx(t *testing.T) (context.Context, pgx.Tx) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping CompanyOps persistence integration test")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if parsed.Port() == "5432" {
		t.Skip("refusing to connect CompanyOps persistence test to port 5432")
	}
	if parsed.Port() != "55432" {
		t.Skipf("CompanyOps persistence test requires isolated worktree port 55432, got %q", parsed.Port())
	}
	if host := parsed.Hostname(); host != "127.0.0.1" && host != "localhost" && host != "::1" {
		t.Skipf("CompanyOps persistence test requires a loopback database host, got %q", host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatalf("open isolated DATABASE_URL: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatalf("begin CompanyOps persistence test transaction: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
		pool.Close()
		cancel()
	})
	return ctx, tx
}

func TestCompanyOpsPersistence_ExternalWorkOrderLinkExactReplayAndConflict(t *testing.T) {
	ctx, tx := beginCompanyOpsPersistenceTestTx(t)
	repo := NewCompanyOpsPersistenceRepository(tx)
	link := ExternalWorkOrderLink{
		WorkspaceID:      persistenceUUID(1),
		WorkOrderRef:     persistenceTarget().WorkOrderRef,
		LinkedRevision:   persistenceTarget().WorkOrderRevision,
		LinkedDigest:     persistenceTarget().WorkOrderDigest,
		SourceObservedAt: time.Date(2026, 8, 11, 8, 30, 0, 0, time.UTC),
		FreshnessAtLink:  "current",
		IssueID:          persistenceUUID(2),
	}

	first, err := repo.EnsureExternalWorkOrderLink(ctx, link)
	if err != nil {
		t.Fatalf("EnsureExternalWorkOrderLink first call: %v", err)
	}
	second, err := repo.EnsureExternalWorkOrderLink(ctx, link)
	if err != nil {
		t.Fatalf("EnsureExternalWorkOrderLink exact replay: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("external WorkOrder exact replay changed link:\nfirst=%+v\nsecond=%+v", first, second)
	}

	conflicts := []struct {
		name   string
		mutate func(*ExternalWorkOrderLink)
	}{
		{
			name: "revision",
			mutate: func(value *ExternalWorkOrderLink) {
				value.LinkedRevision = "wo-rev-conflict"
			},
		},
		{
			name: "digest",
			mutate: func(value *ExternalWorkOrderLink) {
				value.LinkedDigest = assignmentDigest("9")
			},
		},
	}
	for _, tt := range conflicts {
		t.Run(tt.name, func(t *testing.T) {
			conflict := link
			tt.mutate(&conflict)
			_, err := repo.EnsureExternalWorkOrderLink(ctx, conflict)
			if !errors.Is(err, ErrExternalWorkOrderLinkConflict) {
				t.Fatalf("conflicting WorkOrder %s error = %v, want ErrExternalWorkOrderLinkConflict", tt.name, err)
			}
		})
	}
}

func TestCompanyOpsPersistence_AssignmentCommandExactReplayAndConflict(t *testing.T) {
	ctx, tx := beginCompanyOpsPersistenceTestTx(t)
	repo := NewCompanyOpsPersistenceRepository(tx)
	receipt := AssignmentDispatchReceipt{
		CommandID:     persistenceUUID(10),
		WorkspaceID:   persistenceUUID(11),
		IssueID:       persistenceUUID(12),
		LocalAgentID:  persistenceUUID(13),
		InitialTaskID: persistenceUUID(14),
		Target:        persistenceTarget(),
	}

	first, err := repo.AppendAssignmentDispatchReceipt(ctx, receipt)
	if err != nil {
		t.Fatalf("AppendAssignmentDispatchReceipt first call: %v", err)
	}
	second, err := repo.AppendAssignmentDispatchReceipt(ctx, receipt)
	if err != nil {
		t.Fatalf("AppendAssignmentDispatchReceipt exact replay: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("assignment command exact replay changed receipt:\nfirst=%+v\nsecond=%+v", first, second)
	}

	conflict := receipt
	conflict.Target.InputDigest = assignmentDigest("8")
	if _, err := repo.AppendAssignmentDispatchReceipt(ctx, conflict); !errors.Is(err, ErrCompanyOpsAssignmentConflict) {
		t.Fatalf("conflicting assignment payload error = %v, want ErrCompanyOpsAssignmentConflict", err)
	}
}

func TestCompanyOpsPersistence_ExecutionReceiptClaimAndTerminalAreImmutable(t *testing.T) {
	ctx, tx := beginCompanyOpsPersistenceTestTx(t)
	repo := NewCompanyOpsPersistenceRepository(tx)
	claim := ExecutionReceiptClaimSnapshot{
		TaskID:              persistenceUUID(20),
		WorkspaceID:         persistenceUUID(21),
		IssueID:             persistenceUUID(22),
		AssignmentCommandID: persistenceUUID(23),
		Target:              persistenceTarget(),
		RuntimeSnapshot: json.RawMessage(`{
			"runtime_ref":"hivecosm://runtimes/RUNTIME-PERSISTENCE-001",
			"harness_ref":"hivecosm://harnesses/HARNESS-PERSISTENCE-001",
			"model_ref":"litellm://models/MODEL-PERSISTENCE-001",
			"endpoint_ref":"litellm://endpoints/ENDPOINT-PERSISTENCE-001",
			"capacity_ref":"hivecosm://capacity-leases/LEASE-PERSISTENCE-001"
		}`),
		RuntimeDigest: assignmentDigest("6"),
		ClaimedAt:     time.Date(2026, 8, 11, 8, 31, 0, 0, time.UTC),
	}

	first, err := repo.CreateExecutionReceiptClaim(ctx, claim)
	if err != nil {
		t.Fatalf("CreateExecutionReceiptClaim first call: %v", err)
	}
	second, err := repo.CreateExecutionReceiptClaim(ctx, claim)
	if err != nil {
		t.Fatalf("CreateExecutionReceiptClaim exact replay: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("execution claim exact replay changed receipt:\nfirst=%+v\nsecond=%+v", first, second)
	}

	claimConflict := claim
	claimConflict.Target.WorkOrderRevision = "wo-rev-conflict"
	if _, err := repo.CreateExecutionReceiptClaim(ctx, claimConflict); !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("same task_id with different claim error = %v, want ErrExecutionReceiptConflict", err)
	}

	terminal := ExecutionReceiptTerminal{
		TaskID:         claim.TaskID,
		Status:         "completed",
		CompletedAt:    time.Date(2026, 8, 11, 8, 35, 0, 0, time.UTC),
		OutputDigest:   assignmentDigest("7"),
		ResultSnapshot: json.RawMessage(`{"artifact_refs":["hivecrew://artifacts/ART-PERSISTENCE-001"]}`),
	}
	finalized, err := repo.FinalizeExecutionReceipt(ctx, terminal)
	if err != nil {
		t.Fatalf("FinalizeExecutionReceipt first call: %v", err)
	}
	replayed, err := repo.FinalizeExecutionReceipt(ctx, terminal)
	if err != nil {
		t.Fatalf("FinalizeExecutionReceipt exact replay: %v", err)
	}
	if !reflect.DeepEqual(replayed, finalized) {
		t.Fatalf("terminal exact replay changed receipt:\nfirst=%+v\nsecond=%+v", finalized, replayed)
	}

	terminalConflict := terminal
	terminalConflict.OutputDigest = assignmentDigest("0")
	if _, err := repo.FinalizeExecutionReceipt(ctx, terminalConflict); !errors.Is(err, ErrExecutionReceiptConflict) {
		t.Fatalf("different terminal replay error = %v, want ErrExecutionReceiptConflict", err)
	}

	stored, err := repo.GetExecutionReceipt(ctx, claim.TaskID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt after finalize: %v", err)
	}
	if !reflect.DeepEqual(stored.Claim, claim) {
		t.Fatalf("terminal finalize mutated claim snapshot:\ngot=%+v\nwant=%+v", stored.Claim, claim)
	}
	if stored.Terminal == nil || !reflect.DeepEqual(*stored.Terminal, terminal) {
		t.Fatalf("stored terminal = %+v, want exactly %+v", stored.Terminal, terminal)
	}
}

func TestCompanyOpsPersistence_LedgersHaveNoForeignKeysOrCascades(t *testing.T) {
	ctx, tx := beginCompanyOpsPersistenceTestTx(t)
	repo := NewCompanyOpsPersistenceRepository(tx)

	// These Issue and Task UUIDs intentionally have no parent rows. Successful
	// persistence proves the ledger does not acquire parent existence semantics;
	// the catalog assertion below proves no later parent delete can cascade into it.
	link := ExternalWorkOrderLink{
		WorkspaceID:      persistenceUUID(30),
		WorkOrderRef:     "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-DANGLING-PARENT-001",
		LinkedRevision:   "wo-rev-1",
		LinkedDigest:     assignmentDigest("a"),
		SourceObservedAt: time.Date(2026, 8, 11, 8, 40, 0, 0, time.UTC),
		FreshnessAtLink:  "current",
		IssueID:          persistenceUUID(31),
	}
	if _, err := repo.EnsureExternalWorkOrderLink(ctx, link); err != nil {
		t.Fatalf("persist link with non-existent issue id: %v", err)
	}
	claim := ExecutionReceiptClaimSnapshot{
		TaskID:              persistenceUUID(32),
		WorkspaceID:         link.WorkspaceID,
		IssueID:             link.IssueID,
		AssignmentCommandID: persistenceUUID(33),
		Target:              persistenceTarget(),
		RuntimeSnapshot:     json.RawMessage(`{"runtime_ref":"hivecosm://runtimes/RUNTIME-DANGLING-001"}`),
		RuntimeDigest:       assignmentDigest("b"),
		ClaimedAt:           time.Date(2026, 8, 11, 8, 41, 0, 0, time.UTC),
	}
	if _, err := repo.CreateExecutionReceiptClaim(ctx, claim); err != nil {
		t.Fatalf("persist receipt with non-existent task id: %v", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT conrelid::regclass::text, confrelid::regclass::text, confdeltype::text
		FROM pg_constraint
		WHERE contype = 'f'
		  AND conrelid IN (
			to_regclass('external_work_order_link'),
			to_regclass('assignment_dispatch_receipt'),
			to_regclass('execution_receipt')
		  )
	`)
	if err != nil {
		t.Fatalf("inspect CompanyOps ledger constraints: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tableName, parentTable, deleteAction string
		if err := rows.Scan(&tableName, &parentTable, &deleteAction); err != nil {
			t.Fatalf("scan CompanyOps ledger constraint: %v", err)
		}
		t.Errorf("CompanyOps ledger %s has forbidden FK to %s (delete action %s)", tableName, parentTable, deleteAction)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate CompanyOps ledger constraints: %v", err)
	}

	stored, err := repo.GetExecutionReceipt(ctx, claim.TaskID)
	if err != nil {
		t.Fatalf("GetExecutionReceipt with absent parent row: %v", err)
	}
	if !reflect.DeepEqual(stored.Claim, claim) {
		t.Fatalf("absent parent row changed execution receipt: got=%+v want=%+v", stored.Claim, claim)
	}
}
