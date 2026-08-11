package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrExternalWorkOrderLinkConflict = errors.New("external WorkOrder link payload conflict")
	ErrExecutionReceiptConflict      = errors.New("execution receipt payload conflict")
	ErrExecutionReceiptNotFound      = errors.New("execution receipt claim not found")
)

// ExternalWorkOrderLink is a provenance-only link from an authoritative
// WorkOrder reference to its local HiveCrew Issue projection. It deliberately
// carries no WorkOrder title, status, Project, or lifecycle state.
type ExternalWorkOrderLink struct {
	WorkspaceID      pgtype.UUID
	WorkOrderRef     string
	LinkedRevision   string
	LinkedDigest     string
	SourceObservedAt time.Time
	FreshnessAtLink  string
	IssueID          pgtype.UUID
}

// ExecutionReceiptClaimSnapshot freezes the exact execution target and runtime
// evidence at claim time. Finalization never rewrites these fields.
type ExecutionReceiptClaimSnapshot struct {
	TaskID              pgtype.UUID
	WorkspaceID         pgtype.UUID
	IssueID             pgtype.UUID
	AssignmentCommandID pgtype.UUID
	Target              companyops.ExecutionTargetSnapshot
	RuntimeSnapshot     json.RawMessage
	RuntimeDigest       string
	ClaimedAt           time.Time
}

// ExecutionReceiptTerminal is the single terminal outcome allowed for a Run.
// Replaying the exact value is idempotent; any different value conflicts.
type ExecutionReceiptTerminal struct {
	TaskID         pgtype.UUID
	Status         string
	CompletedAt    time.Time
	OutputDigest   string
	ResultSnapshot json.RawMessage
	Error          string
}

// ExecutionReceipt is the immutable claim plus its optional one-way terminal
// finalization.
type ExecutionReceipt struct {
	Claim    ExecutionReceiptClaimSnapshot
	Terminal *ExecutionReceiptTerminal
}

// CompanyOpsPersistenceRepository persists HiveCrew-owned links and receipts
// through SQLC queries bound to the caller's transaction.
type CompanyOpsPersistenceRepository struct {
	queries *db.Queries
}

func NewCompanyOpsPersistenceRepository(tx pgx.Tx) *CompanyOpsPersistenceRepository {
	return &CompanyOpsPersistenceRepository{queries: db.New(tx)}
}

// NewCompanyOpsPersistenceRepositoryWithQueries binds the repository to the
// caller's query handle. Production assignment wiring passes a transaction-
// bound *db.Queries so receipt reads and appends share the Issue/Task qtx.
func NewCompanyOpsPersistenceRepositoryWithQueries(queries *db.Queries) *CompanyOpsPersistenceRepository {
	return &CompanyOpsPersistenceRepository{queries: queries}
}

