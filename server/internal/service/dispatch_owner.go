package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// OwnerDispatchService implements the two Owner explicit-dispatch commands
// (HIV-355): a read-only preview and an idempotent write. Both reuse the
// existing IssueService/TaskService and the formal Task/Run truth — they
// never create a second authority.
type OwnerDispatchService struct {
	Queries     *db.Queries
	TxStarter   TxStarter
	TaskService *TaskService
}

// NewOwnerDispatchService constructs an OwnerDispatchService.
func NewOwnerDispatchService(q *db.Queries, tx TxStarter, ts *TaskService) *OwnerDispatchService {
	return &OwnerDispatchService{Queries: q, TxStarter: tx, TaskService: ts}
}

// DispatchDecision is the human-readable verdict a preview or dispatch returns.
type DispatchDecision string

const (
	DecisionWouldEnqueue    DispatchDecision = "would_enqueue"
	DecisionAlreadyActive   DispatchDecision = "already_active"
	DecisionBlocked         DispatchDecision = "blocked"
	DecisionNeedsAssignment DispatchDecision = "needs_assignment"
)

// DispatchBlockReason is a stable, machine-readable code explaining why a
// dispatch was blocked. Mirrors the dispatch.ReasonCode vocabulary but is
// local to this service to avoid coupling the preview to the admission enum.
type DispatchBlockReason string

const (
	BlockReasonNone                  DispatchBlockReason = ""
	BlockReasonNoAssignee            DispatchBlockReason = "no_assignee"
	BlockReasonAgentArchived         DispatchBlockReason = "agent_archived"
	BlockReasonRuntimeOffline        DispatchBlockReason = "runtime_offline"
	BlockReasonInvocationDenied      DispatchBlockReason = "invocation_not_allowed"
	BlockReasonTerminalStatus        DispatchBlockReason = "terminal_status"
	BlockReasonIdempotencyConflict   DispatchBlockReason = "idempotency_conflict"
	BlockReasonExpectedStateMismatch DispatchBlockReason = "expected_state_mismatch"
)

// PreviewResult is the read-only outcome of dispatch-preview.
type PreviewResult struct {
	Decision       DispatchDecision    `json:"decision"`
	Reason         DispatchBlockReason `json:"reason,omitempty"`
	IssueStatus    string              `json:"issue_status"`
	IssueUpdatedAt string              `json:"issue_updated_at"`
	Assignee       *PreviewAssignee    `json:"assignee,omitempty"`
	ActiveTasks    []PreviewTask       `json:"active_tasks"`
}

// PreviewAssignee summarises the issue's current assignee for the preview.
type PreviewAssignee struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	Archived      bool   `json:"archived,omitempty"`
	RuntimeOnline *bool  `json:"runtime_online,omitempty"`
	CanInvoke     bool   `json:"can_invoke"`
}

// PreviewTask is a lightweight view of an active task for the preview.
type PreviewTask struct {
	ID                string `json:"id"`
	AgentID           string `json:"agent_id"`
	Status            string `json:"status"`
	OriginatorUserID  string `json:"originator_user_id,omitempty"`
	AccountableUserID string `json:"accountable_user_id,omitempty"`
}

// DispatchPreviewParams carries the inputs for a read-only preview.
type DispatchPreviewParams struct {
	Issue       db.Issue
	ActiveTasks []db.AgentTaskQueue
	// CanInvokeAgent is the permission probe. nil means allow-all (caller
	// has already gated at the HTTP boundary).
	CanInvokeAgent func(agent db.Agent) bool
}

