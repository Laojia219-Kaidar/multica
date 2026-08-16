package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	issue, err := tx.queries.LockContinuousDispatchIssue(ctx, db.LockContinuousDispatchIssueParams{
		IssueID: parseDispatchUUID(identity.IssueID), WorkspaceID: parseDispatchUUID(identity.WorkspaceID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, ErrContinuousDispatchIssueDrift
	}
	return issue, err
}

func (tx *productionContinuousDispatchTx) LoadTask(
	ctx context.Context,
	taskID pgtype.UUID,
) (db.AgentTaskQueue, error) {
	return tx.queries.GetAgentTask(ctx, taskID)
}

// VerifyReviewSource is deliberately transaction-local. It locks the exact
// source Comment and completed implementation Task after the Issue is locked,
// then proves the complete lineage before any review Task or receipt exists.
// A changed/deleted Comment, source-task swap, or candidate drift therefore
// leaves the transaction with zero writes.
func (tx *productionContinuousDispatchTx) VerifyReviewSource(
	ctx context.Context,
	req ContinuousDispatchRequest,
) error {
	if req.reviewProvenance == nil {
		return nil
	}
	provenance := *req.reviewProvenance
	commentID, ok := parseContinuousDispatchReviewCommentRef(provenance.SourceRef)
	if !ok {
		return ErrContinuousDispatchReviewLineageDrift
	}
	issueID := parseDispatchUUID(provenance.SourceIssueID)
	taskID := parseDispatchUUID(provenance.SourceTaskID)
	if !issueID.Valid || !taskID.Valid {
		return ErrContinuousDispatchReviewLineageDrift
	}
	comment, err := tx.queries.LockReviewSourceCommentForContinuousDispatch(ctx, db.LockReviewSourceCommentForContinuousDispatchParams{
		SourceCommentID: commentID,
		IssueID:         issueID,
		WorkspaceID:     parseDispatchUUID(req.Identity.WorkspaceID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrContinuousDispatchReviewLineageDrift
	}
	if err != nil {
		return fmt.Errorf("lock review source comment: %w", err)
	}
	if !comment.SourceTaskID.Valid || comment.SourceTaskID != taskID || comment.AuthorType != "agent" || !comment.AuthorID.Valid {
		return ErrContinuousDispatchReviewLineageDrift
	}
	sourceTask, err := tx.queries.LockReviewSourceTaskForContinuousDispatch(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrContinuousDispatchReviewLineageDrift
	}
	if err != nil {
		return fmt.Errorf("lock review source task: %w", err)
	}
	if sourceTask.Status != "completed" || !sourceTask.IssueID.Valid || sourceTask.IssueID != issueID ||
		!sourceTask.AgentID.Valid || sourceTask.AgentID != comment.AuthorID ||
		(sourceTask.HandoffNote.Valid && strings.HasPrefix(sourceTask.HandoffNote.String, "review_dispatch ")) {
		return ErrContinuousDispatchReviewLineageDrift
	}
	// The reviewer must be independent from the completed implementation
	// author. Keep this in the transaction-local source check rather than
	// trusting an upstream selector: every review entry point must preserve the
	// same non-self-review invariant before it can create a Task or receipt.
	if req.Route.LocalAgentID == sourceTask.AgentID {
		return ErrContinuousDispatchReviewLineageDrift
	}
	var contextValue shadowTaskContext
	if len(sourceTask.Context) == 0 || json.Unmarshal(sourceTask.Context, &contextValue) != nil ||
		!contextValue.ContinuousDispatch.Complete() || contextValue.ContinuousDispatch.Stage != "implementation" ||
		contextValue.ContinuousDispatch.WorkspaceID != req.Identity.WorkspaceID ||
		contextValue.ContinuousDispatch.IssueID != req.Identity.IssueID ||
		contextValue.ContinuousDispatch.CandidateRevision != req.Identity.CandidateRevision ||
		contextValue.ContinuousDispatch.Generation != req.Identity.Generation {
		return ErrContinuousDispatchReviewLineageDrift
	}
	return nil
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
	reviewProvenance *ContinuousDispatchReviewProvenance,
) (db.AgentTaskQueue, error) {
	params := db.StampContinuousDispatchTaskIdentityParams{
		WorkspaceID: parseDispatchUUID(identity.WorkspaceID), IssueID: parseDispatchUUID(identity.IssueID),
		Stage: identity.Stage, CandidateRevision: identity.CandidateRevision, Generation: identity.Generation,
		TaskID: task.ID,
	}
	if reviewProvenance != nil {
		params.ReviewSourceRef = pgtype.Text{String: reviewProvenance.SourceRef, Valid: true}
		params.ReviewSourceIssueID = parseDispatchUUID(reviewProvenance.SourceIssueID)
		params.ReviewSourceTaskID = parseDispatchUUID(reviewProvenance.SourceTaskID)
		params.ReviewInitiatorSource = pgtype.Text{String: reviewProvenance.InitiatorSource, Valid: true}
	}
	stamped, err := tx.queries.StampContinuousDispatchTaskIdentity(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentTaskQueue{}, ErrContinuousDispatchConflict
	}
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if reviewProvenance == nil {
		return stamped, nil
	}
	if stamped.TaskKind != TaskKindReview || stamped.ReviewTargetTaskID != params.ReviewSourceTaskID {
		return db.AgentTaskQueue{}, ErrContinuousDispatchConflict
	}
	if _, err := tx.queries.QueueIssueForContinuousReview(ctx, db.QueueIssueForContinuousReviewParams{
		IssueID:     parseDispatchUUID(identity.IssueID),
		WorkspaceID: parseDispatchUUID(identity.WorkspaceID),
	}); errors.Is(err, pgx.ErrNoRows) {
		return db.AgentTaskQueue{}, ErrContinuousDispatchIssueDrift
	} else if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("queue issue for continuous review: %w", err)
	}
	return stamped, nil
}

func (tx *productionContinuousDispatchTx) AppendReceipt(
	ctx context.Context,
	receipt ContinuousDispatchReceipt,
) (ContinuousDispatchReceipt, error) {
	return NewContinuousDispatchReceiptRepository(tx.queries).Append(ctx, receipt)
}

var _ ContinuousDispatchBackend = (*ProductionContinuousDispatchBackend)(nil)
var _ ContinuousDispatchTx = (*productionContinuousDispatchTx)(nil)
