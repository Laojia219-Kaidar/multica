package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ReviewPipelineV2 acceptance-axis states (HIV-326 contract C2). The enum is
// also enforced by the issue_review_state_closed_enum CHECK constraint; this
// Go mirror is the single server-side state machine used by every writer.
const (
	ReviewStateQueued          = "queued"
	ReviewStateTriaging        = "triaging"
	ReviewStateEvidenceReview  = "evidence_review"
	ReviewStateReviseRequested = "revise_requested"
	ReviewStateOwnerDecision   = "owner_decision"
	ReviewStateAccepted        = "accepted"
	ReviewStateSuperseded      = "superseded"
	ReviewStateArchivedHistory = "archived_history"
)

// Task kinds on agent_task_queue (HIV-326 contract C4). 'work' is the schema
// default and preserves every pre-existing insert.
const (
	TaskKindWork   = "work"
	TaskKindReview = "review"
	TaskKindRepair = "repair"
)

// Stable escalation reasons (HIV-326 C4-L-3 / §5-11..14). The issue-level
// review_state_reason column stores "reason/sub_reason" so the review queue can
// render a reason badge without reading issue.metadata (which must never become
// a second review truth source).
const (
	ReviewEscalationReasonMissingLineage = "missing_candidate_lineage"

	LineageFailureNoSourceTaskID        = "no_source_task_id"
	LineageFailureTaskNotFound          = "task_not_found"
	LineageFailureCrossIssueReference   = "cross_issue_reference"
	LineageFailureCandidateNotTerminal  = "candidate_not_terminal"
	LineageFailureReviewerIsImplementer = "reviewer_is_implementer"
	LineageFailureReviewerUnconfigured  = "reviewer_unconfigured"
	LineageFailureReviewerUnavailable   = "reviewer_unavailable"
)

var (
	// ErrNoOpenReviewTask: verdict arrived but no review task is open (never
	// created, already completed, or cancelled — §5-5 取消后写入).
	ErrNoOpenReviewTask = errors.New("review pipeline: no open review task for this issue")
	// ErrNotAssignedReviewer: an agent that is not the review task's reviewer
	// tried to write a verdict.
	ErrNotAssignedReviewer = errors.New("review pipeline: actor is not the assigned reviewer")
	// ErrReviewerIsImplementer: the reviewer must differ from the candidate's
	// implementer (C3). Enforced at task creation and at verdict time.
	ErrReviewerIsImplementer = errors.New("review pipeline: reviewer must differ from the implementer")
	// ErrNotCoordinator: only the coordinator agent may write an accepted
	// verdict (C6); only the coordinator (or a human owner) may requeue.
	ErrNotCoordinator = errors.New("review pipeline: only the coordinator may perform this action")
	// ErrReviewStateClosed: the issue is not in an open review state that
	// accepts this transition.
	ErrReviewStateClosed = errors.New("review pipeline: review state is not open for this action")
	// ErrNotInOwnerDecision: requeue requires owner_decision.
	ErrNotInOwnerDecision = errors.New("review pipeline: issue is not in owner_decision")
	// ErrReviewerUnconfigured: no L1 reviewer agent is configured (fail-closed,
	// never a silent skip).
	ErrReviewerUnconfigured = errors.New("review pipeline: reviewer agent is not configured")
	// ErrReviewerUnavailable: the configured reviewer agent has no runtime and
	// can never claim a review task (fail-closed).
	ErrReviewerUnavailable = errors.New("review pipeline: reviewer agent has no claimable runtime")
)

// ReviewPipelineConfig is the server-side wiring for the review pipeline. It
// is constructed from environment by cmd/server; tests build it directly.
type ReviewPipelineConfig struct {
	Enabled             bool
	ReviewerAgentID     pgtype.UUID
	ReviewerAgentIDSet  bool
	CoordinatorAgentID  pgtype.UUID
	CoordinatorAgentSet bool
	ReviewWIPLimit      int32
	ReviewPriority      int32
}

// ReviewActor is the resolved actor identity for verdict / requeue writes:
// either an agent (the reviewer / coordinator) or a member (the human Owner).
type ReviewActor struct {
	ActorType string // "agent" | "member"
	ActorID   pgtype.UUID
}

