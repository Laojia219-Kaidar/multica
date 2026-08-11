package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

// CompanyOpsFormalArtifactAuthority is the write/read boundary against the
// HiveCosm Formal Artifact authority. Production wires the authenticated
// HiveCosmAuthorityClient; a nil authority fails closed at construction so no
// promotion can synthesize a formal artifact.
type CompanyOpsFormalArtifactAuthority interface {
	PromoteFormalArtifact(ctx context.Context, input companyops.HiveCosmFormalArtifactPromotionRequest) (companyops.HiveCosmFormalArtifactPromotionReceipt, error)
	ReadFormalArtifact(ctx context.Context, lookup companyops.HiveCosmAuthorityLookup, expectedCandidate companyops.HiveCosmFormalArtifactCandidate, artifactManifestID string) (companyops.HiveCosmFormalArtifact, error)
}

// CompanyOpsArtifactPromotion carries the exact active candidate, a stable
// promotion id, the required Owner actor, and the freshly resolved authority
// chain that HiveCosm compares against its current state. The service never
// invents authority snapshots or promotion ids.
type CompanyOpsArtifactPromotion struct {
	CandidateID     string
	PromotionID     string
	ActorUserID     pgtype.UUID
	Lookup          companyops.HiveCosmAuthorityLookup
	WorkOrder       companyops.AuthoritySnapshot
	Employee        companyops.AuthoritySnapshot
	IdentityBinding companyops.AuthoritySnapshot
}

// CompanyOpsArtifactPromotionReceipt is the durable result of one promotion
// attempt. FormalArtifactRef and FormalVisible are populated only after the
// independent GET readback confirms the authority_writeback_confirmed state.
type CompanyOpsArtifactPromotionReceipt struct {
	PromotionID       string
	CandidateID       string
	LifecycleStatus   companyops.ArtifactEventType
	FormalArtifactRef string
	FormalVisible     bool
	WritePerformed    bool
	TerminalEvent     companyops.ArtifactEvent
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
	queries         *db.Queries
	txStarter       companyops.ArtifactPersistenceTxStarter
	taskService     *TaskService
	repo            *companyops.DurableArtifactMaterializationRepository
	store           companyops.ArtifactMaterializationObjectStore
	formalAuthority CompanyOpsFormalArtifactAuthority
}

func NewCompanyOpsArtifactService(
	queries *db.Queries,
	txStarter companyops.ArtifactPersistenceTxStarter,
	store companyops.ArtifactMaterializationObjectStore,
	taskService *TaskService,
	formalAuthority CompanyOpsFormalArtifactAuthority,
) (*CompanyOpsArtifactService, error) {
	if queries == nil || txStarter == nil || store == nil || taskService == nil || formalAuthority == nil {
		return nil, ErrCompanyOpsArtifactUnavailable
	}
	repo, err := companyops.NewDurableArtifactMaterializationRepository(txStarter)
	if err != nil {
		return nil, err
	}
	return &CompanyOpsArtifactService{
		queries: queries, txStarter: txStarter, taskService: taskService, repo: repo, store: store,
		formalAuthority: formalAuthority,
	}, nil
}