// Preview computes the read-only dispatch decision without any writes.
func (s *OwnerDispatchService) Preview(ctx context.Context, p DispatchPreviewParams) (*PreviewResult, error) {
	issue := p.Issue
	result := &PreviewResult{
		IssueStatus:    issue.Status,
		IssueUpdatedAt: issue.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05.000000Z"),
	}

	// Terminal statuses: done / cancelled → blocked.
	if issue.Status == "done" || issue.Status == "cancelled" {
		result.Decision = DecisionBlocked
		result.Reason = BlockReasonTerminalStatus
		return result, nil
	}

	// No assignee → needs_assignment.
	if issue.AssigneeType.String == "" || !issue.AssigneeID.Valid {
		result.Decision = DecisionNeedsAssignment
		result.Reason = BlockReasonNoAssignee
		return result, nil
	}

	// Resolve the target agent.
	agent, err := s.resolveTargetAgent(ctx, issue)
	if err != nil {
		result.Decision = DecisionBlocked
		result.Reason = BlockReasonNoAssignee
		return result, nil
	}

	// Populate assignee info.
	assignee := &PreviewAssignee{
		Type: issue.AssigneeType.String,
		ID:   util.UUIDToString(agent.ID),
		Name: agent.Name,
	}
	if agent.ArchivedAt.Valid {
		assignee.Archived = true
		assignee.CanInvoke = false
		result.Decision = DecisionBlocked
		result.Reason = BlockReasonAgentArchived
		result.Assignee = assignee
		return result, nil
	}

	// Runtime online check.
	if agent.RuntimeID.Valid {
		rt, rtErr := s.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
		if rtErr == nil {
			online := rt.Status == "online"
			assignee.RuntimeOnline = &online
			if !online {
				assignee.CanInvoke = false
				result.Decision = DecisionBlocked
				result.Reason = BlockReasonRuntimeOffline
				result.Assignee = assignee
				return result, nil
			}
		} else {
			offline := false
			assignee.RuntimeOnline = &offline
		}
	} else {
		offline := false
		assignee.RuntimeOnline = &offline
	}

	// Permission check.
	if p.CanInvokeAgent != nil {
		assignee.CanInvoke = p.CanInvokeAgent(agent)
		if !assignee.CanInvoke {
			result.Decision = DecisionBlocked
			result.Reason = BlockReasonInvocationDenied
			result.Assignee = assignee
			return result, nil
		}
	} else {
		assignee.CanInvoke = true
	}
	result.Assignee = assignee

	// Check active tasks.
	for _, t := range p.ActiveTasks {
		result.ActiveTasks = append(result.ActiveTasks, PreviewTask{
			ID:                util.UUIDToString(t.ID),
			AgentID:           util.UUIDToString(t.AgentID),
			Status:            t.Status,
			OriginatorUserID:  util.UUIDToString(t.OriginatorUserID),
			AccountableUserID: util.UUIDToString(t.AccountableUserID),
		})
	}
	if len(result.ActiveTasks) > 0 {
		result.Decision = DecisionAlreadyActive
		return result, nil
	}

	result.Decision = DecisionWouldEnqueue
	return result, nil
}

// DispatchParams carries the inputs for the idempotent write.
type DispatchParams struct {
	Issue             db.Issue
	WorkspaceID       pgtype.UUID
	IdempotencyKey    string
	RequestDigest     string
	ExpectedStatus    string
	ExpectedUpdatedAt string
	ActiveTasks       []db.AgentTaskQueue
	ActorUserID       pgtype.UUID
}

// DispatchResult is the write outcome.
type DispatchResult struct {
	Decision DispatchDecision    `json:"decision"`
	Reason   DispatchBlockReason `json:"reason,omitempty"`
	TaskIDs  []string            `json:"task_ids,omitempty"`
	Replayed bool                `json:"replayed,omitempty"`
}

var (
	ErrIdempotencyConflict   = errors.New("dispatch: idempotency key exists with different digest")
	ErrExpectedStateMismatch = errors.New("dispatch: issue state does not match expected")
)