// VerdictInput carries the structured verdict write.
type VerdictInput struct {
	Verdict            string   // "pass" | "revise"
	Notes              string   // free-form justification
	RepairRequirements []string // required repair checklist for REVISE
}

// VerdictResult is what the verdict endpoint returns.
type VerdictResult struct {
	IssueID      pgtype.UUID
	WorkspaceID  pgtype.UUID
	ReviewState  string
	ReviewTaskID pgtype.UUID
}

// RequeueResult is what the requeue endpoint returns.
type RequeueResult struct {
	ReviewState       string
	ReviewTaskCreated bool
	Reason            string
}

// BackfillEntry is one dry-run request line: an issue and the review_state
// proposed by the HIV-319 classification mapping (continue_now→queued,
// repair→revise_requested, superseded→superseded,
// archive_history→archived_history, owner_decision→owner_decision).
type BackfillEntry struct {
	IssueID             pgtype.UUID
	IntendedReviewState string
}

// BackfillItem is one dry-run result line. ProposedReviewState may be
// downgraded from queued to owner_decision when lineage is invalid; the reason
// explains why. The dry-run is strictly read-only (zero writes).
type BackfillItem struct {
	IssueID                   pgtype.UUID
	WorkspaceID               pgtype.UUID
	Number                    int32
	Title                     string
	CurrentStatus             string
	CurrentReviewState        pgtype.Text
	LegacyMetadataReviewState string // one-time migration input read from issue.metadata
	ProposedReviewState       string
	LineageValid              bool
	LineageSubReason          string
	Warnings                  []string
}

// BackfillSummary aggregates a dry-run result by proposed state.
type BackfillSummary struct {
	ByState map[string]int
	Total   int
}

// ReviewPipelineService implements the ReviewPipelineV2 acceptance axis
// (HIV-326 contract): idempotent review-task creation on IssueUpdated,
// fail-closed lineage resolution, the review state machine, verdict writes,
// requeue and the read-only backfill dry-run.
type ReviewPipelineService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Bus       *events.Bus
	Config    ReviewPipelineConfig
}

func NewReviewPipelineService(q *db.Queries, tx TxStarter, bus *events.Bus, cfg ReviewPipelineConfig) *ReviewPipelineService {
	return &ReviewPipelineService{Queries: q, TxStarter: tx, Bus: bus, Config: cfg}
}

// candidateLineage is the resolved delivery lineage (C4-L): the newest comment
// carrying a source_task_id plus the exact candidate task it references, after
// independent validation.
type candidateLineage struct {
	Comment   db.Comment
	Task      db.AgentTaskQueue
	Valid     bool
	SubReason string
}

// resolveLineage maps the "issue entered in_review" event onto the unique
// review candidate (C4-L). Every failure branch returns an explicit sub-reason;
// no failure ever reaches review-task creation.
func (s *ReviewPipelineService) resolveLineage(ctx context.Context, qtx *db.Queries, issueID pgtype.UUID) (candidateLineage, error) {
	comment, err := qtx.LatestLineageComment(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return candidateLineage{SubReason: LineageFailureNoSourceTaskID}, nil
	}
	if err != nil {
		return candidateLineage{}, fmt.Errorf("resolve latest delivery comment: %w", err)
	}
	if !comment.SourceTaskID.Valid {
		return candidateLineage{SubReason: LineageFailureNoSourceTaskID}, nil
	}
	task, err := qtx.GetAgentTask(ctx, comment.SourceTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return candidateLineage{SubReason: LineageFailureTaskNotFound}, nil
	}
	if err != nil {
		return candidateLineage{}, fmt.Errorf("resolve candidate task: %w", err)
	}
	if !uuidEqual(task.IssueID, issueID) {
		return candidateLineage{SubReason: LineageFailureCrossIssueReference}, nil
	}
	if task.Status != "completed" {
		return candidateLineage{SubReason: LineageFailureCandidateNotTerminal}, nil
	}
	return candidateLineage{Comment: comment, Task: task, Valid: true}, nil
}

