package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ErrCompanyOpsIssueNotAssignable means the exact assignment compare-and-swap
// found no active, wholly-unassigned Issue in the requested workspace.
var ErrCompanyOpsIssueNotAssignable = errors.New("companyops issue is not exactly assignable")

// CanonicalCompanyOpsAssignmentBackendDeps is the injectable boundary between
// the assignment coordinator and future canonical Issue/Task writers. Every
// transaction-local function receives the exact *db.Queries supplied by
// WithQTx; the two effect functions are invoked by the coordinator only after
// WithQTx has returned successfully.
type CanonicalCompanyOpsAssignmentBackendDeps struct {
	WithQTx func(context.Context, func(*db.Queries) error) error

	LockAssignmentCommand func(
		context.Context,
		*db.Queries,
		pgtype.UUID,
		pgtype.UUID,
	) error
	GetAssignmentDispatchReceipt func(
		context.Context,
		*db.Queries,
		pgtype.UUID,
		pgtype.UUID,
	) (AssignmentDispatchReceipt, bool, error)
	AssignIssueExact func(
		context.Context,
		*db.Queries,
		CompanyOpsAssignmentRequest,
		companyops.ExecutionTargetSnapshot,
	) (db.Issue, error)
	PrepareAssignmentTask func(
		context.Context,
		*db.Queries,
		db.Issue,
		CompanyOpsAssignmentRequest,
		companyops.ExecutionTargetSnapshot,
		string,
		pgtype.UUID,
	) (db.AgentTaskQueue, error)
	AppendAssignmentDispatchReceipt func(
		context.Context,
		*db.Queries,
		AssignmentDispatchReceipt,
	) error

	PublishAssignmentDispatched   func(context.Context, AssignmentDispatchReceipt)
	NotifyAssignmentTaskAvailable func(context.Context, db.AgentTaskQueue)
}

// canonicalCompanyOpsAssignmentBackend adapts injected canonical-writer seams
// to CompanyOpsAssignmentBackend. It does not itself own an Issue or Task
// writer; callers must inject those integrations explicitly.
type canonicalCompanyOpsAssignmentBackend struct {
	deps CanonicalCompanyOpsAssignmentBackendDeps
}

// NewCanonicalCompanyOpsAssignmentBackend validates the complete adapter
// boundary up front so an assignment can never enter a transaction with a
// partially wired writer or post-commit effect.
func NewCanonicalCompanyOpsAssignmentBackend(
	deps CanonicalCompanyOpsAssignmentBackendDeps,
) (*canonicalCompanyOpsAssignmentBackend, error) {
	required := map[string]bool{
		"WithQTx":                         deps.WithQTx != nil,
		"LockAssignmentCommand":           deps.LockAssignmentCommand != nil,
		"GetAssignmentDispatchReceipt":    deps.GetAssignmentDispatchReceipt != nil,
		"AssignIssueExact":                deps.AssignIssueExact != nil,
		"PrepareAssignmentTask":           deps.PrepareAssignmentTask != nil,
		"AppendAssignmentDispatchReceipt": deps.AppendAssignmentDispatchReceipt != nil,
		"PublishAssignmentDispatched":     deps.PublishAssignmentDispatched != nil,
		"NotifyAssignmentTaskAvailable":   deps.NotifyAssignmentTaskAvailable != nil,
	}
	for name, ok := range required {
		if !ok {
			return nil, fmt.Errorf("canonical companyops assignment backend dependency %s is required", name)
		}
	}
	return &canonicalCompanyOpsAssignmentBackend{deps: deps}, nil
}