// Dispatch performs the idempotent write. It never cancels existing tasks.
// If an active task already exists, it returns already_active with those IDs.
// If the idempotency key was used before with the same digest, it replays.
// If the digest differs, it returns ErrIdempotencyConflict.
func (s *OwnerDispatchService) Dispatch(ctx context.Context, p DispatchParams) (*DispatchResult, error) {
	issue := p.Issue

	// 1. Terminal status guard.
	if issue.Status == "done" || issue.Status == "cancelled" {
		return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonTerminalStatus}, nil
	}

	// 2. Expected-state check.
	if p.ExpectedStatus != "" && issue.Status != p.ExpectedStatus {
		return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonExpectedStateMismatch}, ErrExpectedStateMismatch
	}
	if p.ExpectedUpdatedAt != "" {
		currentUpdatedAt := issue.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05.000000Z")
		if currentUpdatedAt != p.ExpectedUpdatedAt {
			return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonExpectedStateMismatch}, ErrExpectedStateMismatch
		}
	}

	// 3. No assignee guard.
	if issue.AssigneeType.String == "" || !issue.AssigneeID.Valid {
		return &DispatchResult{Decision: DecisionNeedsAssignment, Reason: BlockReasonNoAssignee}, nil
	}

	// 4. Idempotency lookup.
	existing, err := s.Queries.GetDispatchIdempotency(ctx, db.GetDispatchIdempotencyParams{
		WorkspaceID:    p.WorkspaceID,
		IdempotencyKey: p.IdempotencyKey,
	})
	if err == nil {
		// Key exists.
		if existing.RequestDigest != p.RequestDigest {
			return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonIdempotencyConflict}, ErrIdempotencyConflict
		}
		// Same digest → replay.
		ids := make([]string, 0, len(existing.TaskIds))
		for _, id := range existing.TaskIds {
			ids = append(ids, util.UUIDToString(id))
		}
		return &DispatchResult{Decision: DecisionWouldEnqueue, TaskIDs: ids, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("dispatch idempotency lookup: %w", err)
	}

	// 5. Active tasks check — never cancel, just report.
	activeIDs := make([]string, 0)
	for _, t := range p.ActiveTasks {
		activeIDs = append(activeIDs, util.UUIDToString(t.ID))
	}
	if len(activeIDs) > 0 {
		return &DispatchResult{Decision: DecisionAlreadyActive, TaskIDs: activeIDs}, nil
	}

	// 6. Resolve target agent and validate.
	agent, err := s.resolveTargetAgent(ctx, issue)
	if err != nil {
		return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonNoAssignee}, nil
	}
	if agent.ArchivedAt.Valid {
		return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonAgentArchived}, nil
	}
	if agent.RuntimeID.Valid {
		rt, rtErr := s.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
		if rtErr != nil || rt.Status != "online" {
			return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonRuntimeOffline}, nil
		}
	} else {
		return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonRuntimeOffline}, nil
	}

	// 7. Enqueue the task and record idempotency in a single transaction (F1).
	// Both the task insert and the idempotency row must commit or roll back
	// together — otherwise a crash between them leaves a task with no
	// idempotency record (fail-open) or an idempotency row pointing at a
	// task that was never created.
	tx, txErr := s.TxStarter.Begin(ctx)
	if txErr != nil {
		return nil, fmt.Errorf("begin dispatch tx: %w", txErr)
	}
	defer tx.Rollback(ctx)

	qtx := s.Queries.WithTx(tx)

	var task db.AgentTaskQueue
	switch issue.AssigneeType.String {
	case "agent":
		task, err = s.TaskService.prepareIssueTaskWithCommentPlan(ctx, qtx, issue, pgtype.UUID{}, nil, false, "", p.ActorUserID, pgtype.UUID{}, nil)
		if err != nil {
			if isDuplicatePendingTaskConstraint(err) {
				return s.resolveDuplicatePendingTask(ctx, issue)
			}
			return nil, fmt.Errorf("enqueue agent task: %w", err)
		}
	case "squad":
		squad, sqErr := qtx.GetSquad(ctx, issue.AssigneeID)
		if sqErr != nil {
			return nil, fmt.Errorf("load squad: %w", sqErr)
		}
		task, err = s.TaskService.prepareMentionTaskWithCommentPlan(ctx, qtx, issue, squad.LeaderID, pgtype.UUID{}, nil, true, issue.AssigneeID, false, "", p.ActorUserID, pgtype.UUID{})
		if err != nil {
			if isDuplicatePendingTaskConstraint(err) {
				return s.resolveDuplicatePendingTask(ctx, issue)
			}
			return nil, fmt.Errorf("enqueue squad leader task: %w", err)
		}
	default:
		return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonNoAssignee}, nil
	}

	taskIDs := []string{util.UUIDToString(task.ID)}

	// 8. Record idempotency inside the same transaction.
	taskUUIDs := make([]pgtype.UUID, 0, len(taskIDs))
	for _, tid := range taskIDs {
		taskUUIDs = append(taskUUIDs, util.MustParseUUID(tid))
	}
	_, idemErr := qtx.InsertDispatchIdempotency(ctx, db.InsertDispatchIdempotencyParams{
		WorkspaceID:    p.WorkspaceID,
		IdempotencyKey: p.IdempotencyKey,
		RequestDigest:  p.RequestDigest,
		TaskIds:        taskUUIDs,
	})
	if idemErr != nil {
		if isUniqueViolation(idemErr) {
			winner, lookupErr := qtx.GetDispatchIdempotency(ctx, db.GetDispatchIdempotencyParams{
				WorkspaceID:    p.WorkspaceID,
				IdempotencyKey: p.IdempotencyKey,
			})
			if lookupErr == nil && winner.RequestDigest == p.RequestDigest {
				replayIDs := make([]string, 0, len(winner.TaskIds))
				for _, id := range winner.TaskIds {
					replayIDs = append(replayIDs, util.UUIDToString(id))
				}
				return &DispatchResult{Decision: DecisionWouldEnqueue, TaskIDs: replayIDs, Replayed: true}, nil
			}
			return &DispatchResult{Decision: DecisionBlocked, Reason: BlockReasonIdempotencyConflict}, ErrIdempotencyConflict
		}
		return nil, fmt.Errorf("insert dispatch idempotency: %w", idemErr)
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return nil, fmt.Errorf("commit dispatch tx: %w", commitErr)
	}

	// Broadcast and notify only after successful commit — the task is now
	// durable and the idempotency row is committed alongside it.
	s.TaskService.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.TaskService.NotifyTaskEnqueued(ctx, task)

	return &DispatchResult{Decision: DecisionWouldEnqueue, TaskIDs: taskIDs}, nil
}