// OnIssueEnteredReview is the IssueUpdated listener entry for
// status_changed && status=='in_review'. It is idempotent against duplicate
// events and concurrent consumers: the guarded state transition plus the
// partial unique index on open review tasks make the second arrival a no-op.
func (s *ReviewPipelineService) OnIssueEnteredReview(ctx context.Context, issueID pgtype.UUID) error {
	return s.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, issueID)
		if err != nil {
			return fmt.Errorf("review listener: load issue: %w", err)
		}
		switch {
		case !issue.ReviewState.Valid:
			return s.handleFreshEntry(ctx, qtx, issue)
		case isOpenReviewState(issue.ReviewState.String) || issue.ReviewState.String == ReviewStateReviseRequested:
			return s.handleReentry(ctx, qtx, issue)
		default:
			// owner_decision / terminal states only move through explicit
			// requeue or coordinator actions — never through a duplicate event.
			return nil
		}
	})
}

// handleFreshEntry implements C4 (NULL → queued + review task) and C4-L
// fail-closed (NULL → owner_decision, no review task).
func (s *ReviewPipelineService) handleFreshEntry(ctx context.Context, qtx *db.Queries, issue db.Issue) error {
	lineage, err := s.resolveLineage(ctx, qtx, issue.ID)
	if err != nil {
		return err
	}
	if !lineage.Valid {
		return s.failClosedFromNULL(ctx, qtx, issue, lineage.SubReason)
	}
	created, _, err := s.createReviewTask(ctx, qtx, issue, lineage.Task)
	if err != nil {
		// Creation failures are fail-closed too: never leave the issue queued
		// with no claimable review task.
		return s.failClosedFromNULL(ctx, qtx, issue, lineageFailureForErr(err))
	}
	if !created {
		// A concurrent consumer already created the open review task; the
		// guarded transition below still lands the state once.
		slog.Debug("review listener: open review task already exists",
			"issue_id", util.UUIDToString(issue.ID))
	}
	_, err = qtx.SetIssueReviewState(ctx, db.SetIssueReviewStateParams{
		ID:            issue.ID,
		ExpectedState: pgtype.Text{}, // NULL
		NewState:      pgtype.Text{String: ReviewStateQueued, Valid: true},
		NewReason:     pgtype.Text{},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Someone else transitioned between our lock and the write — no-op.
		return nil
	}
	if err != nil {
		return fmt.Errorf("review listener: transition to queued: %w", err)
	}
	return nil
}

// handleReentry implements C9/C7 for an issue that re-entered in_review while a
// review round was open or waiting for repair: a newer candidate supersedes any
// in-flight review task and starts a fresh round. If the lineage resolves to the
// candidate already under review, the event is a no-op.
func (s *ReviewPipelineService) handleReentry(ctx context.Context, qtx *db.Queries, issue db.Issue) error {
	lineage, err := s.resolveLineage(ctx, qtx, issue.ID)
	if err != nil {
		return err
	}
	if !lineage.Valid {
		return s.failClosedFromOpen(ctx, qtx, issue, lineage.SubReason)
	}

	existing, err := qtx.GetOpenReviewTaskForIssue(ctx, issue.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("review listener: load open review task: %w", err)
	}
	if err == nil && uuidEqual(existing.ReviewTargetTaskID, lineage.Task.ID) {
		// Same candidate already under review — duplicate event no-op.
		return nil
	}
	if err == nil {
		// C9 supersede: cancel the older candidate's in-flight review round.
		if _, err := qtx.CancelOpenReviewTasksForIssue(ctx, issue.ID); err != nil {
			return fmt.Errorf("review listener: supersede old review task: %w", err)
		}
	}
	created, _, err := s.createReviewTask(ctx, qtx, issue, lineage.Task)
	if err != nil {
		return s.failClosedFromOpen(ctx, qtx, issue, lineageFailureForErr(err))
	}
	_ = created
	_, err = qtx.SetIssueReviewStateFromOpen(ctx, db.SetIssueReviewStateFromOpenParams{
		ID:        issue.ID,
		NewState:  pgtype.Text{String: ReviewStateQueued, Valid: true},
		NewReason: pgtype.Text{},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("review listener: requeue after supersede: %w", err)
	}
	return nil
}

// failClosedFromNULL is the C4-L fail-closed path: review_state NULL →
// owner_decision with a stable reason, no review task created, and a
// review:escalated event for the Owner/coordinator inbox.
func (s *ReviewPipelineService) failClosedFromNULL(ctx context.Context, qtx *db.Queries, issue db.Issue, subReason string) error {
	reason := ReviewEscalationReasonMissingLineage + "/" + subReason
	_, err := qtx.SetIssueReviewState(ctx, db.SetIssueReviewStateParams{
		ID:            issue.ID,
		ExpectedState: pgtype.Text{}, // NULL
		NewState:      pgtype.Text{String: ReviewStateOwnerDecision, Valid: true},
		NewReason:     pgtype.Text{String: reason, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // concurrent consumer already failed closed
	}
	if err != nil {
		return fmt.Errorf("review listener: fail-closed transition: %w", err)
	}
	s.publishEscalated(ctx, issue, ReviewEscalationReasonMissingLineage, subReason)
	return nil
}

// failClosedFromOpen moves an open/revise_requested issue to owner_decision
// when a re-entry event cannot resolve valid lineage.
func (s *ReviewPipelineService) failClosedFromOpen(ctx context.Context, qtx *db.Queries, issue db.Issue, subReason string) error {
	reason := ReviewEscalationReasonMissingLineage + "/" + subReason
	_, err := qtx.SetIssueReviewStateFromOpen(ctx, db.SetIssueReviewStateFromOpenParams{
		ID:        issue.ID,
		NewState:  pgtype.Text{String: ReviewStateOwnerDecision, Valid: true},
		NewReason: pgtype.Text{String: reason, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("review listener: fail-closed transition: %w", err)
	}
	s.publishEscalated(ctx, issue, ReviewEscalationReasonMissingLineage, subReason)
	return nil
}

// createReviewTask inserts the idempotent review task for a valid candidate.
// Returns (created=true) when this call inserted the task, (false) when the
// open-task unique index already covered this (issue, candidate) pair, or an
// error for reviewer-routing failures (C3 / fail-closed).
func (s *ReviewPipelineService) createReviewTask(ctx context.Context, qtx *db.Queries, issue db.Issue, candidate db.AgentTaskQueue) (bool, *db.AgentTaskQueue, error) {
	if !s.Config.ReviewerAgentIDSet {
		return false, nil, ErrReviewerUnconfigured
	}
	if uuidEqual(s.Config.ReviewerAgentID, candidate.AgentID) {
		return false, nil, ErrReviewerIsImplementer
	}
	reviewer, err := qtx.GetAgent(ctx, s.Config.ReviewerAgentID)
	if err != nil {
		return false, nil, fmt.Errorf("load reviewer agent: %w", err)
	}
	if !reviewer.RuntimeID.Valid {
		return false, nil, ErrReviewerUnavailable
	}

	payload, err := json.Marshal(reviewTaskPayload{
		Kind:              TaskKindReview,
		CandidateTaskID:   util.UUIDToString(candidate.ID),
		CandidateRevision: candidateRevisionFromContext(candidate.Context),
	})
	if err != nil {
		return false, nil, fmt.Errorf("encode review task payload: %w", err)
	}

	priority := s.Config.ReviewPriority
	if priority <= 0 {
		priority = 5
	}
	task, err := qtx.CreateReviewTask(ctx, db.CreateReviewTaskParams{
		AgentID:            s.Config.ReviewerAgentID,
		RuntimeID:          reviewer.RuntimeID,
		IssueID:            issue.ID,
		Priority:           priority,
		ReviewTargetTaskID: candidate.ID,
		TriggerSummary: pgtype.Text{
			String: fmt.Sprintf("Review delivered candidate for issue %q", issue.Title),
			Valid:  true,
		},
		Context: payload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil // open review task already exists for this candidate
	}
	if err != nil {
		return false, nil, fmt.Errorf("create review task: %w", err)
	}
	return true, &task, nil
}

// OnIssueLeftReview resets the acceptance axis when the issue leaves in_review:
// open review tasks are cancelled and review_state is cleared (C1 invariant;
// §5-8 status rollback; repair rework flow C7).
func (s *ReviewPipelineService) OnIssueLeftReview(ctx context.Context, issueID pgtype.UUID) error {
	return s.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, issueID)
		if err != nil {
			return fmt.Errorf("review listener: load issue: %w", err)
		}
		if !issue.ReviewState.Valid {
			return nil
		}
		if _, err := qtx.CancelOpenReviewTasksForIssue(ctx, issueID); err != nil {
			return fmt.Errorf("review listener: cancel open review tasks: %w", err)
		}
		if _, err := qtx.ClearIssueReviewState(ctx, issueID); err != nil {
			return fmt.Errorf("review listener: clear review state: %w", err)
		}
		return nil
	})
}

// WriteVerdict applies a reviewer/coordinator verdict to the issue's open
// review task (C6/C7). revise → revise_requested (any assigned reviewer);
// pass → accepted (coordinator agent only, or a member Owner). The review task
// is completed with a structured receipt and the verdict lands as a Task-linked
// comment on the issue.
func (s *ReviewPipelineService) WriteVerdict(ctx context.Context, issueID pgtype.UUID, actor ReviewActor, in VerdictInput) (VerdictResult, error) {
	var result VerdictResult
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, issueID)
		if err != nil {
			return fmt.Errorf("review verdict: load issue: %w", err)
		}
		task, err := qtx.GetOpenReviewTaskForIssue(ctx, issueID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoOpenReviewTask
		}
		if err != nil {
			return fmt.Errorf("review verdict: load open review task: %w", err)
		}
		candidate, err := qtx.GetAgentTask(ctx, task.ReviewTargetTaskID)
		if err != nil {
			return fmt.Errorf("review verdict: load candidate task: %w", err)
		}

		var target string
		switch in.Verdict {
		case "revise":
			target = ReviewStateReviseRequested
		case "pass":
			if actor.ActorType == "agent" {
				if !s.Config.CoordinatorAgentSet || !uuidEqual(s.Config.CoordinatorAgentID, actor.ActorID) {
					return ErrNotCoordinator
				}
			}
			target = ReviewStateAccepted
		default:
			return fmt.Errorf("review verdict: invalid verdict %q", in.Verdict)
		}

		if actor.ActorType == "agent" {
			// C6: revise may come from the assigned L1 reviewer or the
			// coordinator; pass (accepted) only from the coordinator. The
			// coordinator is not the review task's agent_id, so it bypasses
			// the assigned-reviewer check rather than being blocked by it.
			isAssignedReviewer := uuidEqual(task.AgentID, actor.ActorID)
			isCoordinator := s.Config.CoordinatorAgentSet && uuidEqual(s.Config.CoordinatorAgentID, actor.ActorID)
			if in.Verdict == "pass" {
				if !isCoordinator {
					return ErrNotCoordinator
				}
			} else if !isAssignedReviewer && !isCoordinator {
				return ErrNotAssignedReviewer
			}
			// C3: the reviewer/coordinator must differ from the candidate's
			// implementer on BOTH write paths.
			if uuidEqual(candidate.AgentID, actor.ActorID) {
				return ErrReviewerIsImplementer
			}
		}
		if !issue.ReviewState.Valid || !isOpenReviewState(issue.ReviewState.String) {
			return ErrReviewStateClosed
		}

		if _, err := qtx.SetIssueReviewStateFromOpen(ctx, db.SetIssueReviewStateFromOpenParams{
			ID:        issue.ID,
			NewState:  pgtype.Text{String: target, Valid: true},
			NewReason: pgtype.Text{},
		}); err != nil {
			return fmt.Errorf("review verdict: state transition: %w", err)
		}

		receipt, err := json.Marshal(verdictReceipt{
			Verdict:            in.Verdict,
			ReviewState:        target,
			ReviewerAgentID:    util.UUIDToString(task.AgentID),
			CandidateTaskID:    util.UUIDToString(candidate.ID),
			Notes:              in.Notes,
			RepairRequirements: in.RepairRequirements,
		})
		if err != nil {
			return fmt.Errorf("review verdict: encode receipt: %w", err)
		}
		if _, err := qtx.CompleteReviewTask(ctx, db.CompleteReviewTaskParams{ID: task.ID, Result: receipt}); err != nil {
			return fmt.Errorf("review verdict: complete review task: %w", err)
		}

		commentType := "comment"
		content := verdictCommentContent(in, task, candidate, actor)
		if _, err := qtx.CreateComment(ctx, db.CreateCommentParams{
			IssueID:      issue.ID,
			WorkspaceID:  issue.WorkspaceID,
			AuthorType:   actor.ActorType,
			AuthorID:     actor.ActorID,
			Content:      content,
			Type:         commentType,
			ParentID:     pgtype.UUID{},
			SourceTaskID: task.ID,
		}); err != nil {
			return fmt.Errorf("review verdict: persist verdict comment: %w", err)
		}

		result = VerdictResult{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, ReviewState: target, ReviewTaskID: task.ID}
		return nil
	})
	if err == nil {
		s.publishVerdict(ctx, result, actor, in)
	}
	return result, err
}

// Requeue re-runs candidate lineage for an owner_decision issue (C4-L-5 late
// lineage / explicit requeue). Valid lineage → owner_decision → queued plus a
// fresh review task; still-invalid lineage keeps owner_decision and refreshes
// the reason. Coordinator agents and member owners may call it; the second
// call is a no-op (idempotent).
func (s *ReviewPipelineService) Requeue(ctx context.Context, issueID pgtype.UUID, actor ReviewActor) (RequeueResult, error) {
	if actor.ActorType == "agent" && !(s.Config.CoordinatorAgentSet && uuidEqual(s.Config.CoordinatorAgentID, actor.ActorID)) {
		return RequeueResult{}, ErrNotCoordinator
	}
	var res RequeueResult
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, issueID)
		if err != nil {
			return fmt.Errorf("review requeue: load issue: %w", err)
		}
		if !issue.ReviewState.Valid || issue.ReviewState.String != ReviewStateOwnerDecision {
			res.ReviewState = reviewStateOrNull(issue.ReviewState)
			return nil
		}
		lineage, err := s.resolveLineage(ctx, qtx, issue.ID)
		if err != nil {
			return err
		}
		if !lineage.Valid {
			reason := ReviewEscalationReasonMissingLineage + "/" + lineage.SubReason
			if _, err := qtx.SetIssueReviewState(ctx, db.SetIssueReviewStateParams{
				ID:            issue.ID,
				ExpectedState: pgtype.Text{String: ReviewStateOwnerDecision, Valid: true},
				NewState:      pgtype.Text{String: ReviewStateOwnerDecision, Valid: true},
				NewReason:     pgtype.Text{String: reason, Valid: true},
			}); err != nil {
				return fmt.Errorf("review requeue: refresh reason: %w", err)
			}
			res = RequeueResult{ReviewState: ReviewStateOwnerDecision, Reason: reason}
			return nil
		}
		created, _, err := s.createReviewTask(ctx, qtx, issue, lineage.Task)
		if err != nil {
			reason := ReviewEscalationReasonMissingLineage + "/" + lineageFailureForErr(err)
			if _, serr := qtx.SetIssueReviewState(ctx, db.SetIssueReviewStateParams{
				ID:            issue.ID,
				ExpectedState: pgtype.Text{String: ReviewStateOwnerDecision, Valid: true},
				NewState:      pgtype.Text{String: ReviewStateOwnerDecision, Valid: true},
				NewReason:     pgtype.Text{String: reason, Valid: true},
			}); serr != nil {
				return fmt.Errorf("review requeue: fail-closed reason: %w", serr)
			}
			res = RequeueResult{ReviewState: ReviewStateOwnerDecision, Reason: reason}
			return nil
		}
		if _, err := qtx.SetIssueReviewState(ctx, db.SetIssueReviewStateParams{
			ID:            issue.ID,
			ExpectedState: pgtype.Text{String: ReviewStateOwnerDecision, Valid: true},
			NewState:      pgtype.Text{String: ReviewStateQueued, Valid: true},
			NewReason:     pgtype.Text{},
		}); err != nil {
			return fmt.Errorf("review requeue: transition to queued: %w", err)
		}
		res = RequeueResult{ReviewState: ReviewStateQueued, ReviewTaskCreated: created}
		return nil
	})
	return res, err
}