// NewProductionCompanyOpsAssignmentBackend wires the injectable adapter to the
// dedicated CompanyOps SQLC queries and TaskService's transaction-local issue
// task prepare helper. It does not register a handler or establish an authority
// transport; callers still own those outer boundaries.
func NewProductionCompanyOpsAssignmentBackend(
	queries *db.Queries,
	txStarter TxStarter,
	taskService *TaskService,
) (*canonicalCompanyOpsAssignmentBackend, error) {
	if queries == nil {
		return nil, fmt.Errorf("companyops assignment queries are required")
	}
	if txStarter == nil {
		return nil, fmt.Errorf("companyops assignment transaction starter is required")
	}
	if taskService == nil {
		return nil, fmt.Errorf("companyops assignment task service is required")
	}
	if taskService.Queries == nil {
		return nil, fmt.Errorf("companyops assignment task service queries are required")
	}
	if taskService.Bus == nil {
		return nil, fmt.Errorf("companyops assignment task event bus is required")
	}

	return NewCanonicalCompanyOpsAssignmentBackend(CanonicalCompanyOpsAssignmentBackendDeps{
		WithQTx: func(ctx context.Context, fn func(*db.Queries) error) error {
			tx, err := txStarter.Begin(ctx)
			if err != nil {
				return fmt.Errorf("begin companyops assignment transaction: %w", err)
			}
			committed := false
			defer func() {
				if !committed {
					_ = tx.Rollback(context.Background())
				}
			}()

			if err := fn(queries.WithTx(tx)); err != nil {
				return err
			}
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit companyops assignment transaction: %w", err)
			}
			committed = true
			return nil
		},
		LockAssignmentCommand: func(
			ctx context.Context,
			qtx *db.Queries,
			workspaceID, commandID pgtype.UUID,
		) error {
			return qtx.LockCompanyOpsAssignmentCommand(ctx, db.LockCompanyOpsAssignmentCommandParams{
				WorkspaceID: workspaceID,
				CommandID:   commandID,
			})
		},
		GetAssignmentDispatchReceipt: func(
			ctx context.Context,
			qtx *db.Queries,
			workspaceID, commandID pgtype.UUID,
		) (AssignmentDispatchReceipt, bool, error) {
			repository := NewCompanyOpsPersistenceRepositoryWithQueries(qtx)
			return repository.GetAssignmentDispatchReceipt(ctx, workspaceID, commandID)
		},
		AssignIssueExact: func(
			ctx context.Context,
			qtx *db.Queries,
			req CompanyOpsAssignmentRequest,
			_ companyops.ExecutionTargetSnapshot,
		) (db.Issue, error) {
			issue, err := qtx.AssignIssueAgentExact(ctx, db.AssignIssueAgentExactParams{
				AgentID:     req.LocalAgentID,
				WorkspaceID: req.WorkspaceID,
				IssueID:     req.IssueID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return db.Issue{}, ErrCompanyOpsIssueNotAssignable
			}
			if err != nil {
				return db.Issue{}, fmt.Errorf("assign companyops issue exact: %w", err)
			}
			return issue, nil
		},
		PrepareAssignmentTask: func(
			ctx context.Context,
			qtx *db.Queries,
			issue db.Issue,
			req CompanyOpsAssignmentRequest,
			_ companyops.ExecutionTargetSnapshot,
			evidenceKind string,
			evidenceRefID pgtype.UUID,
		) (db.AgentTaskQueue, error) {
			if evidenceKind != assignmentDispatchEvidenceKind || evidenceRefID != req.CommandID {
				return db.AgentTaskQueue{}, fmt.Errorf("companyops assignment task evidence must match command")
			}
			return taskService.prepareIssueTaskWithCommentPlan(
				ctx,
				qtx,
				issue,
				pgtype.UUID{},
				nil,
				false,
				req.HandoffNote,
				req.ActorUserID,
				pgtype.UUID{},
				&issueTaskTriggerEvidenceOverride{
					Kind:  assignmentDispatchEvidenceKind,
					RefID: req.CommandID,
				},
			)
		},
		AppendAssignmentDispatchReceipt: func(
			ctx context.Context,
			qtx *db.Queries,
			receipt AssignmentDispatchReceipt,
		) error {
			repository := NewCompanyOpsPersistenceRepositoryWithQueries(qtx)
			stored, err := repository.AppendAssignmentDispatchReceipt(ctx, receipt)
			if err != nil {
				return err
			}
			if stored != receipt {
				return ErrCompanyOpsAssignmentConflict
			}
			return nil
		},
		PublishAssignmentDispatched: func(context.Context, AssignmentDispatchReceipt) {
			// Assignment receipts currently have no dedicated realtime event.
			// Never synthesize a partial task snapshot from the receipt; the
			// following callback receives the complete committed task.
		},
		NotifyAssignmentTaskAvailable: func(ctx context.Context, task db.AgentTaskQueue) {
			taskService.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
			taskService.NotifyTaskEnqueued(ctx, task)
		},
	})
}

