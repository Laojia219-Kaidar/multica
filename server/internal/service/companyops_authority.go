package service

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// CompanyOpsHiveCosmAuthorityReader is the read-only boundary for the three
// company-owned objects that authorize an Owner assignment.
type CompanyOpsHiveCosmAuthorityReader interface {
	ResolveOwnerWorkContext(
		ctx context.Context,
		lookup companyops.HiveCosmAuthorityLookup,
	) (companyops.HiveCosmAuthorityBundle, error)
}

// CompanyOpsAgentAuthorityReader is deliberately limited to the canonical
// HiveCrew Agent row. HiveCosm never supplies or overrides this object.
type CompanyOpsAgentAuthorityReader interface {
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
}

// ResolvedCompanyOpsAuthority is the joined, current cross-system authority
// chain. It is still read-only; the assignment service freezes it together
// with the actual Owner input digest before opening a write transaction.
type ResolvedCompanyOpsAuthority struct {
	WorkOrder       companyops.AuthoritySnapshot
	Employee        companyops.AuthoritySnapshot
	IdentityBinding companyops.IdentityBinding
	Agent           db.Agent
	AgentAuthority  companyops.AuthoritySnapshot
}

// CompanyOpsAuthorityResolver joins company-owned authority to one exact local
// Agent UUID. It never falls back to display name, model, list order, or a
// HiveCosm-projected Agent record.
type CompanyOpsAuthorityResolver struct {
	hiveCosm CompanyOpsHiveCosmAuthorityReader
	agents   CompanyOpsAgentAuthorityReader
}

func NewCompanyOpsAuthorityResolver(
	hiveCosm CompanyOpsHiveCosmAuthorityReader,
	agents CompanyOpsAgentAuthorityReader,
) *CompanyOpsAuthorityResolver {
	return &CompanyOpsAuthorityResolver{hiveCosm: hiveCosm, agents: agents}
}

func (r *CompanyOpsAuthorityResolver) Resolve(
	ctx context.Context,
	workspaceID pgtype.UUID,
	lookup companyops.HiveCosmAuthorityLookup,
) (ResolvedCompanyOpsAuthority, error) {
	if r == nil || r.hiveCosm == nil || r.agents == nil {
		return ResolvedCompanyOpsAuthority{}, fmt.Errorf("companyops authority resolver is not configured")
	}
	if !workspaceID.Valid || workspaceID.Bytes == ([16]byte{}) {
		return ResolvedCompanyOpsAuthority{}, fmt.Errorf("workspace_id is required")
	}

	bundle, err := r.hiveCosm.ResolveOwnerWorkContext(ctx, lookup)
	if err != nil {
		return ResolvedCompanyOpsAuthority{}, fmt.Errorf("resolve HiveCosm authority: %w", err)
	}
	if bundle.RequestedAgentID != lookup.AgentID {
		return ResolvedCompanyOpsAuthority{}, fmt.Errorf("HiveCosm authority returned a different requested Agent UUID")
	}

	agentID, err := util.ParseUUID(lookup.AgentID)
	if err != nil || !agentID.Valid || agentID.Bytes == ([16]byte{}) {
		return ResolvedCompanyOpsAuthority{}, fmt.Errorf("agent_id is not a canonical UUID")
	}
	agent, err := r.agents.GetAgent(ctx, agentID)
	if err != nil {
		return ResolvedCompanyOpsAuthority{}, fmt.Errorf("read HiveCrew Agent authority: %w", err)
	}
	agentAuthority, err := companyops.BuildHiveCrewAgentAuthoritySnapshot(
		agent,
		util.UUIDToString(workspaceID),
		lookup.AgentID,
	)
	if err != nil {
		return ResolvedCompanyOpsAuthority{}, fmt.Errorf("build HiveCrew Agent authority: %w", err)
	}
	if bundle.IdentityBinding.AgentRef != agentAuthority.SourceRef {
		return ResolvedCompanyOpsAuthority{}, fmt.Errorf(
			"IdentityBinding agent_ref %q does not match local Agent authority %q",
			bundle.IdentityBinding.AgentRef,
			agentAuthority.SourceRef,
		)
	}

	return ResolvedCompanyOpsAuthority{
		WorkOrder:       bundle.WorkOrder,
		Employee:        bundle.Employee,
		IdentityBinding: bundle.IdentityBinding,
		Agent:           agent,
		AgentAuthority:  agentAuthority,
	}, nil
}

// AssignmentRequest combines the resolved read chain with the Owner's exact
// issue, command and input digest. CompanyOpsAssignmentService remains the sole
// writer and revalidates/freezes the complete chain before transaction start.
func (a ResolvedCompanyOpsAuthority) AssignmentRequest(
	commandID pgtype.UUID,
	workspaceID pgtype.UUID,
	issueID pgtype.UUID,
	actorUserID pgtype.UUID,
	handoffNote string,
) CompanyOpsAssignmentRequest {
	return CompanyOpsAssignmentRequest{
		CommandID:           commandID,
		WorkspaceID:         workspaceID,
		IssueID:             issueID,
		LocalAgentID:        a.Agent.ID,
		LocalAgentSourceRef: a.AgentAuthority.SourceRef,
		ActorUserID:         actorUserID,
		HandoffNote:         handoffNote,
		WorkOrder:           a.WorkOrder,
		InputDigest:         CompanyOpsHandoffInputDigest(handoffNote),
		Employee:            a.Employee,
		Bindings:            []companyops.IdentityBinding{a.IdentityBinding},
		Agents:              []companyops.AuthoritySnapshot{a.AgentAuthority},
	}
}