// ListReviewQueue returns the open review-queue projection for a workspace.
func (s *ReviewPipelineService) ListReviewQueue(ctx context.Context, workspaceID pgtype.UUID) ([]db.ListReviewQueueRow, error) {
	return s.Queries.ListReviewQueue(ctx, workspaceID)
}

// BackfillDryRun computes the proposed migration mapping for each entry
// without writing anything (C12: dry-run first, zero writes). queued mappings
// require valid lineage; anything else is downgraded to owner_decision with a
// stable reason. accepted is never inferred. issue.metadata.review_state is
// read here as the one-time migration input and is never written back.
func (s *ReviewPipelineService) BackfillDryRun(ctx context.Context, workspaceID pgtype.UUID, entries []BackfillEntry) ([]BackfillItem, BackfillSummary, error) {
	items := make([]BackfillItem, 0, len(entries))
	summary := BackfillSummary{ByState: make(map[string]int)}
	for _, entry := range entries {
		issue, err := s.Queries.GetIssue(ctx, entry.IssueID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				items = append(items, BackfillItem{
					IssueID:             entry.IssueID,
					ProposedReviewState: ReviewStateOwnerDecision,
					Warnings:            []string{"issue not found"},
				})
				continue
			}
			return nil, BackfillSummary{}, fmt.Errorf("backfill dry-run: load issue: %w", err)
		}
		if !uuidEqual(issue.WorkspaceID, workspaceID) {
			items = append(items, BackfillItem{
				IssueID:             entry.IssueID,
				WorkspaceID:         issue.WorkspaceID,
				Number:              issue.Number,
				Title:               issue.Title,
				CurrentStatus:       issue.Status,
				ProposedReviewState: ReviewStateOwnerDecision,
				Warnings:            []string{"issue is outside the requested workspace"},
			})
			continue
		}

		item := BackfillItem{
			IssueID:                   issue.ID,
			WorkspaceID:               issue.WorkspaceID,
			Number:                    issue.Number,
			Title:                     issue.Title,
			CurrentStatus:             issue.Status,
			CurrentReviewState:        issue.ReviewState,
			LegacyMetadataReviewState: legacyMetadataReviewState(issue.Metadata),
		}

		proposed := entry.IntendedReviewState
		lineage, err := s.resolveLineage(ctx, s.Queries, issue.ID)
		if err != nil {
			return nil, BackfillSummary{}, err
		}
		item.LineageValid = lineage.Valid
		item.LineageSubReason = lineage.SubReason

		switch proposed {
		case ReviewStateQueued:
			if !lineage.Valid {
				proposed = ReviewStateOwnerDecision
				item.Warnings = append(item.Warnings,
					"invalid candidate lineage ("+lineage.SubReason+"); cannot map to queued")
			}
		case ReviewStateAccepted:
			proposed = ReviewStateOwnerDecision
			item.Warnings = append(item.Warnings,
				"accepted is never inferred in backfill; requires Owner/coordinator decision")
		case ReviewStateReviseRequested, ReviewStateSuperseded, ReviewStateArchivedHistory, ReviewStateOwnerDecision:
			// Explicit classification from the HIV-319 mapping.
		default:
			proposed = ReviewStateOwnerDecision
			item.Warnings = append(item.Warnings,
				"unknown intended review state "+strconv.Quote(proposed))
		}
		item.ProposedReviewState = proposed
		summary.ByState[proposed]++
		summary.Total++
		items = append(items, item)
	}
	return items, summary, nil
}

