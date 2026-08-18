package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const assignmentDispatchEvidenceKind = "assignment_dispatch"

// ErrCompanyOpsAssignmentConflict means an idempotency command was already
// committed with a different immutable assignment payload.
var ErrCompanyOpsAssignmentConflict = errors.New("companyops assignment command payload conflict")

// ErrCompanyOpsCapacityReject / ErrCompanyOpsCapacityDefer are returned by
// Dispatch when the Lane D capacity router fails the bound agent closed or
// asks the caller to retry later. They carry the router's stable reason via
// the wrapped %w chain.
var (
	ErrCompanyOpsCapacityReject = errors.New("companyops assignment rejected by capacity router")
	ErrCompanyOpsCapacityDefer  = errors.New("companyops assignment deferred by capacity router")
)

// CompanyOpsAssignmentRequest carries authoritative observations plus the
// local HiveCrew projection and execution carrier selected for one assignment.
// Dispatch validates and freezes the authority chain before opening a write
// transaction.
type CompanyOpsAssignmentRequest struct {
	CommandID           pgtype.UUID
	WorkspaceID         pgtype.UUID
	IssueID             pgtype.UUID
	ProjectID           pgtype.UUID
	LocalAgentID        pgtype.UUID
	LocalAgentSourceRef string
	ActorUserID         pgtype.UUID
	HandoffNote         string

	WorkOrder   companyops.AuthoritySnapshot
	InputDigest string
	Employee    companyops.AuthoritySnapshot
	Bindings    []companyops.IdentityBinding
	Agents      []companyops.AuthoritySnapshot

	// CapacityCandidate is an optional observation of the bound agent's
	// current capacity (remaining quota, health, role, base, concurrency
	// slots). Nil preserves the legacy dispatch path (no capacity gate); when
	// set, Dispatch consults the capacity router before opening the write
	// transaction and maps defer/reject to the sentinel errors above.
	CapacityCandidate *metrics.CapacityCandidate
}

// AssignmentDispatchReceipt is the immutable result of assigning one local
// Issue and creating its initial Run. Retries remain separate Run rows while
// carrying this command UUID as their inherited trigger evidence.
type AssignmentDispatchReceipt struct {
	CommandID     pgtype.UUID
	WorkspaceID   pgtype.UUID
	IssueID       pgtype.UUID
	LocalAgentID  pgtype.UUID
	InitialTaskID pgtype.UUID
	Target        companyops.ExecutionTargetSnapshot
}

// CompanyOpsAssignmentTx is the narrow transaction-local seam implemented by
// the canonical IssueService/TaskService database adapter. Implementations must
// not publish events or notify daemons from these methods.
type CompanyOpsAssignmentTx interface {
	LockAssignmentCommand(ctx context.Context, workspaceID, commandID pgtype.UUID) error
	GetAssignmentDispatchReceipt(ctx context.Context, workspaceID, commandID pgtype.UUID) (AssignmentDispatchReceipt, bool, error)
	EnsureWorkOrderIssue(ctx context.Context, req CompanyOpsAssignmentRequest) (CompanyOpsWorkOrderProjection, error)
	CreatedWorkOrderProjection() *CompanyOpsWorkOrderProjection
	AssignIssueExact(ctx context.Context, req CompanyOpsAssignmentRequest, target companyops.ExecutionTargetSnapshot) error
	EnqueueAssignmentTask(
		ctx context.Context,
		req CompanyOpsAssignmentRequest,
		target companyops.ExecutionTargetSnapshot,
		evidenceKind string,
		evidenceRefID pgtype.UUID,
	) (db.AgentTaskQueue, error)
	AppendAssignmentDispatchReceipt(ctx context.Context, receipt AssignmentDispatchReceipt) error
}

// CompanyOpsAssignmentBackend owns transaction boundaries and the two
// post-commit effects. Its production implementation connects the narrow
// transaction seam to existing canonical IssueService/TaskService helpers.
type CompanyOpsAssignmentBackend interface {
	RunInCompanyOpsAssignmentTx(ctx context.Context, fn func(CompanyOpsAssignmentTx) error) error
	FinishWorkOrderProjection(ctx context.Context, projection CompanyOpsWorkOrderProjection)
	PublishAssignmentDispatched(ctx context.Context, receipt AssignmentDispatchReceipt)
	NotifyAssignmentTaskAvailable(ctx context.Context, task db.AgentTaskQueue)
}

// CompanyOpsAssignmentService orchestrates authority validation, idempotent
// local assignment, Run creation, and immutable receipt append.
type CompanyOpsAssignmentService struct {
	backend CompanyOpsAssignmentBackend
	// capacity is the Lane D gate consulted only when the request carries a
	// CapacityCandidate. The default is the deterministic static router; it is
	// nil-safe and never changes the authority-frozen target.
	capacity metrics.CapacityRouter
}

func NewCompanyOpsAssignmentService(backend CompanyOpsAssignmentBackend) *CompanyOpsAssignmentService {
	return &CompanyOpsAssignmentService{
		backend:  backend,
		capacity: metrics.NewStaticCapacityRouter(),
	}
}

