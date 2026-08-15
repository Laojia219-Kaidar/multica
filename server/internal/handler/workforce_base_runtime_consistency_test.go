package handler

import (
	"context"
	"testing"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// employeeRuntimeLookup resolves the single available employee to an agent on
// a runtime whose device_info carries the observed base machine title.
type employeeRuntimeLookup struct {
	agent   db.Agent
	runtime db.AgentRuntime
}

func (l employeeRuntimeLookup) GetAgentInWorkspace(context.Context, db.GetAgentInWorkspaceParams) (db.Agent, error) {
	return l.agent, nil
}

func (l employeeRuntimeLookup) GetAgentRuntimeForWorkspace(context.Context, db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error) {
	return l.runtime, nil
}

// TestWorkforceBaseRuntimeJoinMatchesBasesProjection proves the two Lane C
// read surfaces (organization roster join + bases overview) agree on the
// Employee -> Agent -> Runtime -> Base mapping without deriving identity
// twice. The join row's runtime/base must be found verbatim in the bases
// projection, and the base must count that agent as a resident employee.
func TestWorkforceBaseRuntimeJoinMatchesBasesProjection(t *testing.T) {
	workspace := "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	runtime := baseRuntime("22222222-3333-4444-8555-666666666666", workspace, "mini-a", "online", "HiveCosm Mac mini · 2.1.221", "daemon-mini")
	agent := baseAgent("11111111-2222-4333-8444-555555555555", workspace, "22222222-3333-4444-8555-666666666666", "working")

	directory := service.NewCompanyOpsDirectoryService(
		&handlerAdapter{employees: &companyopsapi.AdapterEmployeesResponse{
			SchemaVersion: companyopsapi.HiveCrewEmployeesSchema,
			OK:            true,
			TenantID:      "tenant",
			WorkspaceID:   workspace,
			Authority:     handlerAuthority(),
			Employees:     []companyopsapi.AdapterEmployeeSummary{handlerSummary()},
		}},
		employeeRuntimeLookup{agent: agent, runtime: runtime},
	)

	_, joinRows, err := directory.GetWorkforceBaseRuntimeJoin(context.Background(), handlerWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(joinRows) != 1 {
		t.Fatalf("join rows = %d, want 1", len(joinRows))
	}
	join := joinRows[0]
	if join.HiveCrewAgentID != "11111111-2222-4333-8444-555555555555" ||
		join.RuntimeID != "22222222-3333-4444-8555-666666666666" ||
		join.BaseMachineTitle != "HiveCosm Mac mini" {
		t.Fatalf("unexpected join row: %#v", join)
	}

	bases := buildBaseOverviews([]db.AgentRuntime{runtime}, []db.Agent{agent})
	if len(bases) != 1 {
		t.Fatalf("bases = %d, want 1", len(bases))
	}
	base := bases[0]
	if base.MachineTitle != join.BaseMachineTitle {
		t.Fatalf("base machine %q != join base %q", base.MachineTitle, join.BaseMachineTitle)
	}
	if base.Employees != 1 || base.LoadRunning != 1 || base.RuntimeOnline != 1 {
		t.Fatalf("base did not reflect joined employee/load: %#v", base)
	}
	found := false
	for _, runtimeInfo := range base.Runtimes {
		if runtimeInfo.RuntimeID == join.RuntimeID {
			found = true
		}
	}
	if !found {
		t.Fatalf("joined runtime %s missing from base %s", join.RuntimeID, base.MachineTitle)
	}
}