// reviewTaskPayload is the context JSONB stamped on a review task at creation.
type reviewTaskPayload struct {
	Kind              string `json:"kind"`
	CandidateTaskID   string `json:"candidate_task_id"`
	CandidateRevision string `json:"candidate_revision,omitempty"`
}

// verdictReceipt is the structured receipt stored on a completed review task.
type verdictReceipt struct {
	Verdict            string   `json:"verdict"`
	ReviewState        string   `json:"review_state"`
	ReviewerAgentID    string   `json:"reviewer_agent_id"`
	CandidateTaskID    string   `json:"candidate_task_id"`
	Notes              string   `json:"notes"`
	RepairRequirements []string `json:"repair_requirements,omitempty"`
}

func (s *ReviewPipelineService) runInTx(ctx context.Context, fn func(*db.Queries) error) error {
	if s.TxStarter == nil {
		return fn(s.Queries)
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("review pipeline: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *ReviewPipelineService) publishEscalated(ctx context.Context, issue db.Issue, reason, subReason string) {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventReviewEscalated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"issue_id":     util.UUIDToString(issue.ID),
			"workspace_id": util.UUIDToString(issue.WorkspaceID),
			"reason":       reason,
			"sub_reason":   subReason,
			"review_state": ReviewStateOwnerDecision,
		},
	})
}

