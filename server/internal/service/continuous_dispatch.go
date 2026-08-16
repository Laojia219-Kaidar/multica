package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrContinuousDispatchConflict           = errors.New("continuous dispatch request conflicts with committed generation")
	ErrContinuousDispatchIssueDrift         = errors.New("continuous dispatch issue identity drifted")
	ErrContinuousDispatchRouteDrift         = errors.New("continuous dispatch execution route drifted")
	ErrContinuousDispatchIssueNotReady      = errors.New("continuous dispatch issue is not ready")
	ErrContinuousDispatchReviewLineageDrift = errors.New("continuous dispatch review source lineage drifted")
)

const (
	continuousDispatchEmployeeRefPrefix       = "hivecosm://employees/"
	continuousDispatchReviewCommentRefPrefix  = "hivecrew://comments/"
	continuousDispatchReviewInitiatorSourceV1 = "owner_admin_review_dispatch/v1"
)

// ContinuousDispatchRoute is the exact employee/Agent/runtime/model/account
// decision already produced from authoritative shadow inputs. It is execution
// evidence only and never becomes a second employee or Runtime registry.
type ContinuousDispatchRoute struct {
	EmployeeRef  string
	LocalAgentID pgtype.UUID
	RuntimeID    pgtype.UUID
	Model        string
	AccountRef   string
}

// ContinuousDispatchReviewProvenance is server-built evidence for a review
// Task. SourceRef identifies the exact Comment that ties the candidate to its
// completed implementation Task; it is persisted in the existing Task context
// and re-exposed on the dispatch receipt without creating another registry.
// Public requests cannot populate this structure.
type ContinuousDispatchReviewProvenance struct {
	SourceRef       string `json:"source_ref"`
	SourceIssueID   string `json:"source_issue_id"`
	SourceTaskID    string `json:"source_task_id"`
	InitiatorSource string `json:"initiator_source"`
}

// ContinuousDispatchRequest is a server-built command for one exact shadow
// generation. Public handlers must recompute the selected route immediately
// before calling Dispatch; browsers do not choose identity or capacity truth.
type ContinuousDispatchRequest struct {
	Identity    continuousdispatch.DispatchIdentity
	Route       ContinuousDispatchRoute
	ActorUserID pgtype.UUID
	HandoffNote string
	// requireInReview is an internal review-drain precondition. It is built by
	// the server-only review trigger, never decoded from HTTP, and is checked
	// again inside the same transaction that creates the Task+receipt.
	requireInReview bool
	// reviewProvenance is present only on the internal review-dispatch path.
	// It never crosses the browser request boundary.
	reviewProvenance *ContinuousDispatchReviewProvenance
}

// ContinuousDispatchTx is the transaction-local seam. Implementations must
// not publish events or wake runtimes before the surrounding transaction has
// committed successfully.
type ContinuousDispatchTx interface {
	LockIdentity(context.Context, continuousdispatch.DispatchIdentity) error
	GetReceipt(context.Context, continuousdispatch.DispatchIdentity) (ContinuousDispatchReceipt, bool, error)
	LoadIssue(context.Context, continuousdispatch.DispatchIdentity) (db.Issue, error)
	LoadTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error)
	VerifyReviewSource(context.Context, ContinuousDispatchRequest) error
	PrepareTask(context.Context, db.Issue, ContinuousDispatchRequest) (db.AgentTaskQueue, error)
	StampTaskIdentity(context.Context, db.AgentTaskQueue, continuousdispatch.DispatchIdentity, *ContinuousDispatchReviewProvenance) (db.AgentTaskQueue, error)
	AppendReceipt(context.Context, ContinuousDispatchReceipt) (ContinuousDispatchReceipt, error)
}

type ContinuousDispatchBackend interface {
	RunInContinuousDispatchTx(context.Context, func(ContinuousDispatchTx) error) error
	NotifyContinuousDispatchTask(context.Context, db.AgentTaskQueue)
}

// ContinuousDispatchService atomically creates one Task and one immutable
// receipt for an exact five-field generation. Exact replays return the stored
// receipt without creating or notifying another Task.
type ContinuousDispatchService struct {
	backend ContinuousDispatchBackend
}

func NewContinuousDispatchService(backend ContinuousDispatchBackend) *ContinuousDispatchService {
	return &ContinuousDispatchService{backend: backend}
}

