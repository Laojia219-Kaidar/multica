package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

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
	Target         companyops.ExecutionTargetSnapshot
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
	WorkspaceID             string
	IssueID                 string
	AssignmentCommandID     string
	AssignmentLineageID     string
	AssignmentInitialTaskID string
	LocalAgentID            string
	CandidateID             string
	PromotionID             string
	ActorUserID             pgtype.UUID
	Lookup                  companyops.HiveCosmAuthorityLookup
	WorkOrder               companyops.AuthoritySnapshot
	Employee                companyops.AuthoritySnapshot
	IdentityBinding         companyops.AuthoritySnapshot
	Agent                   companyops.AuthoritySnapshot
	SourceTaskID            string
	WriterLeaseTargetDigest string
	CompletionReceiptDigest string
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

// CompanyOpsWorkOrderTransitionExpectation is the immutable pair bridged by
// HiveCosm's Formal Artifact GET proof. PreviousAuthority is the observation
// sealed in HiveCrew's external_work_order_link; ResultingAuthority is the
// fresh WorkOrder observation resolved for this GET request.
type CompanyOpsWorkOrderTransitionExpectation struct {
	Lookup             companyops.HiveCosmAuthorityLookup
	PreviousAuthority  companyops.HiveCosmAuthorityTransitionSnapshot
	ResultingAuthority companyops.AuthoritySnapshot
	Employee           companyops.AuthoritySnapshot
	IdentityBinding    companyops.AuthoritySnapshot
	Agent              companyops.AuthoritySnapshot
	LocalAgentID       pgtype.UUID
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
		return nil, wrapArtifactLedgerRestoreConflict(err)
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

// wrapArtifactLedgerRestoreConflict exposes persisted ledger corruption as a
// service conflict while retaining the repository's exact cause for
// diagnostics and errors.Is callers.
func wrapArtifactLedgerRestoreConflict(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: restore persisted artifact ledger: %w", ErrCompanyOpsArtifactConflict, err)
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
		Target:         receipt.Target,
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
		return nil, wrapArtifactLedgerRestoreConflict(err)
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

// VerifyWorkOrderTransitionForGet permits a stale local projection link to be
// read without rewriting it only after a fresh Formal Artifact GET proves the
// exact authority transition for the same Issue assignment lineage. This seam
// is intentionally read-only and must never be reused by review, promotion, or
// assignment writes, which continue to require an exact local link.
func (s *CompanyOpsArtifactService) VerifyWorkOrderTransitionForGet(
	ctx context.Context,
	workspaceID pgtype.UUID,
	issueID pgtype.UUID,
	expectation CompanyOpsWorkOrderTransitionExpectation,
) error {
	if s == nil || s.repo == nil || s.formalAuthority == nil {
		return ErrCompanyOpsArtifactUnavailable
	}
	if expectation.Lookup.WorkOrderSourceRef == "" ||
		expectation.ResultingAuthority.SourceRef != expectation.Lookup.WorkOrderSourceRef ||
		expectation.PreviousAuthority.Revision == "" || expectation.PreviousAuthority.ContentDigest == "" ||
		expectation.ResultingAuthority.Revision == "" || expectation.ResultingAuthority.ContentDigest == "" {
		return fmt.Errorf("%w: WorkOrder transition expectation is incomplete", ErrCompanyOpsArtifactConflict)
	}
	outcome, err := s.GetIssueOutcome(ctx, workspaceID, issueID)
	if err != nil {
		return err
	}
	if outcome == nil || outcome.IssueID != issueID || outcome.Candidate == nil || outcome.Projection == nil {
		return fmt.Errorf("%w: the linked Issue has no formal outcome", ErrCompanyOpsArtifactConflict)
	}
	if !companyOpsTransitionExpectationMatchesDispatch(outcome, expectation) {
		return fmt.Errorf("%w: current Owner selectors do not match the formal outcome assignment receipt", ErrCompanyOpsArtifactConflict)
	}
	candidate := *outcome.Candidate
	projection := *outcome.Projection
	if projection.CandidateID != candidate.ID ||
		projection.Status != companyops.ArtifactEventAuthorityReadbackConfirmed ||
		!projection.FormalVisible || projection.FormalArtifactRef == "" {
		return fmt.Errorf("%w: the linked Issue has no authority_readback_confirmed formal outcome", ErrCompanyOpsArtifactConflict)
	}

	workspace := util.UUIDToString(workspaceID)
	lineageID := util.UUIDToString(outcome.CommandID)
	events, err := s.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		return err
	}
	approval, last, hasLast := companyOpsArtifactPromotionAnchor(events, candidate.ID)
	if approval.ID == "" || !hasLast || last.Type != companyops.ArtifactEventAuthorityReadbackConfirmed ||
		last.FormalArtifactRef != projection.FormalArtifactRef {
		return fmt.Errorf("%w: formal outcome projection and lineage ledger disagree", ErrCompanyOpsArtifactConflict)
	}
	promotionID, established, err := companyOpsResolveAnchoredPromotionID(events, candidate.ID)
	if err != nil {
		return err
	}
	if !established {
		return fmt.Errorf("%w: formal outcome has no anchored promotion", ErrCompanyOpsArtifactConflict)
	}
	manifestID, ok := companyOpsFormalArtifactManifestID(projection.FormalArtifactRef, expectation.Lookup.WorkOrderSourceRef)
	if !ok {
		return fmt.Errorf("%w: formal artifact reference does not match the WorkOrder scope", ErrCompanyOpsArtifactConflict)
	}
	expectedCandidate := companyops.HiveCosmFormalArtifactCandidate{
		ID:               candidate.ID,
		Revision:         candidate.Revision,
		DurableObjectRef: candidate.DurableObjectRef,
		ContentDigest:    candidate.Digest,
		ApprovalEventID:  approval.ID,
	}
	artifact, err := s.formalAuthority.ReadFormalArtifact(ctx, expectation.Lookup, expectedCandidate, manifestID)
	if err != nil {
		return err
	}
	expectedProof := companyops.HiveCosmWorkOrderTransitionProof{
		WorkOrderSourceRef: expectation.Lookup.WorkOrderSourceRef,
		PreviousAuthority:  expectation.PreviousAuthority,
		ResultingAuthority: companyops.HiveCosmAuthorityTransitionSnapshot{
			Revision:      expectation.ResultingAuthority.Revision,
			ContentDigest: expectation.ResultingAuthority.ContentDigest,
		},
		PromotionID:       promotionID,
		CandidateID:       candidate.ID,
		ApprovalEventID:   approval.ID,
		FormalArtifactRef: projection.FormalArtifactRef,
	}
	return verifyCompanyOpsWorkOrderTransitionArtifact(artifact, expectedProof, candidate, approval.ID)
}

func companyOpsTransitionExpectationMatchesDispatch(
	outcome *CompanyOpsArtifactOutcome,
	expectation CompanyOpsWorkOrderTransitionExpectation,
) bool {
	if outcome == nil || !expectation.LocalAgentID.Valid || outcome.LocalAgentID != expectation.LocalAgentID {
		return false
	}
	target := outcome.Target
	return target.WorkOrderRef == expectation.Lookup.WorkOrderSourceRef &&
		target.WorkOrderRevision == expectation.PreviousAuthority.Revision &&
		target.WorkOrderDigest == expectation.PreviousAuthority.ContentDigest &&
		target.EmployeeRef == expectation.Employee.SourceRef &&
		target.EmployeeRevision == expectation.Employee.Revision &&
		target.EmployeeDigest == expectation.Employee.ContentDigest &&
		target.BindingRef == expectation.IdentityBinding.SourceRef &&
		target.BindingRevision == expectation.IdentityBinding.Revision &&
		target.BindingDigest == expectation.IdentityBinding.ContentDigest &&
		// Agent execution configuration is intentionally mutable after a Run.
		// Historical formal readback binds the stable local Agent identity here;
		// the exact execution-time revision/digest remain frozen in target and
		// were required by the initial Promotion path before the authority POST.
		target.AgentRef == expectation.Agent.SourceRef
}

func verifyCompanyOpsWorkOrderTransitionArtifact(
	artifact companyops.HiveCosmFormalArtifact,
	expectedProof companyops.HiveCosmWorkOrderTransitionProof,
	candidate companyops.ArtifactCandidate,
	approvalEventID string,
) error {
	if artifact.FormalArtifactRef != expectedProof.FormalArtifactRef ||
		artifact.CandidateID != candidate.ID ||
		artifact.CandidateRevision != candidate.Revision ||
		artifact.CandidateDigest != candidate.Digest ||
		artifact.ApprovalEventID != approvalEventID ||
		artifact.WorkOrderTransition == nil || *artifact.WorkOrderTransition != expectedProof {
		return fmt.Errorf("%w: Formal Artifact GET did not prove the exact WorkOrder transition", ErrCompanyOpsArtifactConflict)
	}
	return nil
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
	member, memberErr := s.queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: review.ActorUserID, WorkspaceID: workspaceID})
	if memberErr != nil || member.Role != "owner" {
		return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("%w: Owner workspace role is required", ErrCompanyOpsArtifactConflict)
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
	lockedMember, lockedMemberErr := queries.LockOwnerMemberForArtifactPromotion(ctx, db.LockOwnerMemberForArtifactPromotionParams{
		UserID:      review.ActorUserID,
		WorkspaceID: workspaceID,
	})
	if lockedMemberErr != nil || lockedMember.Role != "owner" {
		return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("%w: Owner role changed or disappeared before review commit", ErrCompanyOpsArtifactConflict)
	}
	issue, err := queries.GetIssue(ctx, issueID)
	if err != nil || issue.WorkspaceID != workspaceID || issue.AssigneeID != outcome.LocalAgentID {
		return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("%w: Issue or assignee changed before review", ErrCompanyOpsArtifactConflict)
	}
	if review.Decision == companyops.ArtifactEventApproved {
		candidateUUID, candidateErr := util.ParseUUID(review.CandidateID)
		if candidateErr != nil {
			return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("%w: candidate id is invalid", ErrCompanyOpsArtifactConflict)
		}
		events, eventErr := queries.ListArtifactEventsByLineage(ctx, db.ListArtifactEventsByLineageParams{WorkspaceID: workspaceID, LineageID: outcome.CommandID})
		if eventErr != nil {
			return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("read existing Owner decisions: %w", eventErr)
		}
		for _, row := range events {
			if row.CandidateID == candidateUUID && row.EventType == string(companyops.ArtifactEventApproved) {
				return CompanyOpsArtifactReviewReceipt{}, fmt.Errorf("%w: active candidate already has an Owner approval", ErrCompanyOpsArtifactConflict)
			}
		}
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
			ActorUserID:        util.UUIDToString(review.ActorUserID),
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
	member, memberErr := s.queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: promotion.ActorUserID, WorkspaceID: workspaceID})
	if memberErr != nil || member.Role != "owner" {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: Owner workspace role is required", ErrCompanyOpsArtifactConflict)
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
	if !companyOpsPromotionMatchesDispatch(outcome, promotion) {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: promotion authority does not match the latest assignment receipt", ErrCompanyOpsArtifactConflict)
	}
	candidate := *outcome.Candidate
	projection := *outcome.Projection
	lineageID := util.UUIDToString(outcome.CommandID)
	if err := s.bindWriterLeasePromotionEvidence(ctx, workspaceID, outcome, &promotion); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}

	events, err := s.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	if err := companyOpsValidateActiveApproval(events, candidate.ID); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	approvalEvent, lastEvent, hasLast := companyOpsArtifactPromotionAnchor(events, candidate.ID)
	if approvalEvent.ID == "" {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: approved decision is unavailable for promotion", ErrCompanyOpsArtifactConflict)
	}
	if approvalEvent.ActorUserID == "" || approvalEvent.ActorUserID != util.UUIDToString(promotion.ActorUserID) || approvalEvent.CandidateID != candidate.ID || approvalEvent.CandidateRevision != candidate.Revision || approvalEvent.CandidateDigest != candidate.Digest || approvalEvent.CandidateObjectRef != candidate.DurableObjectRef {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: approved Owner event does not exactly bind the active candidate", ErrCompanyOpsArtifactConflict)
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
	promotion.WorkspaceID = workspace
	promotion.IssueID = util.UUIDToString(outcome.IssueID)
	promotion.AssignmentCommandID = lineageID
	promotion.AssignmentLineageID = lineageID
	promotion.AssignmentInitialTaskID = util.UUIDToString(outcome.InitialTaskID)
	promotion.LocalAgentID = util.UUIDToString(outcome.LocalAgentID)
	payload := companyOpsArtifactClaimPayload(promotion, candidate, approvalEvent)
	delivery, deliveryErr := s.repo.GetArtifactPromotionDelivery(ctx, workspace, effectivePromotionID)
	if deliveryErr != nil {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: durable promotion delivery is unavailable: %v", ErrCompanyOpsArtifactConflict, deliveryErr)
	}
	if delivery.PayloadDigest != payload.Digest() {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: durable promotion delivery payload drifted", ErrCompanyOpsArtifactConflict)
	}

	switch projection.Status {
	case companyops.ArtifactEventAuthorityReadbackConfirmed:
		if delivery.State != "readback_confirmed" {
			return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: confirmed lifecycle lacks a confirmed durable delivery", ErrCompanyOpsArtifactConflict)
		}
		responseReceipt, responseErr := decodeDurablePromotionResponse(delivery.ResponseReceipt, effectivePromotionID, candidate, approvalEvent.ID)
		if responseErr != nil {
			return CompanyOpsArtifactPromotionReceipt{}, responseErr
		}
		readbackReceipt, readbackErr := decodeDurablePromotionReadback(delivery.ReadbackReceipt, effectivePromotionID, candidate, approvalEvent.ID)
		if readbackErr != nil {
			return CompanyOpsArtifactPromotionReceipt{}, readbackErr
		}
		if responseReceipt.WritePerformed != readbackReceipt.WritePerformed || !durablePromotionArtifactsMatch(responseReceipt.Artifact, readbackReceipt.Artifact) {
			return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: durable response/readback receipts disagree", ErrCompanyOpsArtifactConflict)
		}
		if err := s.repo.VerifyPromotion(ctx, workspace, effectivePromotionID, candidate.ID, lineageID, payload); err != nil {
			return CompanyOpsArtifactPromotionReceipt{}, err
		}
		return CompanyOpsArtifactPromotionReceipt{
			PromotionID:       effectivePromotionID,
			CandidateID:       candidate.ID,
			LifecycleStatus:   projection.Status,
			FormalArtifactRef: projection.FormalArtifactRef,
			FormalVisible:     projection.FormalVisible,
			WritePerformed:    readbackReceipt.WritePerformed,
			TerminalEvent:     lastEvent,
		}, nil
	case companyops.ArtifactEventPromotionSucceeded:
		if delivery.State != "succeeded" && delivery.State != "readback_confirmed" {
			return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: promotion_succeeded lacks a durable response receipt", ErrCompanyOpsArtifactConflict)
		}
		responseReceipt, responseErr := decodeDurablePromotionResponse(delivery.ResponseReceipt, effectivePromotionID, candidate, approvalEvent.ID)
		if responseErr != nil {
			return CompanyOpsArtifactPromotionReceipt{}, responseErr
		}
		if err := s.repo.VerifyPromotion(ctx, workspace, effectivePromotionID, candidate.ID, lineageID, payload); err != nil {
			return CompanyOpsArtifactPromotionReceipt{}, err
		}
		succeededRef := companyOpsArtifactSucceededRef(events, candidate.ID)
		return s.finishArtifactPromotionReadback(ctx, delivery, workspace, lineageID, effectivePromotionID, candidate, approvalEvent.ID, approvalEvent.ActorUserID, promotion.Lookup, promotion.WorkOrder, succeededRef, responseReceipt.WritePerformed)
	case companyops.ArtifactEventApproved,
		companyops.ArtifactEventPromotionRequested,
		companyops.ArtifactEventPromotionFailed:
		return s.attemptArtifactPromotion(ctx, workspace, lineageID, effectivePromotionID, candidate, promotion, approvalEvent, lastEvent, hasLast)
	default:
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: artifact is not approved for promotion", ErrCompanyOpsArtifactConflict)
	}
}

