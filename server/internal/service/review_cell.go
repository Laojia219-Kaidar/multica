package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Review-cell event type strings. These are deliberately declared here (not in
// pkg/protocol) so Lane B never mutates a shared package: consumers match on
// the stable string literals.
const (
	eventReviewVerdict   = "review:verdict"
	eventReviewEscalated = "review:escalated"
)

// Review-cell acceptance-axis states. The enum is also enforced by the
// issue_review_state_closed_enum CHECK constraint; this Go mirror is the single
// server-side state machine used by every review-cell writer.
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

// Task kinds on agent_task_queue. 'work' is the schema default and preserves
// every pre-existing insert.
const (
	TaskKindWork   = "work"
	TaskKindReview = "review"
	TaskKindRepair = "repair"
)

// Stable escalation reasons (C4-L / fail-closed). The issue-level
// review_state_reason column stores "reason/sub_reason" so the review queue can
// render a reason badge without reading issue.metadata.
const (
	ReviewEscalationReasonMissingLineage = "missing_candidate_lineage"

	LineageFailureNoSourceTaskID        = "no_source_task_id"
	LineageFailureTaskNotFound          = "task_not_found"
	LineageFailureCrossIssueReference   = "cross_issue_reference"
	LineageFailureNonWorkCandidate      = "non_work_candidate"
	LineageFailureCandidateNotTerminal  = "candidate_not_terminal"
	LineageFailureReviewerIsImplementer = "reviewer_is_implementer"
	LineageFailureReviewerUnconfigured  = "reviewer_unconfigured"
	LineageFailureReviewerUnavailable   = "reviewer_unavailable"
)

var (
	// ErrNoOpenReviewTask: verdict arrived but no review task is open (never
	// created, already completed, or cancelled).
	ErrNoOpenReviewTask = errors.New("review cell: no open review task for this issue")
	// ErrNotAssignedReviewer: an agent that is not the review task's reviewer
	// tried to write a verdict.
	ErrNotAssignedReviewer = errors.New("review cell: actor is not the assigned reviewer")
	// ErrReviewerIsImplementer: the reviewer must differ from the candidate's
	// implementer. Enforced at task creation and at verdict time.
	ErrReviewerIsImplementer = errors.New("review cell: reviewer must differ from the implementer")
	// ErrNotCoordinator: only the coordinator agent (or a member owner) may
	// write an accepted (PASS) verdict; only the coordinator (or a member
	// owner) may requeue.
	ErrNotCoordinator = errors.New("review cell: only the coordinator may perform this action")
	// ErrReviewStateClosed: the issue is not in an open review state that
	// accepts this transition.
	ErrReviewStateClosed = errors.New("review cell: review state is not open for this action")
	// ErrNotInOwnerDecision: requeue requires owner_decision.
	ErrNotInOwnerDecision = errors.New("review cell: issue is not in owner_decision")
	// ErrReviewerUnconfigured: no reviewer agent is configured and no
	// workspace-local reviewer candidate exists (fail-closed).
	ErrReviewerUnconfigured = errors.New("review cell: reviewer agent is not configured")
	// ErrReviewerUnavailable: the selected reviewer has no claimable runtime.
	ErrReviewerUnavailable = errors.New("review cell: reviewer agent has no claimable runtime")
	// ErrNoOpenRepairTask: a re-review arrived but no repair task is open.
	ErrNoOpenRepairTask = errors.New("review cell: no open repair task for this issue")
)

// ReviewCellConfig is the server-side wiring for the review cell. It is
// constructed from environment by cmd/server; tests build it directly.
type ReviewCellConfig struct {
	Enabled             bool
	ReviewerAgentID     pgtype.UUID
	ReviewerAgentIDSet  bool
	CoordinatorAgentID  pgtype.UUID
	CoordinatorAgentSet bool
	ReviewWIPLimit      int32
	ReviewPriority      int32
	RepairPriority      int32
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
	RepairTaskID pgtype.UUID
}

// RequeueResult is what the requeue endpoint returns.
type RequeueResult struct {
	ReviewState       string
	ReviewTaskCreated bool
	Reason            string
}

// ReviewTaskEnsureResult describes the durable review-task decision for one
// candidate lineage. Created and Replayed are mutually exclusive; a nil task
// means the request was fail-closed (for example, a closed Project).
type ReviewTaskEnsureResult struct {
	Created         bool
	Replayed        bool
	ReviewTaskID    pgtype.UUID
	CandidateTaskID pgtype.UUID
}

// ReviewCellService implements the P2 review cell: idempotent review-task
// creation on issue entering in_review, fail-closed lineage resolution, the
// review state machine, PASS/REVISE verdict writes, repair rework and
// independent re-review after repair completion.
type ReviewCellService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Bus       *events.Bus
	Config    ReviewCellConfig
}