func (r *CompanyOpsPersistenceRepository) EnsureExternalWorkOrderLink(
	ctx context.Context,
	link ExternalWorkOrderLink,
) (ExternalWorkOrderLink, error) {
	row, err := r.queries.InsertExternalWorkOrderLink(ctx, db.InsertExternalWorkOrderLinkParams{
		WorkspaceID:      link.WorkspaceID,
		WorkOrderRef:     link.WorkOrderRef,
		LinkedRevision:   link.LinkedRevision,
		LinkedDigest:     link.LinkedDigest,
		SourceObservedAt: timestamptz(link.SourceObservedAt),
		FreshnessAtLink:  link.FreshnessAtLink,
		IssueID:          link.IssueID,
	})
	if err == nil {
		return externalWorkOrderLinkFromDB(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ExternalWorkOrderLink{}, fmt.Errorf("insert external WorkOrder link: %w", err)
	}

	existing, err := r.queries.GetExternalWorkOrderLink(ctx, db.GetExternalWorkOrderLinkParams{
		WorkspaceID:  link.WorkspaceID,
		WorkOrderRef: link.WorkOrderRef,
	})
	if err != nil {
		return ExternalWorkOrderLink{}, fmt.Errorf("reload external WorkOrder link: %w", err)
	}
	stored := externalWorkOrderLinkFromDB(existing)
	if !externalWorkOrderLinksEqual(stored, link) {
		return ExternalWorkOrderLink{}, ErrExternalWorkOrderLinkConflict
	}
	return stored, nil
}

func (r *CompanyOpsPersistenceRepository) AppendAssignmentDispatchReceipt(
	ctx context.Context,
	receipt AssignmentDispatchReceipt,
) (AssignmentDispatchReceipt, error) {
	row, err := r.queries.InsertAssignmentDispatchReceipt(ctx, assignmentDispatchReceiptParams(receipt))
	if err == nil {
		return assignmentDispatchReceiptFromDB(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AssignmentDispatchReceipt{}, fmt.Errorf("insert assignment dispatch receipt: %w", err)
	}

	existing, found, err := r.GetAssignmentDispatchReceipt(ctx, receipt.WorkspaceID, receipt.CommandID)
	if err != nil {
		return AssignmentDispatchReceipt{}, fmt.Errorf("reload assignment dispatch receipt: %w", err)
	}
	if !found || existing != receipt {
		return AssignmentDispatchReceipt{}, ErrCompanyOpsAssignmentConflict
	}
	return existing, nil
}

func (r *CompanyOpsPersistenceRepository) GetAssignmentDispatchReceipt(
	ctx context.Context,
	workspaceID, commandID pgtype.UUID,
) (AssignmentDispatchReceipt, bool, error) {
	if r == nil || r.queries == nil {
		return AssignmentDispatchReceipt{}, false, fmt.Errorf("companyops persistence queries are required")
	}
	row, err := r.queries.GetAssignmentDispatchReceipt(ctx, db.GetAssignmentDispatchReceiptParams{
		WorkspaceID: workspaceID,
		CommandID:   commandID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AssignmentDispatchReceipt{}, false, nil
	}
	if err != nil {
		return AssignmentDispatchReceipt{}, false, fmt.Errorf("get assignment dispatch receipt: %w", err)
	}
	return assignmentDispatchReceiptFromDB(row), true, nil
}

func (r *CompanyOpsPersistenceRepository) CreateExecutionReceiptClaim(
	ctx context.Context,
	claim ExecutionReceiptClaimSnapshot,
) (ExecutionReceipt, error) {
	if !isJSONObject(claim.RuntimeSnapshot) {
		return ExecutionReceipt{}, fmt.Errorf("runtime snapshot must be a JSON object")
	}
	row, err := r.queries.InsertExecutionReceiptClaim(ctx, executionReceiptClaimParams(claim))
	if err == nil {
		return executionReceiptFromDB(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ExecutionReceipt{}, fmt.Errorf("insert execution receipt claim: %w", err)
	}

	existing, err := r.queries.GetExecutionReceipt(ctx, claim.TaskID)
	if err != nil {
		return ExecutionReceipt{}, fmt.Errorf("reload execution receipt claim: %w", err)
	}
	stored := executionReceiptFromDB(existing)
	if !executionClaimsEqual(stored.Claim, claim) {
		return ExecutionReceipt{}, ErrExecutionReceiptConflict
	}
	return stored, nil
}

func (r *CompanyOpsPersistenceRepository) FinalizeExecutionReceipt(
	ctx context.Context,
	terminal ExecutionReceiptTerminal,
) (ExecutionReceipt, error) {
	if len(terminal.ResultSnapshot) > 0 && !isJSONObject(terminal.ResultSnapshot) {
		return ExecutionReceipt{}, fmt.Errorf("result snapshot must be a JSON object")
	}
	row, err := r.queries.FinalizeExecutionReceipt(ctx, db.FinalizeExecutionReceiptParams{
		TerminalStatus: pgtype.Text{String: terminal.Status, Valid: terminal.Status != ""},
		CompletedAt:    timestamptz(terminal.CompletedAt),
		OutputDigest:   pgtype.Text{String: terminal.OutputDigest, Valid: terminal.OutputDigest != ""},
		ResultSnapshot: cloneBytes(terminal.ResultSnapshot),
		TerminalError:  pgtype.Text{String: terminal.Error, Valid: terminal.Error != ""},
		TaskID:         terminal.TaskID,
	})
	if err == nil {
		return executionReceiptFromDB(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ExecutionReceipt{}, fmt.Errorf("finalize execution receipt: %w", err)
	}

	existing, err := r.queries.GetExecutionReceipt(ctx, terminal.TaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExecutionReceipt{}, ErrExecutionReceiptNotFound
	}
	if err != nil {
		return ExecutionReceipt{}, fmt.Errorf("reload finalized execution receipt: %w", err)
	}
	stored := executionReceiptFromDB(existing)
	if stored.Terminal == nil || !executionTerminalsEqual(*stored.Terminal, terminal) {
		return ExecutionReceipt{}, ErrExecutionReceiptConflict
	}
	return stored, nil
}

func (r *CompanyOpsPersistenceRepository) GetExecutionReceipt(
	ctx context.Context,
	taskID pgtype.UUID,
) (ExecutionReceipt, error) {
	row, err := r.queries.GetExecutionReceipt(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExecutionReceipt{}, ErrExecutionReceiptNotFound
	}
	if err != nil {
		return ExecutionReceipt{}, fmt.Errorf("get execution receipt: %w", err)
	}
	return executionReceiptFromDB(row), nil
}

func assignmentDispatchReceiptParams(receipt AssignmentDispatchReceipt) db.InsertAssignmentDispatchReceiptParams {
	return db.InsertAssignmentDispatchReceiptParams{
		CommandID:         receipt.CommandID,
		WorkspaceID:       receipt.WorkspaceID,
		IssueID:           receipt.IssueID,
		LocalAgentID:      receipt.LocalAgentID,
		InitialTaskID:     receipt.InitialTaskID,
		WorkOrderRef:      receipt.Target.WorkOrderRef,
		WorkOrderRevision: receipt.Target.WorkOrderRevision,
		WorkOrderDigest:   receipt.Target.WorkOrderDigest,
		InputDigest:       receipt.Target.InputDigest,
		EmployeeRef:       receipt.Target.EmployeeRef,
		EmployeeRevision:  receipt.Target.EmployeeRevision,
		EmployeeDigest:    receipt.Target.EmployeeDigest,
		BindingRef:        receipt.Target.BindingRef,
		BindingRevision:   receipt.Target.BindingRevision,
		BindingDigest:     receipt.Target.BindingDigest,
		AgentRef:          receipt.Target.AgentRef,
		AgentRevision:     receipt.Target.AgentRevision,
		AgentDigest:       receipt.Target.AgentDigest,
	}
}

func assignmentDispatchReceiptFromDB(row db.AssignmentDispatchReceipt) AssignmentDispatchReceipt {
	return AssignmentDispatchReceipt{
		CommandID:     row.CommandID,
		WorkspaceID:   row.WorkspaceID,
		IssueID:       row.IssueID,
		LocalAgentID:  row.LocalAgentID,
		InitialTaskID: row.InitialTaskID,
		Target: companyops.ExecutionTargetSnapshot{
			WorkOrderRef:      row.WorkOrderRef,
			WorkOrderRevision: row.WorkOrderRevision,
			WorkOrderDigest:   row.WorkOrderDigest,
			InputDigest:       row.InputDigest,
			EmployeeRef:       row.EmployeeRef,
			EmployeeRevision:  row.EmployeeRevision,
			EmployeeDigest:    row.EmployeeDigest,
			BindingRef:        row.BindingRef,
			BindingRevision:   row.BindingRevision,
			BindingDigest:     row.BindingDigest,
			AgentRef:          row.AgentRef,
			AgentRevision:     row.AgentRevision,
			AgentDigest:       row.AgentDigest,
		},
	}
}

func executionReceiptClaimParams(claim ExecutionReceiptClaimSnapshot) db.InsertExecutionReceiptClaimParams {
	return db.InsertExecutionReceiptClaimParams{
		TaskID:              claim.TaskID,
		WorkspaceID:         claim.WorkspaceID,
		IssueID:             claim.IssueID,
		AssignmentCommandID: claim.AssignmentCommandID,
		WorkOrderRef:        claim.Target.WorkOrderRef,
		WorkOrderRevision:   claim.Target.WorkOrderRevision,
		WorkOrderDigest:     claim.Target.WorkOrderDigest,
		InputDigest:         claim.Target.InputDigest,
		EmployeeRef:         claim.Target.EmployeeRef,
		EmployeeRevision:    claim.Target.EmployeeRevision,
		EmployeeDigest:      claim.Target.EmployeeDigest,
		BindingRef:          claim.Target.BindingRef,
		BindingRevision:     claim.Target.BindingRevision,
		BindingDigest:       claim.Target.BindingDigest,
		AgentRef:            claim.Target.AgentRef,
		AgentRevision:       claim.Target.AgentRevision,
		AgentDigest:         claim.Target.AgentDigest,
		RuntimeSnapshot:     cloneBytes(claim.RuntimeSnapshot),
		RuntimeDigest:       claim.RuntimeDigest,
		ClaimedAt:           timestamptz(claim.ClaimedAt),
	}
}

func executionReceiptFromDB(row db.ExecutionReceipt) ExecutionReceipt {
	receipt := ExecutionReceipt{
		Claim: ExecutionReceiptClaimSnapshot{
			TaskID:              row.TaskID,
			WorkspaceID:         row.WorkspaceID,
			IssueID:             row.IssueID,
			AssignmentCommandID: row.AssignmentCommandID,
			Target: companyops.ExecutionTargetSnapshot{
				WorkOrderRef:      row.WorkOrderRef,
				WorkOrderRevision: row.WorkOrderRevision,
				WorkOrderDigest:   row.WorkOrderDigest,
				InputDigest:       row.InputDigest,
				EmployeeRef:       row.EmployeeRef,
				EmployeeRevision:  row.EmployeeRevision,
				EmployeeDigest:    row.EmployeeDigest,
				BindingRef:        row.BindingRef,
				BindingRevision:   row.BindingRevision,
				BindingDigest:     row.BindingDigest,
				AgentRef:          row.AgentRef,
				AgentRevision:     row.AgentRevision,
				AgentDigest:       row.AgentDigest,
			},
			RuntimeSnapshot: cloneBytes(row.RuntimeSnapshot),
			RuntimeDigest:   row.RuntimeDigest,
			ClaimedAt:       row.ClaimedAt.Time.UTC(),
		},
	}
	if row.TerminalStatus.Valid {
		receipt.Terminal = &ExecutionReceiptTerminal{
			TaskID:         row.TaskID,
			Status:         row.TerminalStatus.String,
			CompletedAt:    row.CompletedAt.Time.UTC(),
			OutputDigest:   row.OutputDigest.String,
			ResultSnapshot: cloneBytes(row.ResultSnapshot),
			Error:          row.TerminalError.String,
		}
	}
	return receipt
}

func externalWorkOrderLinkFromDB(row db.ExternalWorkOrderLink) ExternalWorkOrderLink {
	return ExternalWorkOrderLink{
		WorkspaceID:      row.WorkspaceID,
		WorkOrderRef:     row.WorkOrderRef,
		LinkedRevision:   row.LinkedRevision,
		LinkedDigest:     row.LinkedDigest,
		SourceObservedAt: row.SourceObservedAt.Time.UTC(),
		FreshnessAtLink:  row.FreshnessAtLink,
		IssueID:          row.IssueID,
	}
}

func externalWorkOrderLinksEqual(a, b ExternalWorkOrderLink) bool {
	return a.WorkspaceID == b.WorkspaceID &&
		a.WorkOrderRef == b.WorkOrderRef &&
		a.LinkedRevision == b.LinkedRevision &&
		a.LinkedDigest == b.LinkedDigest &&
		a.SourceObservedAt.Equal(b.SourceObservedAt) &&
		a.FreshnessAtLink == b.FreshnessAtLink &&
		a.IssueID == b.IssueID
}

func executionClaimsEqual(a, b ExecutionReceiptClaimSnapshot) bool {
	return a.TaskID == b.TaskID &&
		a.WorkspaceID == b.WorkspaceID &&
		a.IssueID == b.IssueID &&
		a.AssignmentCommandID == b.AssignmentCommandID &&
		a.Target == b.Target &&
		bytes.Equal(a.RuntimeSnapshot, b.RuntimeSnapshot) &&
		a.RuntimeDigest == b.RuntimeDigest &&
		a.ClaimedAt.Equal(b.ClaimedAt)
}

func executionTerminalsEqual(a, b ExecutionReceiptTerminal) bool {
	return a.TaskID == b.TaskID &&
		a.Status == b.Status &&
		a.CompletedAt.Equal(b.CompletedAt) &&
		a.OutputDigest == b.OutputDigest &&
		bytes.Equal(a.ResultSnapshot, b.ResultSnapshot) &&
		a.Error == b.Error
}

func timestamptz(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: !value.IsZero()}
}

func isJSONObject(value []byte) bool {
	var object map[string]json.RawMessage
	return len(value) > 0 && json.Unmarshal(value, &object) == nil && object != nil
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