func companyOpsPromotionMatchesDispatch(
	outcome *CompanyOpsArtifactOutcome,
	promotion CompanyOpsArtifactPromotion,
) bool {
	if outcome == nil {
		return false
	}
	localAgentID, err := util.ParseUUID(promotion.Lookup.AgentID)
	if err != nil || !localAgentID.Valid || outcome.LocalAgentID != localAgentID {
		return false
	}
	target := outcome.Target
	return target.WorkOrderRef == promotion.Lookup.WorkOrderSourceRef &&
		target.WorkOrderRef == promotion.WorkOrder.SourceRef &&
		target.WorkOrderRevision == promotion.WorkOrder.Revision &&
		target.WorkOrderDigest == promotion.WorkOrder.ContentDigest &&
		target.EmployeeRef == promotion.Employee.SourceRef &&
		target.EmployeeRevision == promotion.Employee.Revision &&
		target.EmployeeDigest == promotion.Employee.ContentDigest &&
		target.BindingRef == promotion.IdentityBinding.SourceRef &&
		target.BindingRevision == promotion.IdentityBinding.Revision &&
		target.BindingDigest == promotion.IdentityBinding.ContentDigest &&
		target.AgentRef == promotion.Agent.SourceRef &&
		target.AgentRevision == promotion.Agent.Revision &&
		target.AgentDigest == promotion.Agent.ContentDigest
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
	payload := companyOpsArtifactClaimPayload(promotion, candidate, approvalEvent)
	if err := s.repo.ClaimPromotion(ctx, workspace, promotionID, candidate.ID, lineageID, payload); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	request := companyops.HiveCosmFormalArtifactPromotionRequest{
		PromotionID:     promotionID,
		Lookup:          promotion.Lookup,
		WorkOrder:       promotion.WorkOrder,
		Employee:        promotion.Employee,
		IdentityBinding: promotion.IdentityBinding,
		Candidate:       companyops.HiveCosmFormalArtifactCandidate{ID: candidate.ID, Revision: candidate.Revision, DurableObjectRef: candidate.DurableObjectRef, ContentDigest: candidate.Digest, ApprovalEventID: approvalEvent.ID},
	}
	requestPayload, err := json.Marshal(request)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("marshal promotion request: %w", err)
	}
	delivery, err := s.repo.EnsureArtifactPromotionDelivery(ctx, workspace, promotionID, candidate.ID, lineageID, payload, requestPayload)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	// Re-read the lineage after the durable claim so a concurrent approval or
	// revocation cannot be used as a stale requested-event anchor.
	freshEvents, err := s.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	if err := companyOpsValidateActiveApproval(freshEvents, candidate.ID); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	_, freshLast, freshHasLast := companyOpsArtifactPromotionAnchor(freshEvents, candidate.ID)
	requested, err := s.ensureArtifactPromotionRequested(ctx, workspace, lineageID, promotionID, candidate, freshLast, freshHasLast)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}

	if delivery.State == "succeeded" {
		prior, err := decodeDurablePromotionResponse(delivery.ResponseReceipt, promotionID, candidate, approvalEvent.ID)
		if err != nil {
			return CompanyOpsArtifactPromotionReceipt{}, err
		}
		if err := s.ensureArtifactPromotionSucceeded(ctx, workspace, lineageID, promotionID, candidate, prior.Artifact.FormalArtifactRef); err != nil {
			return CompanyOpsArtifactPromotionReceipt{}, err
		}
		return s.finishArtifactPromotionReadback(ctx, delivery, workspace, lineageID, promotionID, candidate, approvalEvent.ID, approvalEvent.ActorUserID, promotion.Lookup, promotion.WorkOrder, prior.Artifact.FormalArtifactRef, prior.WritePerformed)
	}
	if delivery.State == "readback_confirmed" {
		prior, err := decodeDurablePromotionReadback(delivery.ReadbackReceipt, promotionID, candidate, approvalEvent.ID)
		if err != nil {
			return CompanyOpsArtifactPromotionReceipt{}, err
		}
		if err := s.ensureArtifactPromotionSucceeded(ctx, workspace, lineageID, promotionID, candidate, prior.Artifact.FormalArtifactRef); err != nil {
			return CompanyOpsArtifactPromotionReceipt{}, err
		}
		return s.runArtifactReadback(ctx, workspace, lineageID, promotionID, candidate, approvalEvent.ID, approvalEvent.ActorUserID, promotion.Lookup, promotion.WorkOrder, prior.Artifact.FormalArtifactRef, prior.WritePerformed)
	}
	if delivery.State == "dispatching" {
		return s.recoverArtifactPromotionFromExactReadback(ctx, delivery, workspace, lineageID, promotionID, candidate, approvalEvent.ID, promotion)
	}
	claimed, claimErr := s.repo.ClaimArtifactPromotionDelivery(ctx, workspace, promotionID, payload.Digest())
	if claimErr != nil {
		return CompanyOpsArtifactPromotionReceipt{}, claimErr
	}
	receipt, promoteErr := s.formalAuthority.PromoteFormalArtifact(ctx, request)
	if promoteErr != nil {
		definiteFailure := true
		var authorityErr *companyops.HiveCosmAuthorityError
		if errors.As(promoteErr, &authorityErr) {
			definiteFailure = (authorityErr.Kind == companyops.HiveCosmAuthorityInvalid && (authorityErr.StatusCode == 400 || authorityErr.StatusCode == 404)) ||
				(authorityErr.Kind == companyops.HiveCosmAuthorityNotFound && authorityErr.StatusCode == 404)
		}
		if definiteFailure {
			if markErr := s.repo.MarkArtifactPromotionDeliveryFailed(ctx, claimed, promoteErr.Error()); markErr != nil {
				return CompanyOpsArtifactPromotionReceipt{}, markErr
			}
		} else {
			// A conflict, source gap, unsupported response, or ambiguous invalid
			// response may have been committed remotely. Keep dispatching so the
			// recovery path performs an exact GET instead of issuing a duplicate POST.
			return CompanyOpsArtifactPromotionReceipt{}, promoteErr
		}
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
	responsePayload, err := encodeDurablePromotionResponse(receipt)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	if err := validatePromotionAuthorityArtifact(receipt.Artifact, candidate, promotionID, approvalEvent.ID, promotion.WorkOrder, false); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	if err := s.repo.MarkArtifactPromotionDeliverySucceeded(ctx, claimed, responsePayload); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	confirmedDelivery, deliveryErr := s.repo.GetArtifactPromotionDelivery(ctx, workspace, promotionID)
	if deliveryErr != nil {
		return CompanyOpsArtifactPromotionReceipt{}, deliveryErr
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

	return s.finishArtifactPromotionReadback(ctx, confirmedDelivery, workspace, lineageID, promotionID, candidate, approvalEvent.ID, approvalEvent.ActorUserID, promotion.Lookup, promotion.WorkOrder, receipt.Artifact.FormalArtifactRef, receipt.WritePerformed)
}

func (s *CompanyOpsArtifactService) recoverArtifactPromotionFromExactReadback(ctx context.Context, delivery db.ArtifactPromotionDelivery, workspace, lineageID, promotionID string, candidate companyops.ArtifactCandidate, approvalEventID string, promotion CompanyOpsArtifactPromotion) (CompanyOpsArtifactPromotionReceipt, error) {
	manifestID, err := companyops.FormalArtifactManifestID(promotionID)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	artifact, err := s.formalAuthority.ReadFormalArtifact(ctx, promotion.Lookup, companyops.HiveCosmFormalArtifactCandidate{ID: candidate.ID, Revision: candidate.Revision, DurableObjectRef: candidate.DurableObjectRef, ContentDigest: candidate.Digest, ApprovalEventID: approvalEventID}, manifestID)
	if err != nil {
		var authorityErr *companyops.HiveCosmAuthorityError
		if errors.As(err, &authorityErr) && authorityErr.Kind == companyops.HiveCosmAuthorityNotFound && delivery.LeaseUntil.Valid && time.Now().After(delivery.LeaseUntil.Time) {
			if markErr := s.repo.MarkArtifactPromotionDeliveryDefiniteAbsent(ctx, delivery, "authority formal artifact absent after dispatch lease expiry"); markErr != nil {
				return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: mark definite authority absence: %v", ErrCompanyOpsArtifactConflict, markErr)
			}
		}
		// Ambiguous/404/409/503 all remain dispatching. No POST is safe without
		// an exact positive readback or an explicit pending/definite-failure CAS.
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	if err := validatePromotionAuthorityArtifact(artifact, candidate, promotionID, approvalEventID, promotion.WorkOrder, true); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	response, responseErr := encodeDurablePromotionResponse(companyops.HiveCosmFormalArtifactPromotionReceipt{PromotionID: promotionID, WritePerformed: false, Artifact: artifact})
	if responseErr != nil {
		return CompanyOpsArtifactPromotionReceipt{}, responseErr
	}
	readback, readbackErr := encodeDurablePromotionReadback(CompanyOpsArtifactPromotionReceipt{PromotionID: promotionID, CandidateID: candidate.ID, WritePerformed: false}, artifact)
	if readbackErr != nil {
		return CompanyOpsArtifactPromotionReceipt{}, readbackErr
	}
	if err := s.repo.RecoverArtifactPromotionDeliveryFromReadback(ctx, delivery, response, readback); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, companyops.ErrArtifactPromotionInProgress
	}
	if _, err := s.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{Type: companyops.ArtifactEventPromotionSucceeded, CandidateID: candidate.ID, CandidateRevision: candidate.Revision, CandidateDigest: candidate.Digest, CandidateObjectRef: candidate.DurableObjectRef, FormalArtifactRef: artifact.FormalArtifactRef, IdempotencyKey: "formal-promotion:" + promotionID + ":succeeded"}); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	confirmed, err := s.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{Type: companyops.ArtifactEventAuthorityReadbackConfirmed, CandidateID: candidate.ID, CandidateRevision: candidate.Revision, CandidateDigest: candidate.Digest, CandidateObjectRef: candidate.DurableObjectRef, FormalArtifactRef: artifact.FormalArtifactRef, IdempotencyKey: "formal-promotion:" + promotionID + ":readback"})
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	return CompanyOpsArtifactPromotionReceipt{PromotionID: promotionID, CandidateID: candidate.ID, LifecycleStatus: confirmed.Type, FormalArtifactRef: artifact.FormalArtifactRef, FormalVisible: true, TerminalEvent: confirmed}, nil
}

