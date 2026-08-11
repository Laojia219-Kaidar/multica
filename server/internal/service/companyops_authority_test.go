package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	authorityResolverWorkspaceID = "11111111-1111-4111-8111-111111111111"
	authorityResolverAgentID     = "22222222-2222-4222-8222-222222222222"
)

type stubCompanyOpsHiveCosmAuthorityReader struct {
	bundle companyops.HiveCosmAuthorityBundle
	err    error
}

func (s stubCompanyOpsHiveCosmAuthorityReader) ResolveOwnerWorkContext(
	context.Context,
	companyops.HiveCosmAuthorityLookup,
) (companyops.HiveCosmAuthorityBundle, error) {
	return s.bundle, s.err
}

type stubCompanyOpsAgentAuthorityReader struct {
	agent db.Agent
	err   error
}

func (s stubCompanyOpsAgentAuthorityReader) GetAgent(context.Context, pgtype.UUID) (db.Agent, error) {
	return s.agent, s.err
}

func resolverAuthority(kind, sourceRef, digestChar string) companyops.AuthoritySnapshot {
	return companyops.AuthoritySnapshot{
		Kind:          kind,
		SourceRef:     sourceRef,
		Revision:      "revision-1",
		ContentDigest: "sha256:" + strings.Repeat(digestChar, 64),
		Freshness:     "current",
	}
}

func authorityResolverFixture() (
	pgtype.UUID,
	companyops.HiveCosmAuthorityLookup,
	companyops.HiveCosmAuthorityBundle,
	db.Agent,
) {
	workspaceID := util.MustParseUUID(authorityResolverWorkspaceID)
	agentID := util.MustParseUUID(authorityResolverAgentID)
	employeeRef := "hivecosm://employees/EMP-P2-001"
	lookup := companyops.HiveCosmAuthorityLookup{
		WorkOrderSourceRef: "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001",
		EmployeeID:         "EMP-P2-001",
		IdentityBindingID:  "BIND-P2-001",
		AgentID:            authorityResolverAgentID,
	}
	bundle := companyops.HiveCosmAuthorityBundle{
		WorkOrder: resolverAuthority("WorkOrder", lookup.WorkOrderSourceRef, "a"),
		Employee:  resolverAuthority("Employee", employeeRef, "b"),
		IdentityBinding: companyops.IdentityBinding{
			Authority:   resolverAuthority("IdentityBinding", "hivecosm://identity-bindings/BIND-P2-001", "c"),
			EmployeeRef: employeeRef,
			AgentRef:    "/api/agents/" + authorityResolverAgentID,
			Active:      true,
		},
		RequestedAgentID: authorityResolverAgentID,
	}
	agent := db.Agent{
		ID:                 agentID,
		WorkspaceID:        workspaceID,
		Name:               "Atlas",
		RuntimeID:          util.MustParseUUID("33333333-3333-4333-8333-333333333333"),
		RuntimeMode:        "local",
		Status:             "idle",
		MaxConcurrentTasks: 4,
		PermissionMode:     "workspace",
		Kind:               "worker",
		UpdatedAt:          pgtype.Timestamptz{Time: time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC), Valid: true},
	}
	return workspaceID, lookup, bundle, agent
}

func TestCompanyOpsAuthorityResolverJoinsExactCompanyAndLocalAgentAuthority(t *testing.T) {
	workspaceID, lookup, bundle, agent := authorityResolverFixture()
	resolver := NewCompanyOpsAuthorityResolver(
		stubCompanyOpsHiveCosmAuthorityReader{bundle: bundle},
		stubCompanyOpsAgentAuthorityReader{agent: agent},
	)

	resolved, err := resolver.Resolve(context.Background(), workspaceID, lookup)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.AgentAuthority.SourceRef != "/api/agents/"+authorityResolverAgentID {
		t.Fatalf("Agent source_ref = %q", resolved.AgentAuthority.SourceRef)
	}

	req := resolved.AssignmentRequest(
		util.MustParseUUID("44444444-4444-4444-8444-444444444444"),
		workspaceID,
		util.MustParseUUID("55555555-5555-4555-8555-555555555555"),
		util.MustParseUUID("66666666-6666-4666-8666-666666666666"),
		"Produce the exact P2 execution receipt.",
	)
	target, err := companyops.ValidateAndFreezeExecutionTarget(
		req.WorkOrder,
		req.InputDigest,
		req.Employee,
		req.Bindings,
		req.Agents,
	)
	if err != nil {
		t.Fatalf("joined authority did not freeze: %v", err)
	}
	if target.AgentRef != resolved.IdentityBinding.AgentRef || req.LocalAgentSourceRef != target.AgentRef {
		t.Fatalf("frozen target does not preserve exact local Agent ref: %+v", target)
	}
}

func TestCompanyOpsAuthorityResolverFailsClosed(t *testing.T) {
	workspaceID, lookup, bundle, agent := authorityResolverFixture()
	tests := []struct {
		name      string
		workspace pgtype.UUID
		reader    CompanyOpsHiveCosmAuthorityReader
		agents    CompanyOpsAgentAuthorityReader
	}{
		{
			name:      "upstream source gap",
			workspace: workspaceID,
			reader: stubCompanyOpsHiveCosmAuthorityReader{
				err: &companyops.HiveCosmAuthorityError{Kind: companyops.HiveCosmAuthoritySourceGap},
			},
			agents: stubCompanyOpsAgentAuthorityReader{agent: agent},
		},
		{
			name:      "local Agent missing",
			workspace: workspaceID,
			reader:    stubCompanyOpsHiveCosmAuthorityReader{bundle: bundle},
			agents:    stubCompanyOpsAgentAuthorityReader{err: errors.New("not found")},
		},
		{
			name:      "local Agent belongs to another workspace",
			workspace: workspaceID,
			reader:    stubCompanyOpsHiveCosmAuthorityReader{bundle: bundle},
			agents: stubCompanyOpsAgentAuthorityReader{agent: func() db.Agent {
				agent.WorkspaceID = util.MustParseUUID("66666666-6666-4666-8666-666666666666")
				return agent
			}()},
		},
		{
			name:      "binding targets another local Agent",
			workspace: workspaceID,
			reader: stubCompanyOpsHiveCosmAuthorityReader{bundle: func() companyops.HiveCosmAuthorityBundle {
				bundle.IdentityBinding.AgentRef = "/api/agents/77777777-7777-4777-8777-777777777777"
				return bundle
			}()},
			agents: stubCompanyOpsAgentAuthorityReader{agent: agent},
		},
		{
			name:      "missing resolver",
			workspace: workspaceID,
			reader:    nil,
			agents:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := NewCompanyOpsAuthorityResolver(tt.reader, tt.agents)
			_, err := resolver.Resolve(context.Background(), tt.workspace, lookup)
			if err == nil {
				t.Fatal("Resolve unexpectedly succeeded")
			}
		})
	}
}
