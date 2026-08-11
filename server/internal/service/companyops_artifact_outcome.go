package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const companyOpsArtifactMaximumBytes = 8 << 20

var (
	ErrCompanyOpsArtifactUnavailable = errors.New("CompanyOps artifact writer is unavailable")
	ErrCompanyOpsArtifactConflict    = errors.New("CompanyOps artifact lifecycle conflict")
)

// CompanyOpsArtifactOutcome is the operator-facing read model for one
// assignment lineage. It references the canonical Issue and Run receipts and
// never becomes a second Project or WorkOrder authority.
type CompanyOpsArtifactOutcome struct {
	CommandID      pgtype.UUID
	IssueID        pgtype.UUID
	LocalAgentID   pgtype.UUID
	InitialTaskID  pgtype.UUID
	CurrentTaskID  pgtype.UUID
	ExecutionState string
	Candidate      *companyops.ArtifactCandidate
	Projection     *companyops.ArtifactLifecycleProjection
}

// CompanyOpsArtifactReview is an append-only Owner decision over the exact
// active candidate. Review notes stay in the existing conversation; the event
// ledger records the durable decision receipt.
type CompanyOpsArtifactReview struct {
	CandidateID   string
	Decision      companyops.ArtifactEventType
	IdempotencyID string
	Feedback      string
	ActorUserID   pgtype.UUID
}

type CompanyOpsArtifactReviewReceipt struct {
	Event      companyops.ArtifactEvent
	ReworkTask *db.AgentTaskQueue
}

type companyOpsArtifactSource struct {
	snapshot companyops.ArtifactSourceSnapshot
}

func (s companyOpsArtifactSource) ReadArtifactSource(
	context.Context,
	companyops.ArtifactMaterializationRequest,
) (companyops.ArtifactSourceSnapshot, error) {
	return s.snapshot, nil
}

// CompanyOpsArtifactService turns a completed canonical Run into one durable
// temporary artifact candidate and exposes exact lifecycle readback/review.
type CompanyOpsArtifactService struct {
	queries     *db.Queries
	txStarter   companyops.ArtifactPersistenceTxStarter
	taskService *TaskService
	repo        *companyops.DurableArtifactMaterializationRepository
	store       companyops.ArtifactMaterializationObjectStore
}

func NewCompanyOpsArtifactService(
	queries *db.Queries,
	txStarter companyops.ArtifactPersistenceTxStarter,
	store companyops.ArtifactMaterializationObjectStore,
	taskService *TaskService,
) (*CompanyOpsArtifactService, error) {
	if queries == nil || txStarter == nil || store == nil || taskService == nil {
		return nil, ErrCompanyOpsArtifactUnavailable
	}
	repo, err := companyops.NewDurableArtifactMaterializationRepository(txStarter)
	if err != nil {
		return nil, err
	}
	return &CompanyOpsArtifactService{
		queries: queries, txStarter: txStarter, taskService: taskService, repo: repo, store: store,
	}, nil
}