func (s *CompanyOpsArtifactService) MaterializeCompletedTask(
	ctx context.Context,
	workspaceID string,
	task db.AgentTaskQueue,
	output string,
	prURL string,
) (*CompanyOpsArtifactOutcome, error) {
	if s == nil || s.queries == nil || s.repo == nil || s.store == nil || s.formalAuthority == nil {
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
	if s == nil || s.queries == nil || s.repo == nil || s.formalAuthority == nil {
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

// PromoteArtifact drives the approved → promotion_requested → HiveCosm Formal
// Artifact authority POST → promotion_succeeded → independent GET readback →
// authority_readback_confirmed loop. It is fully re-entrant: a replay after a
// crash resumes from the durable event ledger without duplicating the external
// POST, and a genuine retry after promotion_failed re-issues the same stable
// promotion id so HiveCosm collapses it to one formal artifact.
func (s *CompanyOpsArtifactService) PromoteArtifact(
	ctx context.Context,
	workspaceID pgtype.UUID,
	issueID pgtype.UUID,
	promotion CompanyOpsArtifactPromotion,
) (CompanyOpsArtifactPromotionReceipt, error) {
	if s == nil || s.queries == nil || s.repo == nil || s.formalAuthority == nil {
		return CompanyOpsArtifactPromotionReceipt{}, ErrCompanyOpsArtifactUnavailable
	}
	if !promotion.ActorUserID.Valid || promotion.ActorUserID.Bytes == ([16]byte{}) {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: Owner identity is required", ErrCompanyOpsArtifactConflict)
	}
	promotionID := strings.TrimSpace(promotion.PromotionID)
	if parsed, err := util.ParseUUID(promotionID); err != nil || util.UUIDToString(parsed) != promotionID {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: promotion_id must be a canonical UUID", ErrCompanyOpsArtifactConflict)
	}

	workspace := util.UUIDToString(workspaceID)
	outcome, err := s.GetIssueOutcome(ctx, workspaceID, issueID)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	if outcome == nil || outcome.Candidate == nil || outcome.Projection == nil {
		return CompanyOpsArtifactPromotionReceipt{}, companyops.ErrArtifactCandidateNotFound
	}
	if outcome.Candidate.ID != strings.TrimSpace(promotion.CandidateID) {
		return CompanyOpsArtifactPromotionReceipt{}, companyops.ErrArtifactCandidateNotFound
	}
	candidate := *outcome.Candidate
	projection := *outcome.Projection
	lineageID := util.UUIDToString(outcome.CommandID)

	events, err := s.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	approvalEvent, lastEvent, hasLast := companyOpsArtifactPromotionAnchor(events, candidate.ID)
	if approvalEvent.ID == "" {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: approved decision is unavailable for promotion", ErrCompanyOpsArtifactConflict)
	}

	// The promotion id is anchored by the first approved→requested transition
	// and never re-established. Resolve the durable id from the ledger before
	// touching the authority: a replay that carries a different id, or a ledger
	// that already mixes ids, fails closed without appending events, POSTing,
	// reading, or mutating the projection.
	anchoredPromotionID, established, err := companyOpsResolveAnchoredPromotionID(events, candidate.ID)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	effectivePromotionID := promotionID
	if established {
		if anchoredPromotionID != promotionID {
			return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: promotion_id does not match the anchored promotion for this candidate", ErrCompanyOpsArtifactConflict)
		}
		effectivePromotionID = anchoredPromotionID
	}

	// Build the full canonical claim payload once so every entry point —
	// approved, requested, failed, succeeded, readback — establishes or
	// verifies the durable claim before any external authority call or
	// terminal return.
	payload := companyOpsArtifactClaimPayload(promotion, candidate, approvalEvent.ID)

	switch projection.Status {
	case companyops.ArtifactEventAuthorityReadbackConfirmed:
		if err := s.repo.VerifyPromotion(ctx, workspace, effectivePromotionID, candidate.ID, lineageID, payload); err != nil {
			return CompanyOpsArtifactPromotionReceipt{}, err
		}
		return CompanyOpsArtifactPromotionReceipt{
			PromotionID:       effectivePromotionID,
			CandidateID:       candidate.ID,
			LifecycleStatus:   projection.Status,
			FormalArtifactRef: projection.FormalArtifactRef,
			FormalVisible:     projection.FormalVisible,
			TerminalEvent:     lastEvent,
		}, nil
	case companyops.ArtifactEventPromotionSucceeded:
		if err := s.repo.VerifyPromotion(ctx, workspace, effectivePromotionID, candidate.ID, lineageID, payload); err != nil {
			return CompanyOpsArtifactPromotionReceipt{}, err
		}
		succeededRef := companyOpsArtifactSucceededRef(events, candidate.ID)
		return s.runArtifactReadback(ctx, workspace, lineageID, effectivePromotionID, candidate, approvalEvent.ID, promotion.Lookup, succeededRef, false)
	case companyops.ArtifactEventApproved,
		companyops.ArtifactEventPromotionRequested,
		companyops.ArtifactEventPromotionFailed:
		return s.attemptArtifactPromotion(ctx, workspace, lineageID, effectivePromotionID, candidate, promotion, approvalEvent, lastEvent, hasLast)
	default:
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: artifact is not approved for promotion", ErrCompanyOpsArtifactConflict)
	}
}

func (s *CompanyOpsArtifactService) attemptArtifactPromotion(
	ctx context.Context,
	workspace string,
	lineageID string,
	promotionID string,
	candidate companyops.ArtifactCandidate,
	promotion CompanyOpsArtifactPromotion,
	approvalEvent companyops.ArtifactEvent,
	lastEvent companyops.ArtifactEvent,
	hasLast bool,
) (CompanyOpsArtifactPromotionReceipt, error) {
	payload := companyOpsArtifactClaimPayload(promotion, candidate, approvalEvent.ID)
	if err := s.repo.ClaimPromotion(ctx, workspace, promotionID, candidate.ID, lineageID, payload); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	requested, err := s.ensureArtifactPromotionRequested(ctx, workspace, lineageID, promotionID, candidate, lastEvent, hasLast)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}

	receipt, promoteErr := s.formalAuthority.PromoteFormalArtifact(ctx, companyops.HiveCosmFormalArtifactPromotionRequest{
		PromotionID:     promotionID,
		Lookup:          promotion.Lookup,
		WorkOrder:       promotion.WorkOrder,
		Employee:        promotion.Employee,
		IdentityBinding: promotion.IdentityBinding,
		Candidate: companyops.HiveCosmFormalArtifactCandidate{
			ID:               candidate.ID,
			Revision:         candidate.Revision,
			DurableObjectRef: candidate.DurableObjectRef,
			ContentDigest:    candidate.Digest,
			ApprovalEventID:  approvalEvent.ID,
		},
	})
	if promoteErr != nil {
		failedKey := "formal-promotion:" + promotionID + ":failed:after:" + strconv.Itoa(requested.Sequence)
		if _, appendErr := s.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
			Type:               companyops.ArtifactEventPromotionFailed,
			CandidateID:        candidate.ID,
			CandidateRevision:  candidate.Revision,
			CandidateDigest:    candidate.Digest,
			CandidateObjectRef: candidate.DurableObjectRef,
			IdempotencyKey:     failedKey,
		}); appendErr != nil {
			return CompanyOpsArtifactPromotionReceipt{}, appendErr
		}
		return CompanyOpsArtifactPromotionReceipt{}, promoteErr
	}

	if _, err := s.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
		Type:               companyops.ArtifactEventPromotionSucceeded,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		FormalArtifactRef:  receipt.Artifact.FormalArtifactRef,
		IdempotencyKey:     "formal-promotion:" + promotionID + ":succeeded",
	}); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}

	return s.runArtifactReadback(ctx, workspace, lineageID, promotionID, candidate, approvalEvent.ID, promotion.Lookup, receipt.Artifact.FormalArtifactRef, receipt.WritePerformed)
}