func (s *CompanyOpsArtifactService) ensureArtifactPromotionSucceeded(ctx context.Context, workspace, lineageID, promotionID string, candidate companyops.ArtifactCandidate, formalRef string) error {
	_, err := s.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
		Type:               companyops.ArtifactEventPromotionSucceeded,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		FormalArtifactRef:  formalRef,
		IdempotencyKey:     "formal-promotion:" + promotionID + ":succeeded",
	})
	return err
}

func (s *CompanyOpsArtifactService) finishArtifactPromotionReadback(ctx context.Context, delivery db.ArtifactPromotionDelivery, workspace, lineageID, promotionID string, candidate companyops.ArtifactCandidate, approvalEventID, approvalActorUserID string, lookup companyops.HiveCosmAuthorityLookup, expectedWorkOrder companyops.AuthoritySnapshot, ref string, writePerformed bool) (CompanyOpsArtifactPromotionReceipt, error) {
	receipt, artifact, err := s.runArtifactReadbackWithArtifact(ctx, workspace, lineageID, promotionID, candidate, approvalEventID, approvalActorUserID, lookup, expectedWorkOrder, ref, writePerformed)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	if len(delivery.ResponseReceipt) == 0 {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: durable promotion response is missing before readback confirmation", ErrCompanyOpsArtifactConflict)
	}
	response, responseErr := decodeDurablePromotionResponse(delivery.ResponseReceipt, promotionID, candidate, approvalEventID)
	if responseErr != nil {
		return CompanyOpsArtifactPromotionReceipt{}, responseErr
	}
	if response.WritePerformed != receipt.WritePerformed || !durablePromotionArtifactsMatch(response.Artifact, artifact) {
		return CompanyOpsArtifactPromotionReceipt{}, fmt.Errorf("%w: durable response and Authority readback disagree", ErrCompanyOpsArtifactConflict)
	}
	readback, marshalErr := encodeDurablePromotionReadback(receipt, artifact)
	if marshalErr != nil {
		return CompanyOpsArtifactPromotionReceipt{}, marshalErr
	}
	if err := s.repo.MarkArtifactPromotionDeliveryReadbackConfirmed(ctx, delivery, readback); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, err
	}
	return receipt, nil
}