// WithCapacityRouter replaces the default static capacity router. Used by
// tests and by future wiring that supplies richer signal collection; a nil
// router disables the gate entirely.
func (s *CompanyOpsAssignmentService) WithCapacityRouter(router metrics.CapacityRouter) *CompanyOpsAssignmentService {
	s.capacity = router
	return s
}

// Dispatch performs no write until the full authority chain and exact local
// Agent reference are validated. New dispatches commit exact assignment, task,
// and receipt atomically; exact replays only return the committed receipt.
func (s *CompanyOpsAssignmentService) Dispatch(
	ctx context.Context,
	req CompanyOpsAssignmentRequest,
) (AssignmentDispatchReceipt, error) {
	if s == nil || s.backend == nil {
		return AssignmentDispatchReceipt{}, fmt.Errorf("companyops assignment backend is required")
	}
	if err := validateCompanyOpsAssignmentIDs(req); err != nil {
		return AssignmentDispatchReceipt{}, err
	}
	if !req.ActorUserID.Valid || req.ActorUserID.Bytes == ([16]byte{}) {
		return AssignmentDispatchReceipt{}, fmt.Errorf("actor_user_id is required")
	}
	expectedInputDigest := CompanyOpsHandoffInputDigest(req.HandoffNote)
	if req.InputDigest != expectedInputDigest {
		return AssignmentDispatchReceipt{}, fmt.Errorf("input_digest does not match the exact handoff note")
	}
	expectedLocalAgentRef := "/api/agents/" + util.UUIDToString(req.LocalAgentID)
	if req.LocalAgentSourceRef != expectedLocalAgentRef {
		return AssignmentDispatchReceipt{}, fmt.Errorf(
			"local agent source_ref %q does not identify local_agent_id %q",
			req.LocalAgentSourceRef,
			util.UUIDToString(req.LocalAgentID),
		)
	}

	target, err := companyops.ValidateAndFreezeExecutionTarget(
		req.WorkOrder,
		req.InputDigest,
		req.Employee,
		req.Bindings,
		req.Agents,
	)
	if err != nil {
		return AssignmentDispatchReceipt{}, fmt.Errorf("validate execution target: %w", err)
	}
	if req.LocalAgentSourceRef != target.AgentRef {
		return AssignmentDispatchReceipt{}, fmt.Errorf(
			"local agent source_ref %q does not match exact authority target %q",
			req.LocalAgentSourceRef,
			target.AgentRef,
		)
	}

	if err := gateCapacityDispatch(ctx, s, req, target); err != nil {
		return AssignmentDispatchReceipt{}, err
	}

	var (
		receipt           AssignmentDispatchReceipt
		task              db.AgentTaskQueue
		created           bool
		createdProjection *CompanyOpsWorkOrderProjection
	)
	err = s.backend.RunInCompanyOpsAssignmentTx(ctx, func(tx CompanyOpsAssignmentTx) error {
		if tx == nil {
			return fmt.Errorf("companyops assignment transaction is required")
		}
		if err := tx.LockAssignmentCommand(ctx, req.WorkspaceID, req.CommandID); err != nil {
			return fmt.Errorf("lock assignment command: %w", err)
		}

		existing, found, err := tx.GetAssignmentDispatchReceipt(ctx, req.WorkspaceID, req.CommandID)
		if err != nil {
			return fmt.Errorf("get assignment dispatch receipt: %w", err)
		}
		if found {
			// A project-bound request may omit IssueID on the wire because the
			// Issue is created in this transaction. Re-observe the WorkOrder
			// projection before accepting a replay so the command cannot be
			// reused with a different project.
			if req.ProjectID.Valid {
				projection, err := tx.EnsureWorkOrderIssue(ctx, req)
				if err != nil {
					return fmt.Errorf("verify project-bound WorkOrder replay: %w", err)
				}
				issue := projection.Issue
				if issue.ID != existing.IssueID || issue.WorkspaceID != req.WorkspaceID || issue.ProjectID != req.ProjectID {
					return ErrCompanyOpsAssignmentConflict
				}
				req.IssueID = issue.ID
			}
			if !assignmentReceiptMatches(existing, req, target) {
				return ErrCompanyOpsAssignmentConflict
			}
			receipt = existing
			return nil
		}
		if !req.IssueID.Valid {
			if !req.ProjectID.Valid {
				return fmt.Errorf("issue_id or project_id is required")
			}
			projection, err := tx.EnsureWorkOrderIssue(ctx, req)
			if err != nil {
				return fmt.Errorf("ensure project-bound WorkOrder issue: %w", err)
			}
			issue := projection.Issue
			if !issue.ID.Valid || issue.WorkspaceID != req.WorkspaceID || issue.ProjectID != req.ProjectID {
				return fmt.Errorf("project-bound WorkOrder issue does not match exact workspace and project")
			}
			req.IssueID = issue.ID
		}

		if err := tx.AssignIssueExact(ctx, req, target); err != nil {
			return fmt.Errorf("assign issue exact: %w", err)
		}
		task, err = tx.EnqueueAssignmentTask(
			ctx,
			req,
			target,
			assignmentDispatchEvidenceKind,
			req.CommandID,
		)
		if err != nil {
			return fmt.Errorf("enqueue assignment task: %w", err)
		}
		if !task.ID.Valid {
			return fmt.Errorf("enqueue assignment task returned an invalid task id")
		}

		receipt = AssignmentDispatchReceipt{
			CommandID:     req.CommandID,
			WorkspaceID:   req.WorkspaceID,
			IssueID:       req.IssueID,
			LocalAgentID:  req.LocalAgentID,
			InitialTaskID: task.ID,
			Target:        target,
		}
		if err := tx.AppendAssignmentDispatchReceipt(ctx, receipt); err != nil {
			return fmt.Errorf("append assignment dispatch receipt: %w", err)
		}
		created = true
		if projection := tx.CreatedWorkOrderProjection(); projection != nil {
			createdProjection = projection
		}
		return nil
	})
	if err != nil {
		return AssignmentDispatchReceipt{}, fmt.Errorf("dispatch companyops assignment: %w", err)
	}

	if created {
		if createdProjection != nil {
			s.backend.FinishWorkOrderProjection(ctx, *createdProjection)
		}
		s.backend.PublishAssignmentDispatched(ctx, receipt)
		s.backend.NotifyAssignmentTaskAvailable(ctx, task)
	}
	return receipt, nil
}