func (s *CompanyOpsArtifactService) ensureArtifactPromotionRequested(
	ctx context.Context,
	workspace string,
	lineageID string,
	promotionID string,
	candidate companyops.ArtifactCandidate,
	lastEvent companyops.ArtifactEvent,
	hasLast bool,
) (companyops.ArtifactEvent, error) {
	// Reuse the durable requested event only when it belongs to the exact same
	// promotion command (same candidate AND same anchored promotion id encoded
	// in its idempotency key). A requested event carrying a different id must
	// not be borrowed for a new command.
	if hasLast && lastEvent.Type == companyops.ArtifactEventPromotionRequested && lastEvent.CandidateID == candidate.ID {
		if anchoredID, ok := companyOpsPromotionIDFromEventKey(lastEvent.IdempotencyKey); ok && anchoredID == promotionID {
			return lastEvent, nil
		}
	}
	anchorSeq := 0
	if hasLast {
		anchorSeq = lastEvent.Sequence
	}
	return s.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
		Type:               companyops.ArtifactEventPromotionRequested,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		IdempotencyKey:     "formal-promotion:" + promotionID + ":requested:after:" + strconv.Itoa(anchorSeq),
	})
}

func (s *CompanyOpsArtifactService) runArtifactReadback(
	ctx context.Context,
	workspace string,
	lineageID string,
	promotionID string,
	candidate companyops.ArtifactCandidate,
	approvalEventID string,
	lookup companyops.HiveCosmAuthorityLookup,
	formalArtifactRef string,
	writePerformed bool,
) (CompanyOpsArtifactPromotionReceipt, error) {
	if formalArtifactRef == "" {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: formal artifact reference is missing before readback", ErrCompanyOpsArtifactConflict)
	}
	manifestID, ok := companyOpsFormalArtifactManifestID(formalArtifactRef, lookup.WorkOrderSourceRef)
	if !ok {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: formal artifact reference does not match the WorkOrder scope", ErrCompanyOpsArtifactConflict)
	}
	expectedCandidate := companyops.HiveCosmFormalArtifactCandidate{
		ID:               candidate.ID,
		Revision:         candidate.Revision,
		DurableObjectRef: candidate.DurableObjectRef,
		ContentDigest:    candidate.Digest,
		ApprovalEventID:  approvalEventID,
	}
	if _, err := s.formalAuthority.ReadFormalArtifact(ctx, lookup, expectedCandidate, manifestID); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	confirmed, err := s.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
		Type:               companyops.ArtifactEventAuthorityReadbackConfirmed,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		FormalArtifactRef:  formalArtifactRef,
		IdempotencyKey:     "formal-promotion:" + promotionID + ":readback",
	})
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	return CompanyOpsArtifactPromotionReceipt{
		PromotionID:       promotionID,
		CandidateID:       candidate.ID,
		LifecycleStatus:   confirmed.Type,
		FormalArtifactRef: formalArtifactRef,
		FormalVisible:     true,
		WritePerformed:    writePerformed,
		TerminalEvent:     confirmed,
	}, nil
}