func (s *ContinuousDispatchService) Dispatch(
	ctx context.Context,
	req ContinuousDispatchRequest,
) (ContinuousDispatchReceipt, error) {
	if s == nil || s.backend == nil {
		return ContinuousDispatchReceipt{}, fmt.Errorf("continuous dispatch backend is required")
	}
	if err := validateContinuousDispatchRequest(req); err != nil {
		return ContinuousDispatchReceipt{}, err
	}

	digest, err := continuousDispatchRequestDigest(req)
	if err != nil {
		return ContinuousDispatchReceipt{}, fmt.Errorf("digest continuous dispatch request: %w", err)
	}

	var (
		receipt ContinuousDispatchReceipt
		task    db.AgentTaskQueue
		created bool
	)
	err = s.backend.RunInContinuousDispatchTx(ctx, func(tx ContinuousDispatchTx) error {
		if tx == nil {
			return fmt.Errorf("continuous dispatch transaction is required")
		}
		if err := tx.LockIdentity(ctx, req.Identity); err != nil {
			return fmt.Errorf("lock continuous dispatch identity: %w", err)
		}

		existing, found, err := tx.GetReceipt(ctx, req.Identity)
		if err != nil {
			return fmt.Errorf("get continuous dispatch receipt: %w", err)
		}
		if found {
			if req.reviewProvenance != nil {
				storedTask, loadErr := tx.LoadTask(ctx, existing.TaskID)
				if loadErr != nil {
					return ErrContinuousDispatchConflict
				}
				sourceTaskID := parseDispatchUUID(req.reviewProvenance.SourceTaskID)
				if storedTask.TaskKind != TaskKindReview || storedTask.ReviewTargetTaskID != sourceTaskID {
					return ErrContinuousDispatchConflict
				}
				storedProvenance, provenanceErr := continuousDispatchReviewProvenanceFromTaskContext(storedTask.Context, req.Identity)
				if provenanceErr != nil {
					return ErrContinuousDispatchConflict
				}
				existing.ReviewProvenance = &storedProvenance
			}
			if !continuousDispatchReceiptMatchesRequest(existing, req, digest) {
				return ErrContinuousDispatchConflict
			}
			receipt = existing
			return nil
		}

		issue, err := tx.LoadIssue(ctx, req.Identity)
		if err != nil {
			return fmt.Errorf("load continuous dispatch issue: %w", err)
		}
		if err := validateContinuousDispatchIssue(issue, req.Identity, req.requireInReview); err != nil {
			return err
		}
		if req.reviewProvenance != nil {
			if err := tx.VerifyReviewSource(ctx, req); err != nil {
				return err
			}
		}

		task, err = tx.PrepareTask(ctx, issue, req)
		if err != nil {
			return fmt.Errorf("prepare continuous dispatch task: %w", err)
		}
		if !task.ID.Valid || task.ID.Bytes == ([16]byte{}) || task.IssueID != issue.ID ||
			task.AgentID != req.Route.LocalAgentID || task.RuntimeID != req.Route.RuntimeID || task.Status != "queued" {
			return ErrContinuousDispatchRouteDrift
		}

		task, err = tx.StampTaskIdentity(ctx, task, req.Identity, req.reviewProvenance)
		if err != nil {
			return fmt.Errorf("stamp continuous dispatch task identity: %w", err)
		}
		if req.reviewProvenance != nil {
			sourceTaskID := parseDispatchUUID(req.reviewProvenance.SourceTaskID)
			if task.TaskKind != TaskKindReview || task.ReviewTargetTaskID != sourceTaskID {
				return ErrContinuousDispatchRouteDrift
			}
		}

		receipt = ContinuousDispatchReceipt{
			Identity: req.Identity, TaskID: task.ID, EmployeeRef: req.Route.EmployeeRef,
			LocalAgentID: req.Route.LocalAgentID, RuntimeID: req.Route.RuntimeID,
			Model: req.Route.Model, AccountRef: req.Route.AccountRef, RequestDigest: digest,
			ReviewProvenance: cloneContinuousDispatchReviewProvenance(req.reviewProvenance),
		}
		stored, err := tx.AppendReceipt(ctx, receipt)
		if err != nil {
			return fmt.Errorf("append continuous dispatch receipt: %w", err)
		}
		stored.ReviewProvenance = cloneContinuousDispatchReviewProvenance(receipt.ReviewProvenance)
		if !continuousDispatchReceiptsEqual(stored, receipt) {
			return ErrContinuousDispatchConflict
		}
		receipt = stored
		created = true
		return nil
	})
	if err != nil {
		return ContinuousDispatchReceipt{}, fmt.Errorf("dispatch exact generation: %w", err)
	}

	if created {
		s.backend.NotifyContinuousDispatchTask(ctx, task)
	}
	return receipt, nil
}