func (s *CompanyOpsArtifactService) MaterializeCompletedTask(
	ctx context.Context,
	workspaceID string,
	task db.AgentTaskQueue,
	output string,
	prURL string,
) (*CompanyOpsArtifactOutcome, error) {
	if s == nil || s.queries == nil || s.repo == nil || s.store == nil {
		return nil, ErrCompanyOpsArtifactUnavailable
	}
	lineage, err := resolveCompanyOpsAssignmentLineage(ctx, s.queries, task)
	if err != nil {
		return nil, err
	}
	if lineage == nil {
		return nil, nil
	}
	if util.UUIDToString(lineage.workspaceID) != workspaceID {
		return nil, fmt.Errorf("%w: task workspace does not match assignment lineage", ErrCompanyOpsArtifactConflict)
	}
	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(s.queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("read completed Run receipt: %w", err)
	}
	if receipt.Terminal == nil || receipt.Terminal.Status != "completed" {
		return nil, fmt.Errorf("%w: Run has no completed execution receipt", ErrCompanyOpsArtifactConflict)
	}

	if _, err := s.repo.GetArtifactCandidate(ctx, workspaceID, util.UUIDToString(task.ID)); err == nil {
		return s.GetIssueOutcome(ctx, lineage.workspaceID, lineage.receipt.IssueID)
	} else if !errors.Is(err, companyops.ErrArtifactCandidateNotFound) {
		return nil, err
	}

	revision := 1
	supersedesID := ""
	projection, err := s.repo.GetArtifactLifecycleProjection(ctx, workspaceID, util.UUIDToString(lineage.commandID))
	if err == nil {
		if projection.Status != companyops.ArtifactEventChangesRequested {
			return nil, fmt.Errorf("%w: a new revision requires an active changes_requested decision", ErrCompanyOpsArtifactConflict)
		}
		revision = projection.CandidateRevision + 1
		supersedesID = projection.CandidateID
	} else if !errors.Is(err, companyops.ErrArtifactCandidateNotFound) {
		return nil, err
	}

	body, err := companyOpsArtifactMarkdown(lineage.receipt, task, output, prURL)
	if err != nil {
		return nil, err
	}
	request := companyops.ArtifactMaterializationRequest{
		WorkspaceID:    workspaceID,
		CandidateID:    util.UUIDToString(task.ID),
		LineageID:      util.UUIDToString(lineage.commandID),
		Revision:       revision,
		SupersedesID:   supersedesID,
		IdempotencyKey: "completed-run:" + util.UUIDToString(task.ID),
	}
	materializer := companyops.NewArtifactMaterializer(companyOpsArtifactSource{
		snapshot: companyops.ArtifactSourceSnapshot{
			Bytes:       []byte(body),
			Filename:    fmt.Sprintf("hivecrew-outcome-r%d-%s.md", revision, util.UUIDToString(task.ID)),
			ContentType: "text/markdown; charset=utf-8",
		},
	}, s.store, s.repo)
	if _, err := materializer.Materialize(ctx, request); err != nil {
		return nil, err
	}
	return s.GetIssueOutcome(ctx, lineage.workspaceID, lineage.receipt.IssueID)
}