func companyOpsArtifactPromotionAnchor(events []companyops.ArtifactEvent, candidateID string) (companyops.ArtifactEvent, companyops.ArtifactEvent, bool) {
	var approval companyops.ArtifactEvent
	var last companyops.ArtifactEvent
	hasLast := false
	for i := range events {
		event := events[i]
		if event.CandidateID != candidateID {
			continue
		}
		if event.Type == companyops.ArtifactEventApproved && approval.ID == "" {
			approval = event
		}
		last = event
		hasLast = true
	}
	return approval, last, hasLast
}

func companyOpsArtifactSucceededRef(events []companyops.ArtifactEvent, candidateID string) string {
	for i := range events {
		event := events[i]
		if event.CandidateID == candidateID && event.Type == companyops.ArtifactEventPromotionSucceeded {
			return event.FormalArtifactRef
		}
	}
	return ""
}

func companyOpsFormalArtifactManifestID(ref string, workOrderSourceRef string) (string, bool) {
	prefix := workOrderSourceRef + "/formal-artifact/"
	if !strings.HasPrefix(ref, prefix) {
		return "", false
	}
	return strings.TrimPrefix(ref, prefix), true
}

const (
	companyOpsArtifactPromotionKeyPrefix = "formal-promotion:"
	// companyOpsArtifactCanonicalUUIDLen is the length of a canonical lowercase
	// UUID with hyphens, the only form util.UUIDToString and the promotion_id
	// validation accept.
	companyOpsArtifactCanonicalUUIDLen = 36
)