func (s *ReviewPipelineService) publishVerdict(ctx context.Context, result VerdictResult, actor ReviewActor, in VerdictInput) {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventReviewVerdict,
		WorkspaceID: util.UUIDToString(result.WorkspaceID),
		ActorType:   actor.ActorType,
		ActorID:     util.UUIDToString(actor.ActorID),
		Payload: map[string]any{
			"issue_id":       util.UUIDToString(result.IssueID),
			"review_state":   result.ReviewState,
			"verdict":        in.Verdict,
			"review_task_id": util.UUIDToString(result.ReviewTaskID),
		},
	})
}

func verdictCommentContent(in VerdictInput, task db.AgentTaskQueue, candidate db.AgentTaskQueue, actor ReviewActor) string {
	var b strings.Builder
	label := "PASS"
	if in.Verdict == "revise" {
		label = "REVISE"
	}
	b.WriteString("**Review verdict: " + label + "**\n")
	fmt.Fprintf(&b, "- Review task: `%s`\n", util.UUIDToString(task.ID))
	fmt.Fprintf(&b, "- Candidate task: `%s`\n", util.UUIDToString(candidate.ID))
	if in.Notes != "" {
		fmt.Fprintf(&b, "- Notes: %s\n", in.Notes)
	}
	if len(in.RepairRequirements) > 0 {
		b.WriteString("- Repair requirements:\n")
		for _, req := range in.RepairRequirements {
			fmt.Fprintf(&b, "  - %s\n", req)
		}
	}
	return b.String()
}