func (s *CompanyOpsArtifactService) GetIssueOutcome(
	ctx context.Context,
	workspaceID pgtype.UUID,
	issueID pgtype.UUID,
) (*CompanyOpsArtifactOutcome, error) {
	if s == nil || s.queries == nil || s.repo == nil {
		return nil, ErrCompanyOpsArtifactUnavailable
	}
	receiptRow, err := s.queries.GetLatestAssignmentDispatchReceiptByIssue(ctx, db.GetLatestAssignmentDispatchReceiptByIssueParams{
		WorkspaceID: workspaceID,
		IssueID:     issueID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read latest assignment receipt: %w", err)
	}
	receipt := assignmentDispatchReceiptFromDB(receiptRow)
	outcome := &CompanyOpsArtifactOutcome{
		CommandID:      receipt.CommandID,
		IssueID:        receipt.IssueID,
		LocalAgentID:   receipt.LocalAgentID,
		InitialTaskID:  receipt.InitialTaskID,
		CurrentTaskID:  receipt.InitialTaskID,
		ExecutionState: "awaiting_claim",
	}

	workspace := util.UUIDToString(workspaceID)
	lineageID := util.UUIDToString(receipt.CommandID)
	projection, err := s.repo.GetArtifactLifecycleProjection(ctx, workspace, lineageID)
	if errors.Is(err, companyops.ErrArtifactCandidateNotFound) {
		outcome.ExecutionState, err = companyOpsCurrentExecutionState(ctx, s.queries, outcome.CurrentTaskID)
		return outcome, err
	}
	if err != nil {
		return nil, err
	}
	candidate, err := s.repo.GetArtifactCandidate(ctx, workspace, projection.CandidateID)
	if err != nil {
		return nil, err
	}
	outcome.Candidate = &candidate
	outcome.Projection = &projection
	outcome.CurrentTaskID = util.MustParseUUID(candidate.ID)
	if projection.Status == companyops.ArtifactEventChangesRequested {
		events, listErr := s.repo.ListArtifactEvents(ctx, workspace, lineageID)
		if listErr != nil || len(events) == 0 {
			return nil, fmt.Errorf("%w: changes_requested event is unavailable", ErrCompanyOpsArtifactConflict)
		}
		last := events[len(events)-1]
		if last.Type != companyops.ArtifactEventChangesRequested {
			return nil, fmt.Errorf("%w: artifact projection and event ledger disagree", ErrCompanyOpsArtifactConflict)
		}
		task, taskErr := s.queries.GetCompanyOpsTaskByTriggerEvidence(ctx, db.GetCompanyOpsTaskByTriggerEvidenceParams{
			IssueID:              issueID,
			TriggerEvidenceKind:  pgtype.Text{String: artifactRevisionEvidenceKind, Valid: true},
			TriggerEvidenceRefID: util.MustParseUUID(last.ID),
		})
		if taskErr != nil {
			return nil, fmt.Errorf("%w: exact rework Run is unavailable", ErrCompanyOpsArtifactConflict)
		}
		outcome.CurrentTaskID = task.ID
	}
	outcome.ExecutionState, err = companyOpsCurrentExecutionState(ctx, s.queries, outcome.CurrentTaskID)
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

func companyOpsCurrentExecutionState(
	ctx context.Context,
	queries *db.Queries,
	taskID pgtype.UUID,
) (string, error) {
	if execution, err := NewCompanyOpsPersistenceRepositoryWithQueries(queries).GetExecutionReceipt(ctx, taskID); err == nil {
		if execution.Terminal != nil {
			return execution.Terminal.Status, nil
		}
		return "running", nil
	} else if !errors.Is(err, ErrExecutionReceiptNotFound) {
		return "", err
	}
	task, err := queries.GetAgentTask(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("read current CompanyOps Run: %w", err)
	}
	switch task.Status {
	case "queued", "dispatched", "waiting_local_directory", "deferred":
		return "awaiting_claim", nil
	case "running":
		return "running", nil
	case "completed", "failed", "cancelled":
		return task.Status, nil
	default:
		return "", fmt.Errorf("%w: unsupported current Run status %q", ErrCompanyOpsArtifactConflict, task.Status)
	}
}

func (s *CompanyOpsArtifactService) ReviewArtifact(
	ctx context.Context,
	workspaceID pgtype.UUID,
	issueID pgtype.UUID,
	review CompanyOpsArtifactReview,
) (CompanyOpsArtifactReviewReceipt, error) {
	if review.Decision != companyops.ArtifactEventChangesRequested && review.Decision != companyops.ArtifactEventApproved {
		return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("%w: unsupported Owner decision", ErrCompanyOpsArtifactConflict)
	}
	if review.IdempotencyID == "" {
		return CompanyOpsArtifactReviewReceipt{}, companyops.ErrArtifactIdempotencyRequired
	}
	if !review.ActorUserID.Valid {
		return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("%w: Owner identity is required", ErrCompanyOpsArtifactConflict)
	}
	if review.Decision == companyops.ArtifactEventChangesRequested && strings.TrimSpace(review.Feedback) == "" {
		return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("%w: rework feedback is required", ErrCompanyOpsArtifactConflict)
	}
	outcome, err := s.GetIssueOutcome(ctx, workspaceID, issueID)
	if err != nil {
		return CompanyOpsArtifactReviewReceipt{}, err
	}
	if outcome == nil || outcome.Candidate == nil || outcome.Projection == nil || outcome.Candidate.ID != review.CandidateID {
		return CompanyOpsArtifactReviewReceipt{}, companyops.ErrArtifactCandidateNotFound
	}
	candidate := *outcome.Candidate
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("begin artifact review transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	queries := db.New(tx)
	issue, err := queries.GetIssue(ctx, issueID)
	if err != nil || issue.WorkspaceID != workspaceID || issue.AssigneeID != outcome.LocalAgentID {
		return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("%w: Issue or assignee changed before review", ErrCompanyOpsArtifactConflict)
	}
	event, err := companyops.NewArtifactPersistenceRepository(tx).AppendArtifactEvent(
		ctx,
		util.UUIDToString(workspaceID),
		util.UUIDToString(outcome.CommandID),
		companyops.ArtifactEventInput{
			Type:               review.Decision,
			CandidateID:        candidate.ID,
			CandidateRevision:  candidate.Revision,
			CandidateDigest:    candidate.Digest,
			CandidateObjectRef: candidate.DurableObjectRef,
			IdempotencyKey:     "owner-review:" + review.IdempotencyID,
		},
	)
	if err != nil {
		return CompanyOpsArtifactReviewReceipt{}, err
	}
	var reworkTask *db.AgentTaskQueue
	reworkCreated := false
	if review.Decision == companyops.ArtifactEventChangesRequested {
		eventID := util.MustParseUUID(event.ID)
		existing, lookupErr := queries.GetCompanyOpsTaskByTriggerEvidence(ctx, db.GetCompanyOpsTaskByTriggerEvidenceParams{
			IssueID:              issue.ID,
			TriggerEvidenceKind:  pgtype.Text{String: artifactRevisionEvidenceKind, Valid: true},
			TriggerEvidenceRefID: eventID,
		})
		if lookupErr == nil {
			reworkTask = &existing
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("read artifact rework Run replay: %w", lookupErr)
		} else {
			task, err := s.taskService.prepareIssueTaskWithCommentPlan(
				ctx,
				queries,
				issue,
				pgtype.UUID{},
				nil,
				false,
				strings.TrimSpace(review.Feedback),
				review.ActorUserID,
				pgtype.UUID{},
				&issueTaskTriggerEvidenceOverride{Kind: artifactRevisionEvidenceKind, RefID: eventID},
			)
			if err != nil {
				return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("prepare artifact rework Run: %w", err)
			}
			reworkTask = &task
			reworkCreated = true
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("commit artifact review transaction: %w", err)
	}
	committed = true
	if reworkTask != nil && reworkCreated {
		s.taskService.broadcastTaskEvent(ctx, protocol.EventTaskQueued, *reworkTask)
		s.taskService.NotifyTaskEnqueued(ctx, *reworkTask)
	}
	return CompanyOpsArtifactReviewReceipt{Event: event, ReworkTask: reworkTask}, nil
}

func companyOpsArtifactMarkdown(
	receipt AssignmentDispatchReceipt,
	task db.AgentTaskQueue,
	output string,
	prURL string,
) (string, error) {
	output = strings.TrimSpace(output)
	prURL = strings.TrimSpace(prURL)
	if output == "" && prURL == "" {
		return "", fmt.Errorf("%w: completed Run produced no operator-visible output", ErrCompanyOpsArtifactConflict)
	}
	var body strings.Builder
	body.WriteString("# HiveCrew Temporary Artifact\n\n")
	fmt.Fprintf(&body, "- WorkOrder: `%s`\n", receipt.Target.WorkOrderRef)
	fmt.Fprintf(&body, "- Assignment command: `%s`\n", util.UUIDToString(receipt.CommandID))
	fmt.Fprintf(&body, "- Run: `%s`\n", util.UUIDToString(task.ID))
	fmt.Fprintf(&body, "- Employee: `%s`\n", receipt.Target.EmployeeRef)
	fmt.Fprintf(&body, "- Agent: `%s`\n\n", receipt.Target.AgentRef)
	if output != "" {
		body.WriteString("## Result\n\n")
		body.WriteString(output)
		body.WriteString("\n\n")
	}
	if prURL != "" {
		body.WriteString("## Pull request\n\n")
		fmt.Fprintf(&body, "%s\n", prURL)
	}
	if body.Len() > companyOpsArtifactMaximumBytes {
		return "", fmt.Errorf("%w: temporary artifact exceeds %d bytes", ErrCompanyOpsArtifactConflict, companyOpsArtifactMaximumBytes)
	}
	return body.String(), nil
}