// isDuplicatePendingTaskConstraint reports whether err is the unique-index
// violation on idx_one_pending_task_per_issue_agent (a concurrent enqueue won
// the race). Mirrors the check in task.go so the dispatch path can detect the
// same benign outcome without leaking the constraint name.
func isDuplicatePendingTaskConstraint(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "idx_one_pending_task_per_issue_agent"
}

// resolveDuplicatePendingTask looks up the existing active task for the issue
// and returns already_active with its IDs. This is the F2 graceful handling:
// a concurrent dispatch won the race to enqueue, so we report the winner
// instead of returning 500.
func (s *OwnerDispatchService) resolveDuplicatePendingTask(ctx context.Context, issue db.Issue) (*DispatchResult, error) {
	activeTasks, err := s.Queries.ListActiveTasksByIssue(ctx, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("lookup active tasks after duplicate: %w", err)
	}
	ids := make([]string, 0, len(activeTasks))
	for _, t := range activeTasks {
		ids = append(ids, util.UUIDToString(t.ID))
	}
	if len(ids) > 0 {
		return &DispatchResult{Decision: DecisionAlreadyActive, TaskIDs: ids}, nil
	}
	return &DispatchResult{Decision: DecisionAlreadyActive}, nil
}

// resolveTargetAgent finds the runnable agent for an issue.
// For agent assignees, returns the agent directly.
// For squad assignees, returns the squad leader.
func (s *OwnerDispatchService) resolveTargetAgent(ctx context.Context, issue db.Issue) (db.Agent, error) {
	switch issue.AssigneeType.String {
	case "agent":
		return s.Queries.GetAgent(ctx, issue.AssigneeID)
	case "squad":
		squad, err := s.Queries.GetSquad(ctx, issue.AssigneeID)
		if err != nil {
			return db.Agent{}, fmt.Errorf("load squad: %w", err)
		}
		return s.Queries.GetAgent(ctx, squad.LeaderID)
	default:
		return db.Agent{}, fmt.Errorf("unsupported assignee type: %s", issue.AssigneeType.String)
	}
}

// ComputeDigest returns a hex-encoded SHA-256 of the request body bytes.
func ComputeDigest(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unique") || strings.Contains(msg, "23505")
}