func NewReviewCellService(q *db.Queries, tx TxStarter, bus *events.Bus, cfg ReviewCellConfig) *ReviewCellService {
	return &ReviewCellService{Queries: q, TxStarter: tx, Bus: bus, Config: cfg}
}

// candidateLineage is the resolved delivery lineage: the newest comment
// carrying a source_task_id plus the exact candidate task it references, after
// independent validation.
type candidateLineage struct {
	Comment   db.Comment
	Task      db.AgentTaskQueue
	Valid     bool
	SubReason string
}

// resolveLineage maps the "issue entered in_review" event onto the unique
// review candidate. Every failure branch returns an explicit sub-reason; no
// failure ever reaches review-task creation.
func (s *ReviewCellService) resolveLineage(ctx context.Context, qtx *db.Queries, issueID pgtype.UUID) (candidateLineage, error) {
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
	if task.TaskKind != TaskKindWork {
		return candidateLineage{SubReason: LineageFailureNonWorkCandidate}, nil
	}
	if task.Status != "completed" {
		return candidateLineage{SubReason: LineageFailureCandidateNotTerminal}, nil
	}
	return candidateLineage{Comment: comment, Task: task, Valid: true}, nil
}

// OnIssueEnteredReview is the IssueUpdated listener entry for
// status_changed && status=='in_review'. It is idempotent against duplicate
// events and concurrent consumers.
func (s *ReviewCellService) OnIssueEnteredReview(ctx context.Context, issueID pgtype.UUID) error {
	_, err := s.EnsureReviewTask(ctx, issueID)
	return err
}

// EnsureReviewTask is the result-bearing review entry point used by the
// scheduler. The legacy OnIssueEnteredReview wrapper remains the event-listener
// API for callers that only need an error.
func (s *ReviewCellService) EnsureReviewTask(ctx context.Context, issueID pgtype.UUID) (ReviewTaskEnsureResult, error) {
	var result ReviewTaskEnsureResult
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, issueID)
		if err != nil {
			return fmt.Errorf("review cell: load issue: %w", err)
		}
		switch {
		case !issue.ReviewState.Valid:
			result, err = s.handleFreshEntry(ctx, qtx, issue)
			return err
		case issue.ReviewState.String == ReviewStateReviseRequested:
			// Repair pending: keep revise_requested. OnRepairTaskCompleted
			// creates the fresh independent review round after the repair
			// completes. Do not pre-create a review of an in-progress repair
			// (it would overwrite revise_requested and orphan the issue when
			// the repair finishes).
			return nil
		case isOpenReviewState(issue.ReviewState.String):
			result, err = s.handleReentry(ctx, qtx, issue)
			return err
		default:
			return nil
		}
	})
	return result, err
}