// CompanyOpsHandoffInputDigest is the canonical digest of the exact UTF-8
// handoff text delivered to the Agent task. The API computes this server-side;
// browsers never supply or choose an authority digest.
func CompanyOpsHandoffInputDigest(handoffNote string) string {
	sum := sha256.Sum256([]byte(handoffNote))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateCompanyOpsAssignmentIDs(req CompanyOpsAssignmentRequest) error {
	for name, value := range map[string]pgtype.UUID{
		"command_id":     req.CommandID,
		"workspace_id":   req.WorkspaceID,
		"local_agent_id": req.LocalAgentID,
	} {
		if !value.Valid || value.Bytes == ([16]byte{}) {
			return fmt.Errorf("%s is required", name)
		}
	}
	if !req.IssueID.Valid && !req.ProjectID.Valid {
		return fmt.Errorf("issue_id or project_id is required")
	}
	if req.ProjectID.Valid && req.ProjectID.Bytes == ([16]byte{}) {
		return fmt.Errorf("project_id is invalid")
	}
	return nil
}

func assignmentReceiptMatches(
	receipt AssignmentDispatchReceipt,
	req CompanyOpsAssignmentRequest,
	target companyops.ExecutionTargetSnapshot,
) bool {
	return receipt.CommandID == req.CommandID &&
		receipt.WorkspaceID == req.WorkspaceID &&
		(!req.IssueID.Valid || receipt.IssueID == req.IssueID) &&
		receipt.LocalAgentID == req.LocalAgentID &&
		receipt.InitialTaskID.Valid &&
		receipt.Target == target
}

// gateCapacityDispatch consults the Lane D capacity router for the bound agent
// before any write. The authority-frozen target never changes: the router only
// grants, defers, or rejects the exact bound agent based on the caller-supplied
// capacity observation. No observation means no gate.
func gateCapacityDispatch(
	ctx context.Context,
	s *CompanyOpsAssignmentService,
	req CompanyOpsAssignmentRequest,
	target companyops.ExecutionTargetSnapshot,
) error {
	if req.CapacityCandidate == nil {
		return nil
	}
	if s == nil || s.capacity == nil {
		return fmt.Errorf("capacity router is required when a capacity candidate is supplied")
	}
	if req.CapacityCandidate.AgentID != util.UUIDToString(req.LocalAgentID) {
		return fmt.Errorf("%w: capacity candidate agent %q does not match bound agent %q",
			ErrCompanyOpsCapacityReject,
			req.CapacityCandidate.AgentID,
			util.UUIDToString(req.LocalAgentID))
	}
	decision := s.capacity.RouteCapacity(ctx, metrics.CapacityRouteRequest{
		TaskID:      util.UUIDToString(req.CommandID),
		EmployeeRef: target.EmployeeRef,
		Candidates:  []metrics.CapacityCandidate{*req.CapacityCandidate},
	})
	switch decision.Decision {
	case metrics.CapacityDecisionGrant:
		return nil
	case metrics.CapacityDecisionDefer:
		return fmt.Errorf("%w: %s", ErrCompanyOpsCapacityDefer, decision.Reason)
	case metrics.CapacityDecisionReject:
		return fmt.Errorf("%w: %s", ErrCompanyOpsCapacityReject, decision.Reason)
	default:
		return fmt.Errorf("%w: unknown decision %q", ErrCompanyOpsCapacityReject, decision.Decision)
	}
}