func validateContinuousDispatchRequest(req ContinuousDispatchRequest) error {
	if !req.Identity.Complete() {
		return fmt.Errorf("continuous dispatch identity is incomplete")
	}
	if !parseDispatchUUID(req.Identity.WorkspaceID).Valid || !parseDispatchUUID(req.Identity.IssueID).Valid {
		return fmt.Errorf("continuous dispatch identity UUIDs are invalid")
	}
	for name, value := range map[string]pgtype.UUID{
		"local_agent_id": req.Route.LocalAgentID,
		"runtime_id":     req.Route.RuntimeID,
		"actor_user_id":  req.ActorUserID,
	} {
		if !value.Valid || value.Bytes == ([16]byte{}) {
			return fmt.Errorf("%s is required", name)
		}
	}
	if !strings.HasPrefix(req.Route.EmployeeRef, continuousDispatchEmployeeRefPrefix) ||
		strings.TrimSpace(strings.TrimPrefix(req.Route.EmployeeRef, continuousDispatchEmployeeRefPrefix)) == "" {
		return fmt.Errorf("employee_ref must use the canonical HiveCosm employee scheme")
	}
	for name, value := range map[string]string{"model": req.Route.Model, "account_ref": req.Route.AccountRef} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if req.requireInReview != (req.reviewProvenance != nil) {
		return fmt.Errorf("review dispatch requires server-built provenance")
	}
	if req.reviewProvenance != nil {
		if err := validateContinuousDispatchReviewProvenance(*req.reviewProvenance, req.Identity); err != nil {
			return err
		}
	}
	return nil
}

func validateContinuousDispatchIssue(issue db.Issue, identity continuousdispatch.DispatchIdentity, requireInReview bool) error {
	if !issue.ID.Valid || !issue.WorkspaceID.Valid ||
		shadowUUIDString(issue.ID) != identity.IssueID || shadowUUIDString(issue.WorkspaceID) != identity.WorkspaceID {
		return ErrContinuousDispatchIssueDrift
	}
	switch issue.Status {
	case "done", "cancelled", "backlog":
		return ErrContinuousDispatchIssueNotReady
	}
	if requireInReview && issue.Status != "in_review" {
		return ErrContinuousDispatchIssueDrift
	}
	metadata := parseShadowMetadata(issue.Metadata)
	if metadata.Stage != identity.Stage || metadata.CandidateRevision != identity.CandidateRevision ||
		metadata.Generation != identity.Generation {
		return ErrContinuousDispatchIssueDrift
	}
	return nil
}

func continuousDispatchReceiptMatchesRequest(
	receipt ContinuousDispatchReceipt,
	req ContinuousDispatchRequest,
	digest string,
) bool {
	return receipt.Identity == req.Identity && receipt.TaskID.Valid &&
		receipt.EmployeeRef == req.Route.EmployeeRef && receipt.LocalAgentID == req.Route.LocalAgentID &&
		receipt.RuntimeID == req.Route.RuntimeID && receipt.Model == req.Route.Model &&
		receipt.AccountRef == req.Route.AccountRef && receipt.RequestDigest == digest &&
		continuousDispatchReviewProvenanceEqual(receipt.ReviewProvenance, req.reviewProvenance)
}