func (s *ReviewCellService) handleFreshEntry(ctx context.Context, qtx *db.Queries, issue db.Issue) (ReviewTaskEnsureResult, error) {
	lineage, err := s.resolveLineage(ctx, qtx, issue.ID)
	if err != nil {
		return ReviewTaskEnsureResult{}, err
	}
	if !lineage.Valid {
		return ReviewTaskEnsureResult{}, s.failClosedFromNULL(ctx, qtx, issue, lineage.SubReason)
	}
	created, task, err := s.createReviewTask(ctx, qtx, issue, lineage.Task)
	if err != nil {
		return ReviewTaskEnsureResult{}, s.failClosedFromNULL(ctx, qtx, issue, lineageFailureForErr(err))
	}
	if !created {
		slog.Debug("review cell: open review task already exists",
			"issue_id", util.UUIDToString(issue.ID))
	}
	_, err = qtx.SetIssueReviewState(ctx, db.SetIssueReviewStateParams{
		ID:            issue.ID,
		ExpectedState: pgtype.Text{},
		NewState:      pgtype.Text{String: ReviewStateQueued, Valid: true},
		NewReason:     pgtype.Text{},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return reviewTaskEnsureResult(created, task, lineage.Task.ID), nil
	}
	if err != nil {
		return ReviewTaskEnsureResult{}, fmt.Errorf("review cell: transition to queued: %w", err)
	}
	return reviewTaskEnsureResult(created, task, lineage.Task.ID), nil
}

func (s *ReviewCellService) handleReentry(ctx context.Context, qtx *db.Queries, issue db.Issue) (ReviewTaskEnsureResult, error) {
	lineage, err := s.resolveLineage(ctx, qtx, issue.ID)
	if err != nil {
		return ReviewTaskEnsureResult{}, err
	}
	if !lineage.Valid {
		return ReviewTaskEnsureResult{}, s.failClosedFromOpen(ctx, qtx, issue, lineage.SubReason)
	}

	existing, err := qtx.GetOpenReviewTaskForIssue(ctx, issue.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ReviewTaskEnsureResult{}, fmt.Errorf("review cell: load open review task: %w", err)
	}
	if err == nil && uuidEqual(existing.ReviewTargetTaskID, lineage.Task.ID) {
		return ReviewTaskEnsureResult{Replayed: true, ReviewTaskID: existing.ID, CandidateTaskID: lineage.Task.ID}, nil
	}
	if err == nil {
		if _, err := qtx.CancelOpenReviewTasksForIssue(ctx, issue.ID); err != nil {
			return ReviewTaskEnsureResult{}, fmt.Errorf("review cell: supersede old review task: %w", err)
		}
	}
	created, task, err := s.createReviewTask(ctx, qtx, issue, lineage.Task)
	if err != nil {
		return ReviewTaskEnsureResult{}, s.failClosedFromOpen(ctx, qtx, issue, lineageFailureForErr(err))
	}
	_, err = qtx.SetIssueReviewStateFromOpen(ctx, db.SetIssueReviewStateFromOpenParams{
		ID:        issue.ID,
		NewState:  pgtype.Text{String: ReviewStateQueued, Valid: true},
		NewReason: pgtype.Text{},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return reviewTaskEnsureResult(created, task, lineage.Task.ID), nil
	}
	if err != nil {
		return ReviewTaskEnsureResult{}, fmt.Errorf("review cell: requeue after supersede: %w", err)
	}
	return reviewTaskEnsureResult(created, task, lineage.Task.ID), nil
}

func (s *ReviewCellService) failClosedFromNULL(ctx context.Context, qtx *db.Queries, issue db.Issue, subReason string) error {
	reason := ReviewEscalationReasonMissingLineage + "/" + subReason
	_, err := qtx.SetIssueReviewState(ctx, db.SetIssueReviewStateParams{
		ID:            issue.ID,
		ExpectedState: pgtype.Text{},
		NewState:      pgtype.Text{String: ReviewStateOwnerDecision, Valid: true},
		NewReason:     pgtype.Text{String: reason, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("review cell: fail-closed transition: %w", err)
	}
	s.publishEscalated(ctx, issue, ReviewEscalationReasonMissingLineage, subReason)
	return nil
}

func (s *ReviewCellService) failClosedFromOpen(ctx context.Context, qtx *db.Queries, issue db.Issue, subReason string) error {
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
		return fmt.Errorf("review cell: fail-closed transition: %w", err)
	}
	s.publishEscalated(ctx, issue, ReviewEscalationReasonMissingLineage, subReason)
	return nil
}

// selectReviewer resolves the reviewer agent for a candidate. A configured
// reviewer is preferred; otherwise the least-loaded workspace reviewer
// candidate is chosen automatically. In both cases reviewer != implementer is
// enforced.
func (s *ReviewCellService) selectReviewer(ctx context.Context, qtx *db.Queries, issue db.Issue, implementerID pgtype.UUID) (db.ListReviewerCandidatesRow, error) {
	if s.Config.ReviewerAgentIDSet {
		if uuidEqual(s.Config.ReviewerAgentID, implementerID) {
			return db.ListReviewerCandidatesRow{}, ErrReviewerIsImplementer
		}
		agent, err := qtx.GetAgent(ctx, s.Config.ReviewerAgentID)
		if err != nil {
			return db.ListReviewerCandidatesRow{}, fmt.Errorf("load reviewer agent: %w", err)
		}
		if !agent.RuntimeID.Valid {
			return db.ListReviewerCandidatesRow{}, ErrReviewerUnavailable
		}
		return db.ListReviewerCandidatesRow{
			ID:          agent.ID,
			WorkspaceID: agent.WorkspaceID,
			Name:        agent.Name,
			RuntimeID:   agent.RuntimeID,
		}, nil
	}

	candidates, err := qtx.ListReviewerCandidates(ctx, db.ListReviewerCandidatesParams{
		WorkspaceID: issue.WorkspaceID,
		ID:          implementerID,
	})
	if err != nil {
		return db.ListReviewerCandidatesRow{}, fmt.Errorf("list reviewer candidates: %w", err)
	}
	if len(candidates) == 0 {
		return db.ListReviewerCandidatesRow{}, ErrReviewerUnconfigured
	}
	return candidates[0], nil
}

func (s *ReviewCellService) createReviewTask(ctx context.Context, qtx *db.Queries, issue db.Issue, candidate db.AgentTaskQueue) (bool, *db.AgentTaskQueue, error) {
	// The issue row is held FOR UPDATE by the caller. The partial unique index
	// below protects open tasks, but it intentionally excludes completed rows;
	// retain completed review history as the idempotency boundary for this exact
	// candidate lineage so repeated drain ticks cannot re-dispatch it.
	if existing, err := qtx.GetReviewTaskForCandidate(ctx, db.GetReviewTaskForCandidateParams{
		IssueID:            issue.ID,
		ReviewTargetTaskID: candidate.ID,
	}); err == nil {
		return false, &existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, nil, fmt.Errorf("read review task replay: %w", err)
	}

	reviewer, err := s.selectReviewer(ctx, qtx, issue, candidate.AgentID)
	if err != nil {
		return false, nil, err
	}
	if !reviewer.RuntimeID.Valid {
		return false, nil, ErrReviewerUnavailable
	}

	payload, err := json.Marshal(reviewTaskPayload{
		Kind:            TaskKindReview,
		CandidateTaskID: util.UUIDToString(candidate.ID),
	})
	if err != nil {
		return false, nil, fmt.Errorf("encode review task payload: %w", err)
	}

	priority := s.Config.ReviewPriority
	if priority <= 0 {
		priority = 5
	}
	task, err := qtx.CreateReviewTask(ctx, db.CreateReviewTaskParams{
		AgentID:            reviewer.ID,
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
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("create review task: %w", err)
	}
	return true, &task, nil
}

func reviewTaskEnsureResult(created bool, task *db.AgentTaskQueue, candidateID pgtype.UUID) ReviewTaskEnsureResult {
	result := ReviewTaskEnsureResult{Created: created, CandidateTaskID: candidateID}
	if task != nil {
		result.ReviewTaskID = task.ID
		result.Replayed = !created
	}
	return result
}

// OnIssueLeftReview resets the acceptance axis when the issue leaves in_review:
// open review tasks are cancelled and review_state is cleared.
func (s *ReviewCellService) OnIssueLeftReview(ctx context.Context, issueID pgtype.UUID) error {
	return s.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, issueID)
		if err != nil {
			return fmt.Errorf("review cell: load issue: %w", err)
		}
		if !issue.ReviewState.Valid {
			return nil
		}
		if _, err := qtx.CancelOpenReviewTasksForIssue(ctx, issueID); err != nil {
			return fmt.Errorf("review cell: cancel open review tasks: %w", err)
		}
		if _, err := qtx.ClearIssueReviewState(ctx, issueID); err != nil {
			return fmt.Errorf("review cell: clear review state: %w", err)
		}
		return nil
	})
}

// WriteVerdict applies a reviewer/coordinator verdict to the issue's open
// review task. revise -> revise_requested + repair task (assigned to the
// implementer); pass -> accepted + delivery outcome (issue done). The review
// task is completed with a structured receipt and the verdict lands as a
// Task-linked comment on the issue.
func (s *ReviewCellService) WriteVerdict(ctx context.Context, issueID pgtype.UUID, actor ReviewActor, in VerdictInput) (VerdictResult, error) {
	var result VerdictResult
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, issueID)
		if err != nil {
			return fmt.Errorf("review cell: load issue: %w", err)
		}
		task, err := qtx.GetOpenReviewTaskForIssue(ctx, issueID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoOpenReviewTask
		}
		if err != nil {
			return fmt.Errorf("review cell: load open review task: %w", err)
		}
		candidate, err := qtx.GetAgentTask(ctx, task.ReviewTargetTaskID)
		if err != nil {
			return fmt.Errorf("review cell: load candidate task: %w", err)
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
			return fmt.Errorf("review cell: invalid verdict %q", in.Verdict)
		}

		if actor.ActorType == "agent" {
			isAssignedReviewer := uuidEqual(task.AgentID, actor.ActorID)
			isCoordinator := s.Config.CoordinatorAgentSet && uuidEqual(s.Config.CoordinatorAgentID, actor.ActorID)
			if in.Verdict == "pass" {
				if !isCoordinator {
					return ErrNotCoordinator
				}
			} else if !isAssignedReviewer && !isCoordinator {
				return ErrNotAssignedReviewer
			}
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
			return fmt.Errorf("review cell: state transition: %w", err)
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
			return fmt.Errorf("review cell: encode receipt: %w", err)
		}
		if _, err := qtx.CompleteReviewTask(ctx, db.CompleteReviewTaskParams{ID: task.ID, Result: receipt}); err != nil {
			return fmt.Errorf("review cell: complete review task: %w", err)
		}

		if _, err := qtx.CreateComment(ctx, db.CreateCommentParams{
			IssueID:      issue.ID,
			WorkspaceID:  issue.WorkspaceID,
			AuthorType:   actor.ActorType,
			AuthorID:     actor.ActorID,
			Content:      verdictCommentContent(in, task, candidate, actor),
			Type:         "comment",
			ParentID:     pgtype.UUID{},
			SourceTaskID: task.ID,
		}); err != nil {
			return fmt.Errorf("review cell: persist verdict comment: %w", err)
		}

		result = VerdictResult{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, ReviewState: target, ReviewTaskID: task.ID}

		// PASS -> delivery outcome: the accepted issue leaves the in_review
		// delivery axis and becomes done. REVISE -> create the rework task for
		// the implementer and keep the issue in_review while the repair round
		// runs.
		if in.Verdict == "pass" {
			if _, err := qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
				ID:          issue.ID,
				Status:      "done",
				WorkspaceID: issue.WorkspaceID,
			}); err != nil {
				return fmt.Errorf("review cell: move accepted issue to done: %w", err)
			}
			// Keep invariant C1 (review_state IS NOT NULL => status = 'in_review').
			// The durable verdict lives on the completed review task receipt, the
			// verdict comment and the review:verdict event; the acceptance axis is
			// cleared so a later re-open starts a fresh review round.
			if _, err := qtx.ClearIssueReviewState(ctx, issue.ID); err != nil {
				return fmt.Errorf("review cell: clear accepted review state: %w", err)
			}
		} else {
			repairTask, err := s.createRepairTask(ctx, qtx, issue, candidate, task, in)
			if err != nil {
				return err
			}
			result.RepairTaskID = repairTask.ID
		}
		return nil
	})
	if err == nil {
		s.publishVerdict(ctx, result, actor, in)
	}
	return result, err
}