// Durable promotion receipts are local evidence records, not a second
// Authority wire contract. They use explicit keys and strict decoding so a
// replay cannot silently discard write_performed, candidate provenance, or the
// GET transition proof.
type durablePromotionReceipt struct {
	PromotionID    string                            `json:"PromotionID"`
	WritePerformed bool                              `json:"WritePerformed"`
	Artifact       companyops.HiveCosmFormalArtifact `json:"Artifact"`
}

// durablePromotionReceiptWire keeps presence separate from the business
// values. In particular, a missing WritePerformed must not silently become
// false during replay; PromotionID and Artifact are equally mandatory.
type durablePromotionReceiptWire struct {
	PromotionID    *string                            `json:"PromotionID"`
	WritePerformed *bool                              `json:"WritePerformed"`
	Artifact       *companyops.HiveCosmFormalArtifact `json:"Artifact"`
}

func encodeDurablePromotionResponse(receipt companyops.HiveCosmFormalArtifactPromotionReceipt) ([]byte, error) {
	return json.Marshal(durablePromotionReceipt{PromotionID: receipt.PromotionID, WritePerformed: receipt.WritePerformed, Artifact: receipt.Artifact})
}

func decodeDurablePromotionResponse(raw []byte, promotionID string, candidate companyops.ArtifactCandidate, approvalEventID string) (durablePromotionReceipt, error) {
	receipt, err := decodeDurablePromotionReceipt(raw)
	if err != nil {
		return durablePromotionReceipt{}, fmt.Errorf("%w: durable promotion response is invalid: %v", ErrCompanyOpsArtifactConflict, err)
	}
	if err := validateDurablePromotionArtifact(receipt.PromotionID, receipt.Artifact, promotionID, candidate, approvalEventID, false); err != nil {
		return durablePromotionReceipt{}, err
	}
	return receipt, nil
}