// companyOpsArtifactIsPromotionPhaseEvent reports whether the event anchors a
// Formal Artifact promotion command. Only these event types embed the stable
// promotion id in their idempotency key; approved/submitted/changes_requested
// never do.
func companyOpsArtifactIsPromotionPhaseEvent(event companyops.ArtifactEvent) bool {
	switch event.Type {
	case companyops.ArtifactEventPromotionRequested,
		companyops.ArtifactEventPromotionFailed,
		companyops.ArtifactEventPromotionSucceeded,
		companyops.ArtifactEventAuthorityReadbackConfirmed:
		return true
	default:
		return false
	}
}

// companyOpsPromotionIDFromEventKey extracts the canonical promotion id from a
// promotion-phase idempotency key of the form
// "formal-promotion:<canonical-uuid>:<non-empty suffix>". Any deviation from
// the grammar fails closed (ok=false) so a malformed or tampered key can never
// be confused with an anchored command.
func companyOpsPromotionIDFromEventKey(key string) (string, bool) {
	promotionID, _, ok := companyOpsPromotionIDAndSuffixFromEventKey(key)
	return promotionID, ok
}

// companyOpsPromotionIDAndSuffixFromEventKey splits a promotion-phase
// idempotency key into its canonical promotion id and the remaining suffix.
// Returns ok=false when the key does not start with the prefix, the UUID is not
// canonical, or the suffix is empty.
func companyOpsPromotionIDAndSuffixFromEventKey(key string) (promotionID string, suffix string, ok bool) {
	if !strings.HasPrefix(key, companyOpsArtifactPromotionKeyPrefix) {
		return "", "", false
	}
	rest := key[len(companyOpsArtifactPromotionKeyPrefix):]
	if len(rest) < companyOpsArtifactCanonicalUUIDLen+1 {
		return "", "", false
	}
	promotionID = rest[:companyOpsArtifactCanonicalUUIDLen]
	parsed, err := util.ParseUUID(promotionID)
	if err != nil || util.UUIDToString(parsed) != promotionID {
		return "", "", false
	}
	if rest[companyOpsArtifactCanonicalUUIDLen] != ':' {
		return "", "", false
	}
	suffix = rest[companyOpsArtifactCanonicalUUIDLen+1:]
	if suffix == "" {
		return "", "", false
	}
	return promotionID, suffix, true
}

// companyOpsArtifactValidatePromotionSuffix checks the suffix of a
// promotion-phase idempotency key against the strict per-type grammar:
//
//   - promotion_requested  → requested:after:<non-negative decimal>
//   - promotion_failed     → failed:after:<non-negative decimal>
//   - promotion_succeeded  → succeeded
//   - authority_readback_confirmed → readback
//
// The decimal must be canonical (no leading zeros except "0" itself) so that
// two keys that differ only in encoding cannot coexist.
func companyOpsArtifactValidatePromotionSuffix(eventType companyops.ArtifactEventType, suffix string) bool {
	switch eventType {
	case companyops.ArtifactEventPromotionRequested:
		return companyOpsArtifactValidateAfterSuffix(suffix, "requested")
	case companyops.ArtifactEventPromotionFailed:
		return companyOpsArtifactValidateAfterSuffix(suffix, "failed")
	case companyops.ArtifactEventPromotionSucceeded:
		return suffix == "succeeded"
	case companyops.ArtifactEventAuthorityReadbackConfirmed:
		return suffix == "readback"
	default:
		return false
	}
}

func companyOpsArtifactValidateAfterSuffix(suffix, phase string) bool {
	prefix := phase + ":after:"
	if !strings.HasPrefix(suffix, prefix) {
		return false
	}
	decimal := suffix[len(prefix):]
	if decimal == "" {
		return false
	}
	for _, c := range decimal {
		if c < '0' || c > '9' {
			return false
		}
	}
	if len(decimal) > 1 && decimal[0] == '0' {
		return false
	}
	return true
}