func (s *ReviewCellService) createRepairTask(ctx context.Context, qtx *db.Queries, issue db.Issue, candidate db.AgentTaskQueue, reviewTask db.AgentTaskQueue, in VerdictInput) (*db.AgentTaskQueue, error) {
	existing, err := qtx.GetRepairTaskByEvidence(ctx, db.GetRepairTaskByEvidenceParams{
		IssueID:              issue.ID,
		TriggerEvidenceRefID: reviewTask.ID,
	})
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read repair task replay: %w", err)
	}

	implementer, err := qtx.GetAgent(ctx, candidate.AgentID)
	if err != nil {
		return nil, fmt.Errorf("load implementer agent: %w", err)
	}
	if !implementer.RuntimeID.Valid {
		return nil, fmt.Errorf("review cell: implementer has no claimable runtime for repair")
	}

	payload, err := json.Marshal(repairTaskPayload{
		Kind:               TaskKindRepair,
		CandidateTaskID:    util.UUIDToString(candidate.ID),
		ReviewTaskID:       util.UUIDToString(reviewTask.ID),
		RepairRequirements: in.RepairRequirements,
	})
	if err != nil {
		return nil, fmt.Errorf("encode repair task payload: %w", err)
	}

	priority := s.Config.RepairPriority
	if priority <= 0 {
		priority = 5
	}
	feedback := strings.TrimSpace(in.Notes)
	if feedback == "" && len(in.RepairRequirements) > 0 {
		feedback = "Address the listed repair requirements."
	}
	task, err := qtx.CreateRepairTask(ctx, db.CreateRepairTaskParams{
		AgentID:            candidate.AgentID,
		RuntimeID:          implementer.RuntimeID,
		IssueID:            issue.ID,
		Priority:           priority,
		ReviewTargetTaskID: candidate.ID,
		TriggerSummary: pgtype.Text{
			String: fmt.Sprintf("Repair rework for issue %q after review REVISE", issue.Title),
			Valid:  true,
		},
		Context:              payload,
		TriggerEvidenceRefID: reviewTask.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("create repair task: %w", err)
	}
	_ = feedback
	return &task, nil
}

