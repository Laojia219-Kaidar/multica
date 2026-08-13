package service

import (
	"context"
	"testing"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
)

func TestWorkforceBaseRuntimeJoinMapsOneEmployeeToOneAgentRuntimeBase(t *testing.T) {
	agentID := "11111111-2222-4333-8444-555555555555"
	agent := executableAgent()
	runtime := onlineRuntime()
	runtime.DeviceInfo = "HiveCosm Mac mini · 2.1.221 (Claude Code)"

	service := NewCompanyOpsDirectoryService(
		&stubDirectoryAdapter{
			employees: serviceEmployees(serviceSummary(companyopsapi.BindingStateUniqueActiveCandidate, &agentID)),
		},
		&stubAgentLookup{agent: agent, runtime: runtime},
	)

	authority, rows, err := service.GetWorkforceBaseRuntimeJoin(context.Background(), testWorkspaceUUID)
	if err != nil {
		t.Fatal(err)
	}
	if authority.ContentDigest != serviceDigest {
		t.Fatalf("authority digest = %q", authority.ContentDigest)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.EmployeeID != "DE-ALICE" ||
		row.WorkforceAgentID != "KT-ALICE" ||
		row.HiveCrewAgentID != agentID ||
		row.RuntimeID != "22222222-3333-4444-8555-666666666666" ||
		row.BaseMachineTitle != "HiveCosm Mac mini" ||
		row.AgentStatus != "idle" ||
		row.RuntimeStatus != "online" ||
		row.Model != "qwen3.7-plus" {
		t.Fatalf("unexpected join row: %#v", row)
	}
}

func TestWorkforceBaseRuntimeJoinLeavesUnavailableEmployeesEmpty(t *testing.T) {
	service := NewCompanyOpsDirectoryService(
		&stubDirectoryAdapter{
			employees: serviceEmployees(serviceSummary(companyopsapi.BindingStateNone, nil)),
		},
		&stubAgentLookup{},
	)

	_, rows, err := service.GetWorkforceBaseRuntimeJoin(context.Background(), testWorkspaceUUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.EmployeeID != "DE-ALICE" || row.WorkforceAgentID != "KT-ALICE" {
		t.Fatalf("unexpected join row: %#v", row)
	}
	if row.HiveCrewAgentID != "" || row.RuntimeID != "" || row.BaseMachineTitle != "" {
		t.Fatalf("unavailable employee leaked executable identity: %#v", row)
	}
}

func TestWorkforceBaseRuntimeJoinPropagatesMalformedSource(t *testing.T) {
	employees := serviceEmployees(serviceSummary(companyopsapi.BindingStateNone, nil))
	employees.Employees = nil
	service := NewCompanyOpsDirectoryService(
		&stubDirectoryAdapter{employees: employees},
		&stubAgentLookup{},
	)
	if _, _, err := service.GetWorkforceBaseRuntimeJoin(context.Background(), testWorkspaceUUID); err == nil {
		t.Fatal("expected malformed source error")
	}
}
