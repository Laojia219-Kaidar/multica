package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ProductionContinuousDispatchBackend reuses canonical Issue reads,
// TaskService task preparation, SQLC receipts, and post-commit task effects.
// It does not expose an API route or start an autonomous scheduler by itself.
type ProductionContinuousDispatchBackend struct {
	queries     *db.Queries
	txStarter   TxStarter
	taskService *TaskService
}

func NewProductionContinuousDispatchBackend(
	queries *db.Queries,
	txStarter TxStarter,
	taskService *TaskService,
) (*ProductionContinuousDispatchBackend, error) {
	if queries == nil {
		return nil, fmt.Errorf("continuous dispatch queries are required")
	}
	if txStarter == nil {
		return nil, fmt.Errorf("continuous dispatch transaction starter is required")
	}
	if taskService == nil || taskService.Queries == nil || taskService.Bus == nil {
		return nil, fmt.Errorf("continuous dispatch task service with queries and event bus is required")
	}
	return &ProductionContinuousDispatchBackend{queries: queries, txStarter: txStarter, taskService: taskService}, nil
}

func (b *ProductionContinuousDispatchBackend) RunInContinuousDispatchTx(
	ctx context.Context,
	fn func(ContinuousDispatchTx) error,
) error {
	if b == nil || b.queries == nil || b.txStarter == nil || b.taskService == nil {
		return fmt.Errorf("production continuous dispatch backend is incomplete")
	}
	if fn == nil {
		return fmt.Errorf("continuous dispatch transaction callback is required")
	}
	tx, err := b.txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin continuous dispatch transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	qtx := b.queries.WithTx(tx)
	if err := fn(&productionContinuousDispatchTx{queries: qtx, taskService: b.taskService}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit continuous dispatch transaction: %w", err)
	}
	committed = true
	return nil
}

func (b *ProductionContinuousDispatchBackend) NotifyContinuousDispatchTask(
	ctx context.Context,
	task db.AgentTaskQueue,
) {
	if b == nil || b.taskService == nil {
		return
	}
	b.taskService.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	b.taskService.NotifyTaskEnqueued(ctx, task)
}

type productionContinuousDispatchTx struct {
	queries     *db.Queries
	taskService *TaskService
}

func (tx *productionContinuousDispatchTx) LockIdentity(
	ctx context.Context,
	identity continuousdispatch.DispatchIdentity,
) error {
	return tx.queries.LockContinuousDispatchIdentity(ctx, db.LockContinuousDispatchIdentityParams{
		WorkspaceID: parseDispatchUUID(identity.WorkspaceID), IssueID: parseDispatchUUID(identity.IssueID),
		Stage: identity.Stage, CandidateRevision: identity.CandidateRevision, Generation: identity.Generation,
	})
}

func (tx *productionContinuousDispatchTx) GetReceipt(
	ctx context.Context,
	identity continuousdispatch.DispatchIdentity,
) (ContinuousDispatchReceipt, bool, error) {
	return getContinuousDispatchReceipt(ctx, tx.queries, identity)
}

func (tx *productionContinuousDispatchTx) LoadIssue(
	ctx context.Context,
	identity continuousdispatch.DispatchIdentity,
) (db.Issue, error) {
	issue, err := tx.queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: parseDispatchUUID(identity.IssueID), WorkspaceID: parseDispatchUUID(identity.WorkspaceID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, ErrContinuousDispatchIssueDrift
	}
	return issue, err
}

func (tx *productionContinuousDispatchTx) PrepareTask(
	ctx context.Context,
	issue db.Issue,
	req ContinuousDispatchRequest,
) (db.AgentTaskQueue, error) {
	agent, err := tx.queries.GetAgent(ctx, req.Route.LocalAgentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load selected agent: %w", err)
	}
	if agent.ArchivedAt.Valid || !agent.RuntimeID.Valid || agent.RuntimeID != req.Route.RuntimeID ||
		!agent.Model.Valid || agent.Model.String != req.Route.Model {
		return db.AgentTaskQueue{}, ErrContinuousDispatchRouteDrift
	}
	task, err := tx.taskService.prepareMentionTaskWithCommentPlan(
		ctx,
		tx.queries,
		issue,
		req.Route.LocalAgentID,
		pgtype.UUID{},
		nil,
		false,
		pgtype.UUID{},
		false,
		req.HandoffNote,
		req.ActorUserID,
		pgtype.UUID{},
	)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	return task, nil
}

func (tx *productionContinuousDispatchTx) StampTaskIdentity(
	ctx context.Context,
	task db.AgentTaskQueue,
	identity continuousdispatch.DispatchIdentity,
) (db.AgentTaskQueue, error) {
	stamped, err := tx.queries.StampContinuousDispatchTaskIdentity(ctx, db.StampContinuousDispatchTaskIdentityParams{
		WorkspaceID: parseDispatchUUID(identity.WorkspaceID), IssueID: parseDispatchUUID(identity.IssueID),
		Stage: identity.Stage, CandidateRevision: identity.CandidateRevision, Generation: identity.Generation,
		TaskID: task.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentTaskQueue{}, ErrContinuousDispatchConflict
	}
	return stamped, err
}

func (tx *productionContinuousDispatchTx) AppendReceipt(
	ctx context.Context,
	receipt ContinuousDispatchReceipt,
) (ContinuousDispatchReceipt, error) {
	return NewContinuousDispatchReceiptRepository(tx.queries).Append(ctx, receipt)
}

var _ ContinuousDispatchBackend = (*ProductionContinuousDispatchBackend)(nil)
var _ ContinuousDispatchTx = (*productionContinuousDispatchTx)(nil)