// OnRepairTaskCompleted is the TaskCompleted listener entry for task_kind ==
// 'repair'. It re-enters the issue into review and creates a fresh, independent
// review round for the repaired candidate.
func (s *ReviewCellService) OnRepairTaskCompleted(ctx context.Context, taskID pgtype.UUID) error {
	return s.runInTx(ctx, func(qtx *db.Queries) error {
		task, err := qtx.GetAgentTask(ctx, taskID)
		if err != nil {
			return fmt.Errorf("review cell: load repair task: %w", err)
		}
		if task.TaskKind != TaskKindRepair {
			return nil
		}
		if task.Status != "completed" {
			return nil
		}
		issue, err := qtx.GetIssueForUpdate(ctx, task.IssueID)
		if err != nil {
			return fmt.Errorf("review cell: load issue for re-review: %w", err)
		}
		if issue.Status != "in_review" {
			// The delivery axis is authoritative; only re-review when the
			// issue is actually sitting in review.
			return nil
		}
		if issue.ReviewState.Valid && issue.ReviewState.String != ReviewStateReviseRequested {
			return nil
		}
		// Cancel any open review round (defensive: should not exist) and
		// start a fresh independent review of the repaired candidate.
		if _, err := qtx.CancelOpenReviewTasksForIssue(ctx, issue.ID); err != nil {
			return fmt.Errorf("review cell: cancel stale review task: %w", err)
		}
		created, _, err := s.createReviewTask(ctx, qtx, issue, task)
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
			return fmt.Errorf("review cell: requeue after repair: %w", err)
		}
		return nil
	})
}

