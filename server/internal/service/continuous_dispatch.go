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
	ErrContinuousDispatchConflict      = errors.New("continuous dispatch request conflicts with committed generation")
	ErrContinuousDispatchIssueDrift    = errors.New("continuous dispatch issue identity drifted")
	ErrContinuousDispatchRouteDrift    = errors.New("continuous dispatch execution route drifted")
	ErrContinuousDispatchIssueNotReady = errors.New("continuous dispatch issue is not ready")
)

const continuousDispatchEmployeeRefPrefix = "hivecosm://employees/"

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

// ContinuousDispatchRequest is a server-built command for one exact shadow
// generation. Public handlers must recompute the selected route immediately
// before calling Dispatch; browsers do not choose identity or capacity truth.
type ContinuousDispatchRequest struct {
	Identity    continuousdispatch.DispatchIdentity
	Route       ContinuousDispatchRoute
	ActorUserID pgtype.UUID
	HandoffNote string
}

// ContinuousDispatchTx is the transaction-local seam. Implementations must
// not publish events or wake runtimes before the surrounding transaction has
// committed successfully.
type ContinuousDispatchTx interface {
	LockIdentity(context.Context, continuousdispatch.DispatchIdentity) error
	GetReceipt(context.Context, continuousdispatch.DispatchIdentity) (ContinuousDispatchReceipt, bool, error)
	LoadIssue(context.Context, continuousdispatch.DispatchIdentity) (db.Issue, error)
	PrepareTask(context.Context, db.Issue, ContinuousDispatchRequest) (db.AgentTaskQueue, error)
	StampTaskIdentity(context.Context, db.AgentTaskQueue, continuousdispatch.DispatchIdentity) (db.AgentTaskQueue, error)
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
		if err := validateContinuousDispatchIssue(issue, req.Identity); err != nil {
			return err
		}

		task, err = tx.PrepareTask(ctx, issue, req)
		if err != nil {
			return fmt.Errorf("prepare continuous dispatch task: %w", err)
		}
		if !task.ID.Valid || task.ID.Bytes == ([16]byte{}) || task.IssueID != issue.ID ||
			task.AgentID != req.Route.LocalAgentID || task.RuntimeID != req.Route.RuntimeID || task.Status != "queued" {
			return ErrContinuousDispatchRouteDrift
		}

		task, err = tx.StampTaskIdentity(ctx, task, req.Identity)
		if err != nil {
			return fmt.Errorf("stamp continuous dispatch task identity: %w", err)
		}

		receipt = ContinuousDispatchReceipt{
			Identity: req.Identity, TaskID: task.ID, EmployeeRef: req.Route.EmployeeRef,
			LocalAgentID: req.Route.LocalAgentID, RuntimeID: req.Route.RuntimeID,
			Model: req.Route.Model, AccountRef: req.Route.AccountRef, RequestDigest: digest,
		}
		stored, err := tx.AppendReceipt(ctx, receipt)
		if err != nil {
			return fmt.Errorf("append continuous dispatch receipt: %w", err)
		}
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
	return nil
}

func validateContinuousDispatchIssue(issue db.Issue, identity continuousdispatch.DispatchIdentity) error {
	if !issue.ID.Valid || !issue.WorkspaceID.Valid ||
		shadowUUIDString(issue.ID) != identity.IssueID || shadowUUIDString(issue.WorkspaceID) != identity.WorkspaceID {
		return ErrContinuousDispatchIssueDrift
	}
	switch issue.Status {
	case "done", "cancelled", "backlog":
		return ErrContinuousDispatchIssueNotReady
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
		receipt.AccountRef == req.Route.AccountRef && receipt.RequestDigest == digest
}

func continuousDispatchRequestDigest(req ContinuousDispatchRequest) (string, error) {
	canonical := struct {
		WorkspaceID       string `json:"workspace_id"`
		IssueID           string `json:"issue_id"`
		Stage             string `json:"stage"`
		CandidateRevision string `json:"candidate_revision"`
		Generation        string `json:"generation"`
		EmployeeRef       string `json:"employee_ref"`
		LocalAgentID      string `json:"local_agent_id"`
		RuntimeID         string `json:"runtime_id"`
		Model             string `json:"model"`
		AccountRef        string `json:"account_ref"`
		HandoffNote       string `json:"handoff_note"`
	}{
		WorkspaceID: req.Identity.WorkspaceID, IssueID: req.Identity.IssueID, Stage: req.Identity.Stage,
		CandidateRevision: req.Identity.CandidateRevision, Generation: req.Identity.Generation,
		EmployeeRef: req.Route.EmployeeRef, LocalAgentID: shadowUUIDString(req.Route.LocalAgentID),
		RuntimeID: shadowUUIDString(req.Route.RuntimeID), Model: req.Route.Model,
		AccountRef: req.Route.AccountRef, HandoffNote: req.HandoffNote,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
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