// companyOpsResolveAnchoredPromotionID parses the durable promotion ledger for
// one candidate and returns the single stable promotion id that anchors every
// promotion-phase event. It fails closed (returns an ErrCompanyOpsArtifactConflict
// wrapping error) when any promotion-phase event's idempotency key is malformed,
// its suffix does not match the strict per-type grammar, or two promotion-phase
// events anchor different ids (a mixed-id ledger).
//
// Promotion-phase events belonging to a different candidate revision are
// skipped: each candidate owns an independent promotion lifecycle, and the
// durable promotion claim table enforces cross-candidate uniqueness at the
// database level. When the candidate has no promotion-phase events yet, the
// command is still establishing its id: anchored is "" and established is false.
func companyOpsResolveAnchoredPromotionID(
	events []companyops.ArtifactEvent,
	candidateID string,
) (anchored string, established bool, err error) {
	for i := range events {
		event := events[i]
		if !companyOpsArtifactIsPromotionPhaseEvent(event) {
			continue
		}
		if event.CandidateID != candidateID {
			continue
		}
		promotionID, suffix, ok := companyOpsPromotionIDAndSuffixFromEventKey(event.IdempotencyKey)
		if !ok {
			return "", false, fmt.Errorf("%w: promotion event has a malformed idempotency key", ErrCompanyOpsArtifactConflict)
		}
		if !companyOpsArtifactValidatePromotionSuffix(event.Type, suffix) {
			return "", false, fmt.Errorf("%w: promotion event has an invalid idempotency suffix", ErrCompanyOpsArtifactConflict)
		}
		if !established {
			anchored = promotionID
			established = true
			continue
		}
		if anchored != promotionID {
			return "", false, fmt.Errorf("%w: promotion ledger anchors multiple promotion ids", ErrCompanyOpsArtifactConflict)
		}
	}
	return anchored, established, nil
}

// companyOpsArtifactClaimPayload builds the full canonical claim payload from
// the promotion command, the current candidate, and the approval event id.
// Every field that participates in the HiveCosm Formal Artifact promotion is
// covered so any drift on replay produces a different digest and fails closed.
func companyOpsArtifactClaimPayload(
	promotion CompanyOpsArtifactPromotion,
	candidate companyops.ArtifactCandidate,
	approvalEventID string,
) companyops.PromotionClaimPayload {
	return companyops.PromotionClaimPayload{
		CommandSchemaVersion:   companyops.HiveCosmFormalArtifactPromotionCommandV1,
		ActorUserID:            util.UUIDToString(promotion.ActorUserID),
		LookupWorkOrderRef:     promotion.Lookup.WorkOrderSourceRef,
		LookupEmployeeID:       promotion.Lookup.EmployeeID,
		LookupBindingID:        promotion.Lookup.IdentityBindingID,
		LookupAgentID:          promotion.Lookup.AgentID,
		WorkOrderRef:           promotion.WorkOrder.SourceRef,
		WorkOrderRevision:      promotion.WorkOrder.Revision,
		WorkOrderContentDigest: promotion.WorkOrder.ContentDigest,
		EmployeeRef:            promotion.Employee.SourceRef,
		EmployeeRevision:       promotion.Employee.Revision,
		EmployeeContentDigest:  promotion.Employee.ContentDigest,
		BindingRef:             promotion.IdentityBinding.SourceRef,
		BindingRevision:        promotion.IdentityBinding.Revision,
		BindingContentDigest:   promotion.IdentityBinding.ContentDigest,
		CandidateRevision:      candidate.Revision,
		CandidateDigest:        candidate.Digest,
		CandidateObjectRef:     candidate.DurableObjectRef,
		CandidateContentType:   companyops.HiveCosmFormalArtifactContentTypeMarkdown,
		ApprovalEventID:        approvalEventID,
	}
}