// Requeue re-runs candidate lineage for an owner_decision issue. Valid lineage
// -> owner_decision -> queued plus a fresh review task; still-invalid lineage
// keeps owner_decision and refreshes the reason. Coordinator agents and member
// owners may call it; the second call is a no-op (idempotent).
func (s *ReviewCellService) Requeue(ctx context.Context, issueID pgtype.UUID, actor ReviewActor) (RequeueResult, error) {
	if actor.ActorType == "agent" && !(s.Config.CoordinatorAgentSet && uuidEqual(s.Config.CoordinatorAgentID, actor.ActorID)) {
		return RequeueResult{}, ErrNotCoordinator
	}
	var res RequeueResult
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		issue, err := qtx.GetIssueForUpdate(ctx, issueID)
		if err != nil {
			return fmt.Errorf("review cell: load issue: %w", err)
		}
		if !issue.ReviewState.Valid || issue.ReviewState.String != ReviewStateOwnerDecision {
			res.ReviewState = reviewStateOrNull(issue.ReviewState)
			return ErrNotInOwnerDecision
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
				return fmt.Errorf("review cell: refresh requeue reason: %w", err)
			}
			res.ReviewState = ReviewStateOwnerDecision
			res.ReviewTaskCreated = false
			res.Reason = reason
			return nil
		}
		created, _, err := s.createReviewTask(ctx, qtx, issue, lineage.Task)
		if err != nil {
			return s.failClosedFromOpen(ctx, qtx, issue, lineageFailureForErr(err))
		}
		if _, err := qtx.SetIssueReviewStateFromOpen(ctx, db.SetIssueReviewStateFromOpenParams{
			ID:        issue.ID,
			NewState:  pgtype.Text{String: ReviewStateQueued, Valid: true},
			NewReason: pgtype.Text{},
		}); err != nil {
			return fmt.Errorf("review cell: requeue transition: %w", err)
		}
		res.ReviewState = ReviewStateQueued
		res.ReviewTaskCreated = created
		return nil
	})
	return res, err
}