func encodeDurablePromotionReadback(receipt CompanyOpsArtifactPromotionReceipt, artifact companyops.HiveCosmFormalArtifact) ([]byte, error) {
	return json.Marshal(durablePromotionReceipt{PromotionID: receipt.PromotionID, WritePerformed: receipt.WritePerformed, Artifact: artifact})
}

func decodeDurablePromotionReadback(raw []byte, promotionID string, candidate companyops.ArtifactCandidate, approvalEventID string) (durablePromotionReceipt, error) {
	receipt, err := decodeDurablePromotionReceipt(raw)
	if err != nil {
		return durablePromotionReceipt{}, fmt.Errorf("%w: durable readback receipt is invalid: %v", ErrCompanyOpsArtifactConflict, err)
	}
	if err := validateDurablePromotionArtifact(receipt.PromotionID, receipt.Artifact, promotionID, candidate, approvalEventID, true); err != nil {
		return durablePromotionReceipt{}, err
	}
	return receipt, nil
}

func decodeDurablePromotionReceipt(raw []byte) (durablePromotionReceipt, error) {
	var wire durablePromotionReceiptWire
	if err := decodeStrictDurableJSON(raw, &wire); err != nil {
		return durablePromotionReceipt{}, err
	}
	if wire.PromotionID == nil || wire.WritePerformed == nil || wire.Artifact == nil {
		return durablePromotionReceipt{}, errors.New("PromotionID, WritePerformed, and Artifact are required")
	}
	return durablePromotionReceipt{
		PromotionID:    *wire.PromotionID,
		WritePerformed: *wire.WritePerformed,
		Artifact:       *wire.Artifact,
	}, nil
}

func decodeStrictDurableJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func validateDurablePromotionArtifact(receiptPromotionID string, artifact companyops.HiveCosmFormalArtifact, promotionID string, candidate companyops.ArtifactCandidate, approvalEventID string, requireTransition bool) error {
	manifestID, manifestErr := companyops.FormalArtifactManifestID(promotionID)
	if manifestErr != nil {
		return fmt.Errorf("%w: durable promotion manifest id is invalid: %v", ErrCompanyOpsArtifactConflict, manifestErr)
	}
	if receiptPromotionID != promotionID || artifact.FormalArtifactRef == "" ||
		artifact.ArtifactManifestID != manifestID || !strings.HasSuffix(artifact.FormalArtifactRef, "/formal-artifact/"+manifestID) ||
		artifact.CandidateID != candidate.ID || artifact.CandidateRevision != candidate.Revision ||
		artifact.CandidateDigest != candidate.Digest || artifact.ContentRef != candidate.DurableObjectRef ||
		artifact.ApprovalEventID != approvalEventID || artifact.ReviewerID == "" {
		return fmt.Errorf("%w: durable promotion receipt provenance changed", ErrCompanyOpsArtifactConflict)
	}
	if requireTransition && artifact.WorkOrderTransition == nil {
		return fmt.Errorf("%w: durable readback receipt is missing the WorkOrder transition", ErrCompanyOpsArtifactConflict)
	}
	if artifact.WorkOrderTransition != nil && (artifact.WorkOrderTransition.PromotionID != promotionID || artifact.WorkOrderTransition.CandidateID != candidate.ID || artifact.WorkOrderTransition.ApprovalEventID != approvalEventID || artifact.WorkOrderTransition.FormalArtifactRef != artifact.FormalArtifactRef) {
		return fmt.Errorf("%w: durable transition provenance changed", ErrCompanyOpsArtifactConflict)
	}
	return nil
}