func (b *canonicalCompanyOpsAssignmentBackend) RunInCompanyOpsAssignmentTx(
	ctx context.Context,
	fn func(CompanyOpsAssignmentTx) error,
) error {
	if b == nil {
		return fmt.Errorf("canonical companyops assignment backend is required")
	}
	if fn == nil {
		return fmt.Errorf("companyops assignment transaction callback is required")
	}
	return b.deps.WithQTx(ctx, func(qtx *db.Queries) error {
		if qtx == nil {
			return fmt.Errorf("canonical companyops assignment qtx is required")
		}
		return fn(&canonicalCompanyOpsAssignmentTx{deps: b.deps, qtx: qtx})
	})
}

func (b *canonicalCompanyOpsAssignmentBackend) PublishAssignmentDispatched(
	ctx context.Context,
	receipt AssignmentDispatchReceipt,
) {
	b.deps.PublishAssignmentDispatched(ctx, receipt)
}

func (b *canonicalCompanyOpsAssignmentBackend) NotifyAssignmentTaskAvailable(
	ctx context.Context,
	task db.AgentTaskQueue,
) {
	b.deps.NotifyAssignmentTaskAvailable(ctx, task)
}

type canonicalCompanyOpsAssignmentTx struct {
	deps          CanonicalCompanyOpsAssignmentBackendDeps
	qtx           *db.Queries
	assignedIssue db.Issue
	assigned      bool
}

func (tx *canonicalCompanyOpsAssignmentTx) LockAssignmentCommand(
	ctx context.Context,
	workspaceID, commandID pgtype.UUID,
) error {
	return tx.deps.LockAssignmentCommand(ctx, tx.qtx, workspaceID, commandID)
}

func (tx *canonicalCompanyOpsAssignmentTx) GetAssignmentDispatchReceipt(
	ctx context.Context,
	workspaceID, commandID pgtype.UUID,
) (AssignmentDispatchReceipt, bool, error) {
	return tx.deps.GetAssignmentDispatchReceipt(ctx, tx.qtx, workspaceID, commandID)
}

func (tx *canonicalCompanyOpsAssignmentTx) AssignIssueExact(
	ctx context.Context,
	req CompanyOpsAssignmentRequest,
	target companyops.ExecutionTargetSnapshot,
) error {
	issue, err := tx.deps.AssignIssueExact(ctx, tx.qtx, req, target)
	if err != nil {
		return err
	}
	if issue.WorkspaceID != req.WorkspaceID || issue.ID != req.IssueID ||
		!issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" ||
		issue.AssigneeID != req.LocalAgentID {
		return fmt.Errorf("canonical issue assignment result does not match exact workspace, issue, and agent")
	}
	tx.assignedIssue = issue
	tx.assigned = true
	return nil
}

func (tx *canonicalCompanyOpsAssignmentTx) EnqueueAssignmentTask(
	ctx context.Context,
	req CompanyOpsAssignmentRequest,
	target companyops.ExecutionTargetSnapshot,
	evidenceKind string,
	evidenceRefID pgtype.UUID,
) (db.AgentTaskQueue, error) {
	if !tx.assigned {
		return db.AgentTaskQueue{}, fmt.Errorf("canonical issue assignment must complete before task prepare")
	}
	task, err := tx.deps.PrepareAssignmentTask(
		ctx,
		tx.qtx,
		tx.assignedIssue,
		req,
		target,
		evidenceKind,
		evidenceRefID,
	)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if !task.ID.Valid || task.ID.Bytes == ([16]byte{}) ||
		task.IssueID != req.IssueID || task.AgentID != req.LocalAgentID ||
		task.Status != "queued" ||
		!task.TriggerEvidenceKind.Valid || task.TriggerEvidenceKind.String != evidenceKind ||
		task.TriggerEvidenceRefID != evidenceRefID {
		return db.AgentTaskQueue{}, fmt.Errorf("canonical task prepare result does not match exact assignment evidence")
	}
	return task, nil
}

func (tx *canonicalCompanyOpsAssignmentTx) AppendAssignmentDispatchReceipt(
	ctx context.Context,
	receipt AssignmentDispatchReceipt,
) error {
	return tx.deps.AppendAssignmentDispatchReceipt(ctx, tx.qtx, receipt)
}

var _ CompanyOpsAssignmentBackend = (*canonicalCompanyOpsAssignmentBackend)(nil)
var _ CompanyOpsAssignmentTx = (*canonicalCompanyOpsAssignmentTx)(nil)
