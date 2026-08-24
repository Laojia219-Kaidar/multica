package orcabridge

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

// AssignmentDispatchPort is the single HiveCrew dispatch entry the bridge
// reuses. The bridge never creates its own assignment, issue, task, or run
// rows: the existing CompanyOpsAssignmentService owns that write path, its
// authority validation, and its command-id idempotency.
type AssignmentDispatchPort interface {
	// DispatchAssignment dispatches one assignment command through the
	// existing governed entry and returns the committed HiveCrew lineage
	// (created issue and initial task). Replays with the same command id
	// return the committed outcome.
	DispatchAssignment(ctx context.Context, cmd AssignmentCommand) (AssignmentOutcome, error)
}

// AssignmentCommand carries everything the existing dispatch entry needs.
// The authority chain (WorkOrder/Employee/Bindings/Agents snapshots) is
// passed through unchanged; the existing service validates it and fails
// closed. The bridge never re-derives or second-guesses authority.
type AssignmentCommand struct {
	// CommandID is the HiveCrew assignment command id: the idempotency
	// anchor for exactly one run attempt.
	CommandID string
	// WorkspaceID scopes the command.
	WorkspaceID string
	// ProjectID is required for project-bound dispatches (the issue is
	// created inside the existing transaction).
	ProjectID string
	// IssueID is optional when ProjectID is set.
	IssueID string
	// LocalAgentID / LocalAgentSourceRef identify the exact bound agent.
	LocalAgentID        string
	LocalAgentSourceRef string
	// ActorUserID is the human owner authorizing the dispatch.
	ActorUserID string
	// HandoffNote is the frozen work input; its digest is computed by the
	// adapter exactly as the existing service requires.
	HandoffNote string

	WorkOrder companyops.AuthoritySnapshot
	Employee  companyops.AuthoritySnapshot
	Bindings  []companyops.IdentityBinding
	Agents    []companyops.AuthoritySnapshot
}

// AssignmentOutcome is the committed HiveCrew lineage of one dispatch.
type AssignmentOutcome struct {
	CommandID     string
	WorkspaceID   string
	IssueID       string
	InitialTaskID string
}

// CompanyOpsDispatchAdapter adapts the existing CompanyOpsAssignmentService to
// the port. It performs uuid parsing and digest computation only; every write
// and every authority decision stays with the existing service.
type CompanyOpsDispatchAdapter struct {
	Service *service.CompanyOpsAssignmentService
}

// NewCompanyOpsDispatchPort builds the production dispatch port over the
// existing assignment service.
func NewCompanyOpsDispatchPort(svc *service.CompanyOpsAssignmentService) AssignmentDispatchPort {
	return &CompanyOpsDispatchAdapter{Service: svc}
}

// DispatchAssignment forwards one command to the existing dispatch entry.
func (a *CompanyOpsDispatchAdapter) DispatchAssignment(ctx context.Context, cmd AssignmentCommand) (AssignmentOutcome, error) {
	if a == nil || a.Service == nil {
		return AssignmentOutcome{}, fmt.Errorf("orcabridge: companyops assignment service is required")
	}
	commandID, err := parseRequiredUUID("command id", cmd.CommandID)
	if err != nil {
		return AssignmentOutcome{}, err
	}
	workspaceID, err := parseRequiredUUID("workspace id", cmd.WorkspaceID)
	if err != nil {
		return AssignmentOutcome{}, err
	}
	localAgentID, err := parseRequiredUUID("local agent id", cmd.LocalAgentID)
	if err != nil {
		return AssignmentOutcome{}, err
	}
	actorUserID, err := parseRequiredUUID("actor user id", cmd.ActorUserID)
	if err != nil {
		return AssignmentOutcome{}, err
	}
	var issueID, projectID pgtype.UUID
	if cmd.IssueID != "" {
		issueID, err = parseRequiredUUID("issue id", cmd.IssueID)
		if err != nil {
			return AssignmentOutcome{}, err
		}
	}
	if cmd.ProjectID != "" {
		projectID, err = parseRequiredUUID("project id", cmd.ProjectID)
		if err != nil {
			return AssignmentOutcome{}, err
		}
	}
	receipt, err := a.Service.Dispatch(ctx, service.CompanyOpsAssignmentRequest{
		CommandID:           commandID,
		WorkspaceID:         workspaceID,
		IssueID:             issueID,
		ProjectID:           projectID,
		LocalAgentID:        localAgentID,
		LocalAgentSourceRef: cmd.LocalAgentSourceRef,
		ActorUserID:         actorUserID,
		HandoffNote:         cmd.HandoffNote,
		InputDigest:         service.CompanyOpsHandoffInputDigest(cmd.HandoffNote),
		WorkOrder:           cmd.WorkOrder,
		Employee:            cmd.Employee,
		Bindings:            cmd.Bindings,
		Agents:              cmd.Agents,
	})
	if err != nil {
		return AssignmentOutcome{}, fmt.Errorf("orcabridge: dispatch through companyops assignment entry: %w", err)
	}
	outcome := AssignmentOutcome{
		CommandID:     util.UUIDToString(receipt.CommandID),
		WorkspaceID:   util.UUIDToString(receipt.WorkspaceID),
		IssueID:       util.UUIDToString(receipt.IssueID),
		InitialTaskID: util.UUIDToString(receipt.InitialTaskID),
	}
	if outcome.CommandID == "" || outcome.WorkspaceID == "" || outcome.IssueID == "" || outcome.InitialTaskID == "" {
		return AssignmentOutcome{}, fmt.Errorf("orcabridge: assignment receipt lineage is incomplete: %+v", outcome)
	}
	return outcome, nil
}

func parseRequiredUUID(label, value string) (pgtype.UUID, error) {
	parsed, err := util.ParseUUID(value)
	if err != nil || !parsed.Valid {
		return pgtype.UUID{}, fmt.Errorf("%w: %s %q is not a canonical uuid", ErrInvalidChain, label, value)
	}
	return parsed, nil
}