func validatePromotionAuthorityArtifact(artifact companyops.HiveCosmFormalArtifact, candidate companyops.ArtifactCandidate, promotionID, approvalEventID string, expectedWorkOrder companyops.AuthoritySnapshot, requireTransition bool) error {
	manifestID, err := companyops.FormalArtifactManifestID(promotionID)
	if err != nil {
		return fmt.Errorf("%w: promotion manifest id is invalid: %v", ErrCompanyOpsArtifactConflict, err)
	}
	expectedFormalRef := expectedWorkOrder.SourceRef + "/formal-artifact/" + manifestID
	if expectedWorkOrder.SourceRef == "" || artifact.FormalArtifactRef != expectedFormalRef || artifact.ArtifactManifestID != manifestID || artifact.ReviewerID == "" ||
		artifact.CandidateID != candidate.ID || artifact.CandidateRevision != candidate.Revision || artifact.CandidateDigest != candidate.Digest || artifact.ContentRef != candidate.DurableObjectRef || artifact.ApprovalEventID != approvalEventID {
		return fmt.Errorf("%w: Authority artifact provenance is incomplete or changed", ErrCompanyOpsArtifactConflict)
	}
	if requireTransition && artifact.WorkOrderTransition == nil {
		return fmt.Errorf("%w: Authority readback transition is missing", ErrCompanyOpsArtifactConflict)
	}
	if artifact.WorkOrderTransition != nil {
		transition := artifact.WorkOrderTransition
		if transition.PromotionID != promotionID || transition.CandidateID != candidate.ID || transition.ApprovalEventID != approvalEventID || transition.FormalArtifactRef != artifact.FormalArtifactRef || transition.WorkOrderSourceRef == "" || transition.ResultingAuthority.Revision == "" || transition.ResultingAuthority.ContentDigest == "" {
			return fmt.Errorf("%w: Authority transition provenance is incomplete or changed", ErrCompanyOpsArtifactConflict)
		}
		if expectedWorkOrder.Revision != "" && (transition.PreviousAuthority.Revision != expectedWorkOrder.Revision || transition.PreviousAuthority.ContentDigest != expectedWorkOrder.ContentDigest) {
			return fmt.Errorf("%w: Authority transition previous WorkOrder does not match the assignment snapshot", ErrCompanyOpsArtifactConflict)
		}
	}
	return nil
}

func durablePromotionArtifactsMatch(left, right companyops.HiveCosmFormalArtifact) bool {
	return left.FormalArtifactRef == right.FormalArtifactRef &&
		left.Revision == right.Revision && left.ContentDigest == right.ContentDigest &&
		left.ProjectID == right.ProjectID && left.WorkOrderID == right.WorkOrderID &&
		left.AssignmentID == right.AssignmentID && left.EmployeeID == right.EmployeeID &&
		left.AgentID == right.AgentID && left.IdentityBindingID == right.IdentityBindingID &&
		left.ArtifactManifestID == right.ArtifactManifestID && left.ContentObjectID == right.ContentObjectID &&
		left.ContentRef == right.ContentRef && left.CandidateID == right.CandidateID &&
		left.CandidateRevision == right.CandidateRevision && left.CandidateDigest == right.CandidateDigest &&
		left.ReviewDecisionID == right.ReviewDecisionID && left.ReviewerID == right.ReviewerID &&
		left.ApprovalEventID == right.ApprovalEventID
}

func (s *CompanyOpsArtifactService) bindWriterLeasePromotionEvidence(ctx context.Context, workspaceID pgtype.UUID, outcome *CompanyOpsArtifactOutcome, promotion *CompanyOpsArtifactPromotion) error {
	if outcome == nil || promotion == nil || !outcome.CurrentTaskID.Valid {
		return fmt.Errorf("%w: source task is required for C3b2 promotion", ErrCompanyOpsArtifactConflict)
	}
	promotion.SourceTaskID = util.UUIDToString(outcome.CurrentTaskID)
	task, err := s.queries.GetAgentTask(ctx, outcome.CurrentTaskID)
	if err != nil {
		return err
	}
	runtime, err := s.queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return err
	}
	claim, legacy, err := DecodePersistedWriterLeaseClaim(task, runtime.WorkspaceID.String())
	if err != nil {
		return err
	}
	if legacy || claim.Mode != WriterLeaseModeEnforce {
		return fmt.Errorf("%w: C3b2 promotion requires a migration-406 enforced writer-lease claim", ErrCompanyOpsArtifactConflict)
	}
	promotion.WriterLeaseTargetDigest = claim.Digest
	receipt, err := s.queries.GetWriterLeaseCompletionReceipt(ctx, db.GetWriterLeaseCompletionReceiptParams{WorkspaceID: workspaceID, TaskID: task.ID})
	if err != nil {
		return fmt.Errorf("%w: load completion receipt for promotion: %v", ErrCompanyOpsArtifactConflict, err)
	}
	if receipt.WorkspaceID != workspaceID || receipt.TaskID != task.ID || receipt.TargetDigest != claim.Digest {
		return fmt.Errorf("%w: completion receipt binding drift", ErrCompanyOpsArtifactConflict)
	}
	if err := verifyWriterLeaseCompletionReceipt(task.ID.String(), claim.Digest, receipt); err != nil {
		return fmt.Errorf("%w: %v", ErrCompanyOpsArtifactConflict, err)
	}
	promotion.CompletionReceiptDigest = receipt.ReceiptDigest
	return nil
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
	approvalActorUserID string,
	lookup companyops.HiveCosmAuthorityLookup,
	expectedWorkOrder companyops.AuthoritySnapshot,
	formalArtifactRef string,
	writePerformed bool,
) (CompanyOpsArtifactPromotionReceipt, error) {
	receipt, _, err := s.runArtifactReadbackWithArtifact(ctx, workspace, lineageID, promotionID, candidate, approvalEventID, approvalActorUserID, lookup, expectedWorkOrder, formalArtifactRef, writePerformed)
	return receipt, err
}