func (s *ReviewCellService) ListReviewQueue(ctx context.Context, workspaceID pgtype.UUID) ([]db.ListReviewQueueRow, error) {
	if s.Queries == nil {
		return nil, ErrCompanyOpsArtifactUnavailable
	}
	return s.Queries.ListReviewQueue(ctx, workspaceID)
}

type reviewTaskPayload struct {
	Kind            string `json:"kind"`
	CandidateTaskID string `json:"candidate_task_id"`
}

type repairTaskPayload struct {
	Kind               string   `json:"kind"`
	CandidateTaskID    string   `json:"candidate_task_id"`
	ReviewTaskID       string   `json:"review_task_id"`
	RepairRequirements []string `json:"repair_requirements,omitempty"`
}

type verdictReceipt struct {
	Verdict            string   `json:"verdict"`
	ReviewState        string   `json:"review_state"`
	ReviewerAgentID    string   `json:"reviewer_agent_id"`
	CandidateTaskID    string   `json:"candidate_task_id"`
	Notes              string   `json:"notes"`
	RepairRequirements []string `json:"repair_requirements,omitempty"`
}

func (s *ReviewCellService) runInTx(ctx context.Context, fn func(*db.Queries) error) error {
	if s.TxStarter == nil {
		return fn(s.Queries)
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("review cell: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *ReviewCellService) publishEscalated(ctx context.Context, issue db.Issue, reason, subReason string) {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(events.Event{
		Type:        eventReviewEscalated,
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

func (s *ReviewCellService) publishVerdict(ctx context.Context, result VerdictResult, actor ReviewActor, in VerdictInput) {
	if s.Bus == nil {
		return
	}
	s.Bus.Publish(events.Event{
		Type:        eventReviewVerdict,
		WorkspaceID: util.UUIDToString(result.WorkspaceID),
		ActorType:   actor.ActorType,
		ActorID:     util.UUIDToString(actor.ActorID),
		Payload: map[string]any{
			"issue_id":       util.UUIDToString(result.IssueID),
			"review_state":   result.ReviewState,
			"verdict":        in.Verdict,
			"review_task_id": util.UUIDToString(result.ReviewTaskID),
			"repair_task_id": uuidOrEmpty(result.RepairTaskID),
		},
	})
}

func verdictCommentContent(in VerdictInput, task db.AgentTaskQueue, candidate db.AgentTaskQueue, actor ReviewActor) string {
	var b strings.Builder
	if in.Verdict == "pass" {
		b.WriteString("Review passed. Candidate accepted.")
	} else {
		b.WriteString("Review requested changes. Reopen/repair required.")
	}
	if in.Notes != "" {
		b.WriteString("\n\n")
		b.WriteString(in.Notes)
	}
	if len(in.RepairRequirements) > 0 {
		b.WriteString("\n\nRepair requirements:\n")
		for _, req := range in.RepairRequirements {
			b.WriteString("- ")
			b.WriteString(req)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func isOpenReviewState(state string) bool {
	switch state {
	case ReviewStateQueued, ReviewStateTriaging, ReviewStateEvidenceReview, ReviewStateOwnerDecision:
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
		return "creation_failed"
	}
}

func reviewStateOrNull(state pgtype.Text) string {
	if !state.Valid {
		return ""
	}
	return state.String
}

func uuidOrEmpty(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return util.UUIDToString(id)
}