func continuousDispatchRequestDigest(req ContinuousDispatchRequest) (string, error) {
	canonical := struct {
		WorkspaceID       string                              `json:"workspace_id"`
		IssueID           string                              `json:"issue_id"`
		Stage             string                              `json:"stage"`
		CandidateRevision string                              `json:"candidate_revision"`
		Generation        string                              `json:"generation"`
		EmployeeRef       string                              `json:"employee_ref"`
		LocalAgentID      string                              `json:"local_agent_id"`
		RuntimeID         string                              `json:"runtime_id"`
		Model             string                              `json:"model"`
		AccountRef        string                              `json:"account_ref"`
		HandoffNote       string                              `json:"handoff_note"`
		RequireInReview   bool                                `json:"require_in_review"`
		ReviewProvenance  *ContinuousDispatchReviewProvenance `json:"review_provenance,omitempty"`
	}{
		WorkspaceID: req.Identity.WorkspaceID, IssueID: req.Identity.IssueID, Stage: req.Identity.Stage,
		CandidateRevision: req.Identity.CandidateRevision, Generation: req.Identity.Generation,
		EmployeeRef: req.Route.EmployeeRef, LocalAgentID: shadowUUIDString(req.Route.LocalAgentID),
		RuntimeID: shadowUUIDString(req.Route.RuntimeID), Model: req.Route.Model,
		AccountRef: req.Route.AccountRef, HandoffNote: req.HandoffNote, RequireInReview: req.requireInReview,
		ReviewProvenance: req.reviewProvenance,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func continuousDispatchReviewCommentRef(commentID pgtype.UUID) string {
	return continuousDispatchReviewCommentRefPrefix + shadowUUIDString(commentID)
}

func parseContinuousDispatchReviewCommentRef(sourceRef string) (pgtype.UUID, bool) {
	canonical := strings.TrimSpace(sourceRef)
	if !strings.HasPrefix(canonical, continuousDispatchReviewCommentRefPrefix) {
		return pgtype.UUID{}, false
	}
	commentID := parseDispatchUUID(strings.TrimPrefix(canonical, continuousDispatchReviewCommentRefPrefix))
	if !commentID.Valid || continuousDispatchReviewCommentRef(commentID) != canonical {
		return pgtype.UUID{}, false
	}
	return commentID, true
}

func validateContinuousDispatchReviewProvenance(
	provenance ContinuousDispatchReviewProvenance,
	identity continuousdispatch.DispatchIdentity,
) error {
	if identity.Stage != "review" {
		return fmt.Errorf("review provenance requires review stage")
	}
	if _, ok := parseContinuousDispatchReviewCommentRef(provenance.SourceRef); !ok {
		return fmt.Errorf("review source_ref must be a canonical comment reference")
	}
	if sourceIssueID := parseDispatchUUID(provenance.SourceIssueID); !sourceIssueID.Valid || provenance.SourceIssueID != identity.IssueID {
		return fmt.Errorf("review source_issue_id must match dispatch issue")
	}
	if sourceTaskID := parseDispatchUUID(provenance.SourceTaskID); !sourceTaskID.Valid ||
		provenance.SourceTaskID != shadowUUIDString(sourceTaskID) {
		return fmt.Errorf("review source_task_id must be canonical")
	}
	if provenance.InitiatorSource != continuousDispatchReviewInitiatorSourceV1 {
		return fmt.Errorf("review initiator_source is not admitted")
	}
	return nil
}

func cloneContinuousDispatchReviewProvenance(value *ContinuousDispatchReviewProvenance) *ContinuousDispatchReviewProvenance {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func continuousDispatchReviewProvenanceEqual(left, right *ContinuousDispatchReviewProvenance) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func continuousDispatchReviewProvenanceFromTaskContext(
	raw []byte,
	identity continuousdispatch.DispatchIdentity,
) (ContinuousDispatchReviewProvenance, error) {
	var envelope shadowTaskContext
	if len(raw) == 0 || json.Unmarshal(raw, &envelope) != nil || envelope.ReviewDispatch == nil {
		return ContinuousDispatchReviewProvenance{}, ErrContinuousDispatchReviewLineageDrift
	}
	if err := validateContinuousDispatchReviewProvenance(*envelope.ReviewDispatch, identity); err != nil {
		return ContinuousDispatchReviewProvenance{}, ErrContinuousDispatchReviewLineageDrift
	}
	return *envelope.ReviewDispatch, nil
}

// getContinuousDispatchReceipt returns a typed found flag so callers do not
// need to infer idempotency from database errors.
func getContinuousDispatchReceipt(
	ctx context.Context,
	queries *db.Queries,
	identity continuousdispatch.DispatchIdentity,
) (ContinuousDispatchReceipt, bool, error) {
	receipt, err := NewContinuousDispatchReceiptRepository(queries).Get(ctx, identity)
	if errors.Is(err, pgx.ErrNoRows) {
		return ContinuousDispatchReceipt{}, false, nil
	}
	if err != nil {
		return ContinuousDispatchReceipt{}, false, err
	}
	return receipt, true, nil
}