func (s *CompanyOpsArtifactService) runArtifactReadbackWithArtifact(
	ctx context.Context,
	workspace string,
	lineageID string,
	promotionID string,
	candidate companyops.ArtifactCandidate,
	approvalEventID string,
	approvalActorUserID string,
	lookup companyops.HiveCosmAuthorityLookup,
	expectedWorkOrder companyops.AuthoritySnapshot,
	formalArtifactRef string,
	writePerformed bool,
) (CompanyOpsArtifactPromotionReceipt, companyops.HiveCosmFormalArtifact, error) {
	if formalArtifactRef == "" {
		return CompanyOpsArtifactPromotionReceipt{}, companyops.HiveCosmFormalArtifact{}, fmt.Errorf("%w: formal artifact reference is missing before readback", ErrCompanyOpsArtifactConflict)
	}
	manifestID, ok := companyOpsFormalArtifactManifestID(formalArtifactRef, lookup.WorkOrderSourceRef)
	if !ok {
		return CompanyOpsArtifactPromotionReceipt{}, companyops.HiveCosmFormalArtifact{}, fmt.Errorf("%w: formal artifact reference does not match the WorkOrder scope", ErrCompanyOpsArtifactConflict)
	}
	expectedCandidate := companyops.HiveCosmFormalArtifactCandidate{
		ID:               candidate.ID,
		Revision:         candidate.Revision,
		DurableObjectRef: candidate.DurableObjectRef,
		ContentDigest:    candidate.Digest,
		ApprovalEventID:  approvalEventID,
	}
	artifact, err := s.formalAuthority.ReadFormalArtifact(ctx, lookup, expectedCandidate, manifestID)
	if err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, companyops.HiveCosmFormalArtifact{}, err
	}
	if err := validatePromotionAuthorityArtifact(artifact, candidate, promotionID, approvalEventID, expectedWorkOrder, true); err != nil {
		return CompanyOpsArtifactPromotionReceipt{}, companyops.HiveCosmFormalArtifact{}, err
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
		return CompanyOpsArtifactPromotionReceipt{}, companyops.HiveCosmFormalArtifact{}, err
	}
	return CompanyOpsArtifactPromotionReceipt{
		PromotionID:       promotionID,
		CandidateID:       candidate.ID,
		LifecycleStatus:   confirmed.Type,
		FormalArtifactRef: formalArtifactRef,
		FormalVisible:     true,
		WritePerformed:    writePerformed,
		TerminalEvent:     confirmed,
	}, artifact, nil
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

// companyOpsValidateActiveApproval is deliberately stricter than the
// lifecycle transition validator. A promotion may use exactly one approved
// event for the active candidate; any later review decision or a second
// approval supersedes that decision and fails closed. Promotion-phase events
// are allowed only after this immutable approval anchor.
func companyOpsValidateActiveApproval(events []companyops.ArtifactEvent, candidateID string) error {
	approvalCount := 0
	approvalIndex := -1
	for i := range events {
		if events[i].CandidateID != candidateID {
			continue
		}
		switch events[i].Type {
		case companyops.ArtifactEventApproved:
			approvalCount++
			if approvalIndex < 0 {
				approvalIndex = i
			}
		}
	}
	if approvalCount != 1 || approvalIndex < 0 {
		return fmt.Errorf("%w: active candidate must have exactly one Owner approval", ErrCompanyOpsArtifactConflict)
	}
	for _, event := range events[approvalIndex+1:] {
		if event.CandidateID != candidateID {
			continue
		}
		switch event.Type {
		case companyops.ArtifactEventChangesRequested,
			companyops.ArtifactEventRejected,
			companyops.ArtifactEventApprovalRevoked,
			companyops.ArtifactEventApproved:
			return fmt.Errorf("%w: Owner approval was superseded before promotion", ErrCompanyOpsArtifactConflict)
		}
	}
	return nil
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
	approvalEvent companyops.ArtifactEvent,
) companyops.PromotionClaimPayload {
	return companyops.PromotionClaimPayload{
		WorkspaceID:             promotion.WorkspaceID,
		PromotionID:             promotion.PromotionID,
		IssueID:                 promotion.IssueID,
		AssignmentCommandID:     promotion.AssignmentCommandID,
		AssignmentLineageID:     promotion.AssignmentLineageID,
		AssignmentInitialTaskID: promotion.AssignmentInitialTaskID,
		LocalAgentID:            promotion.LocalAgentID,
		CommandSchemaVersion:    companyops.HiveCosmFormalArtifactPromotionCommandV1,
		ActorUserID:             util.UUIDToString(promotion.ActorUserID),
		LookupWorkOrderRef:      promotion.Lookup.WorkOrderSourceRef,
		LookupEmployeeID:        promotion.Lookup.EmployeeID,
		LookupBindingID:         promotion.Lookup.IdentityBindingID,
		LookupAgentID:           promotion.Lookup.AgentID,
		WorkOrderRef:            promotion.WorkOrder.SourceRef,
		WorkOrderRevision:       promotion.WorkOrder.Revision,
		WorkOrderContentDigest:  promotion.WorkOrder.ContentDigest,
		EmployeeRef:             promotion.Employee.SourceRef,
		EmployeeRevision:        promotion.Employee.Revision,
		EmployeeContentDigest:   promotion.Employee.ContentDigest,
		AgentRef:                promotion.Agent.SourceRef,
		AgentRevision:           promotion.Agent.Revision,
		AgentContentDigest:      promotion.Agent.ContentDigest,
		BindingRef:              promotion.IdentityBinding.SourceRef,
		BindingRevision:         promotion.IdentityBinding.Revision,
		BindingContentDigest:    promotion.IdentityBinding.ContentDigest,
		CandidateRevision:       candidate.Revision,
		CandidateID:             candidate.ID,
		CandidateDigest:         candidate.Digest,
		CandidateObjectRef:      candidate.DurableObjectRef,
		CandidateContentType:    companyops.HiveCosmFormalArtifactContentTypeMarkdown,
		ApprovalActorUserID:     approvalEvent.ActorUserID,
		ApprovalEventID:         approvalEvent.ID,
		ApprovalEventSequence:   approvalEvent.Sequence,
		ApprovalEventType:       string(approvalEvent.Type),
		ApprovalEventDigest:     companyops.ArtifactEventDigest(approvalEvent),
		SourceTaskID:            promotion.SourceTaskID,
		WriterLeaseTargetDigest: promotion.WriterLeaseTargetDigest,
		CompletionReceiptDigest: promotion.CompletionReceiptDigest,
	}
}