// candidateRevisionFromContext extracts the exact candidate revision (head_sha)
// from the candidate task context JSONB when present, so the review payload
// freezes a precise commit identity instead of a drifting branch head (C5).
func candidateRevisionFromContext(ctx []byte) string {
	if len(ctx) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(ctx, &parsed); err != nil {
		return ""
	}
	if head, ok := parsed["head_sha"].(string); ok {
		return head
	}
	return ""
}

// legacyMetadataReviewState reads the one-time migration input from
// issue.metadata. The review pipeline never writes this key — after migration
// the canonical issue.review_state is the single source of truth (HIV-333).
func legacyMetadataReviewState(metadata []byte) string {
	if len(metadata) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(metadata, &parsed); err != nil {
		return ""
	}
	value, _ := parsed["review_state"].(string)
	return value
}

func isOpenReviewState(state string) bool {
	switch state {
	case ReviewStateQueued, ReviewStateTriaging, ReviewStateEvidenceReview:
		return true
	default:
		return false
	}
}

func lineageFailureForErr(err error) string {
	switch {
	case errors.Is(err, ErrReviewerIsImplementer):
		return LineageFailureReviewerIsImplementer
	case errors.Is(err, ErrReviewerUnconfigured):
		return LineageFailureReviewerUnconfigured
	case errors.Is(err, ErrReviewerUnavailable):
		return LineageFailureReviewerUnavailable
	default:
		return "review_task_creation_failed"
	}
}

func reviewStateOrNull(state pgtype.Text) string {
	if state.Valid {
		return state.String
	}
	return ""
}
