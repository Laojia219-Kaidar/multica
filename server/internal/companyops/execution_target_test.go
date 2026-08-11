package companyops

import (
	"strings"
	"testing"
)

const (
	workOrderRef = "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-EXECUTION-TARGET-001"
	employeeRef  = "hivecosm://employees/EMP-EXECUTION-TARGET-001"
	bindingRef   = "hivecosm://identity-bindings/BIND-EXECUTION-TARGET-001"
	agentRef     = "/api/agents/22222222-2222-4222-8222-222222222222"
)

type executionTargetFixture struct {
	workOrder   AuthoritySnapshot
	inputDigest string
	employee    AuthoritySnapshot
	bindings    []IdentityBinding
	agents      []AuthoritySnapshot
}

func sha256Of(ch string) string {
	return "sha256:" + strings.Repeat(ch, 64)
}

func currentAuthority(kind, sourceRef, revision, digestChar, displayName, model string) AuthoritySnapshot {
	return AuthoritySnapshot{
		Kind:          kind,
		SourceRef:     sourceRef,
		Revision:      revision,
		ContentDigest: sha256Of(digestChar),
		Freshness:     "current",
		DisplayName:   displayName,
		Model:         model,
	}
}

func baseExecutionTargetFixture() executionTargetFixture {
	return executionTargetFixture{
		workOrder: currentAuthority(
			"WorkOrder",
			workOrderRef,
			"wo-rev-17",
			"a",
			"Launch the governed execution target",
			"",
		),
		inputDigest: sha256Of("b"),
		employee: currentAuthority(
			"Employee",
			employeeRef,
			"employee-rev-9",
			"c",
			"Platform Engineer",
			"",
		),
		bindings: []IdentityBinding{
			{
				// Historical bindings may remain readable, but only the single active
				// Employee -> Agent edge participates in target resolution.
				Authority: currentAuthority(
					"IdentityBinding",
					"hivecosm://identity-bindings/BIND-HISTORICAL-001",
					"binding-rev-3",
					"d",
					"Platform Engineer",
					"gpt-5.6",
				),
				EmployeeRef: employeeRef,
				AgentRef:    "/api/agents/11111111-1111-4111-8111-111111111111",
				Active:      false,
			},
			{
				Authority: currentAuthority(
					"IdentityBinding",
					bindingRef,
					"binding-rev-12",
					"e",
					"Platform Engineer",
					"gpt-5.6",
				),
				EmployeeRef: employeeRef,
				AgentRef:    agentRef,
				Active:      true,
			},
		},
		agents: []AuthoritySnapshot{
			// This decoy is intentionally first and has the same display/model
			// hints as the exact target. A resolver that falls back to first item,
			// display name, or model will select the wrong execution carrier.
			currentAuthority(
				"Agent",
				"/api/agents/33333333-3333-4333-8333-333333333333",
				"agent-rev-4",
				"f",
				"Platform Engineer",
				"gpt-5.6",
			),
			currentAuthority(
				"Agent",
				agentRef,
				"agent-rev-21",
				"1",
				"Platform Engineer",
				"gpt-5.6",
			),
		},
	}
}

func (f executionTargetFixture) clone() executionTargetFixture {
	f.bindings = append([]IdentityBinding(nil), f.bindings...)
	f.agents = append([]AuthoritySnapshot(nil), f.agents...)
	return f
}

func (f executionTargetFixture) freeze() (ExecutionTargetSnapshot, error) {
	return ValidateAndFreezeExecutionTarget(
		f.workOrder,
		f.inputDigest,
		f.employee,
		f.bindings,
		f.agents,
	)
}

func activeBindingIndex(bindings []IdentityBinding) int {
	for i := range bindings {
		if bindings[i].Active {
			return i
		}
	}
	return -1
}

func exactAgentIndex(agents []AuthoritySnapshot) int {
	for i := range agents {
		if agents[i].SourceRef == agentRef {
			return i
		}
	}
	return -1
}

func TestExecutionTarget_ExactAuthorityChainIsFrozen(t *testing.T) {
	f := baseExecutionTargetFixture()

	got, err := f.freeze()
	if err != nil {
		t.Fatalf("ValidateAndFreezeExecutionTarget returned error for exact current chain: %v", err)
	}

	if got.WorkOrderRef != workOrderRef {
		t.Fatalf("work order ref = %q, want %q", got.WorkOrderRef, workOrderRef)
	}
	if got.WorkOrderRevision != "wo-rev-17" {
		t.Fatalf("work order revision = %q, want frozen revision wo-rev-17", got.WorkOrderRevision)
	}
	if got.WorkOrderDigest != sha256Of("a") {
		t.Fatalf("work order digest = %q, want %q", got.WorkOrderDigest, sha256Of("a"))
	}
	if got.InputDigest != sha256Of("b") {
		t.Fatalf("input digest = %q, want %q", got.InputDigest, sha256Of("b"))
	}
	if got.EmployeeRef != employeeRef {
		t.Fatalf("employee ref = %q, want %q", got.EmployeeRef, employeeRef)
	}
	if got.EmployeeRevision != "employee-rev-9" || got.EmployeeDigest != sha256Of("c") {
		t.Fatalf("employee snapshot was not frozen: revision=%q digest=%q", got.EmployeeRevision, got.EmployeeDigest)
	}
	if got.BindingRef != bindingRef {
		t.Fatalf("binding ref = %q, want exact active binding %q", got.BindingRef, bindingRef)
	}
	if got.BindingRevision != "binding-rev-12" || got.BindingDigest != sha256Of("e") {
		t.Fatalf("binding snapshot was not frozen: revision=%q digest=%q", got.BindingRevision, got.BindingDigest)
	}
	if got.AgentRef != agentRef {
		t.Fatalf("agent ref = %q, want exact binding target %q; display/model/first-item fallback is forbidden", got.AgentRef, agentRef)
	}
	if got.AgentRevision != "agent-rev-21" || got.AgentDigest != sha256Of("1") {
		t.Fatalf("agent snapshot was not frozen: revision=%q digest=%q", got.AgentRevision, got.AgentDigest)
	}

	// The returned execution target is a value snapshot. Later mutation of the
	// observed authority inputs must not rewrite the WorkOrder/input evidence that
	// the Run will carry.
	f.workOrder.Revision = "wo-rev-mutated"
	f.workOrder.ContentDigest = sha256Of("9")
	f.inputDigest = sha256Of("8")
	if got.WorkOrderRevision != "wo-rev-17" || got.WorkOrderDigest != sha256Of("a") || got.InputDigest != sha256Of("b") {
		t.Fatalf("frozen WorkOrder/input evidence changed after input mutation: %+v", got)
	}
}

func TestExecutionTarget_FailsClosedOnInvalidAuthorityOrBinding(t *testing.T) {
	base := baseExecutionTargetFixture()

	tests := []struct {
		name   string
		mutate func(*executionTargetFixture)
	}{
		{
			name: "missing work order source ref",
			mutate: func(f *executionTargetFixture) {
				f.workOrder.SourceRef = ""
			},
		},
		{
			name: "display label is not a source ref",
			mutate: func(f *executionTargetFixture) {
				f.workOrder.SourceRef = f.workOrder.DisplayName
			},
		},
		{
			name: "legacy WorkOrder source ref",
			mutate: func(f *executionTargetFixture) {
				f.workOrder.SourceRef = "hivecosm://work-orders/WO-EXECUTION-TARGET-001"
			},
		},
		{
			name: "missing authority revision",
			mutate: func(f *executionTargetFixture) {
				f.workOrder.Revision = ""
			},
		},
		{
			name: "malformed authority sha256",
			mutate: func(f *executionTargetFixture) {
				f.workOrder.ContentDigest = "sha256:not-a-digest"
			},
		},
		{
			name: "stale work order authority",
			mutate: func(f *executionTargetFixture) {
				f.workOrder.Freshness = "stale"
			},
		},
		{
			name: "wrong employee authority kind",
			mutate: func(f *executionTargetFixture) {
				f.employee.Kind = "Agent"
			},
		},
		{
			name: "stale employee authority",
			mutate: func(f *executionTargetFixture) {
				f.employee.Freshness = "stale"
			},
		},
		{
			name: "missing active binding",
			mutate: func(f *executionTargetFixture) {
				f.bindings[activeBindingIndex(f.bindings)].Active = false
			},
		},
		{
			name: "incomplete active binding",
			mutate: func(f *executionTargetFixture) {
				f.bindings[activeBindingIndex(f.bindings)].AgentRef = ""
			},
		},
		{
			name: "stale active binding authority",
			mutate: func(f *executionTargetFixture) {
				f.bindings[activeBindingIndex(f.bindings)].Authority.Freshness = "stale"
			},
		},
		{
			name: "binding belongs to a different employee",
			mutate: func(f *executionTargetFixture) {
				f.bindings[activeBindingIndex(f.bindings)].EmployeeRef = "hivecosm://employees/EMP-OTHER-001"
			},
		},
		{
			name: "duplicate active employee binding",
			mutate: func(f *executionTargetFixture) {
				otherAgentRef := "/api/agents/44444444-4444-4444-8444-444444444444"
				f.bindings = append(f.bindings, IdentityBinding{
					Authority:   currentAuthority("IdentityBinding", "hivecosm://identity-bindings/BIND-SECOND-001", "binding-rev-1", "2", "Platform Engineer", "gpt-5.6"),
					EmployeeRef: employeeRef,
					AgentRef:    otherAgentRef,
					Active:      true,
				})
				f.agents = append(f.agents, currentAuthority("Agent", otherAgentRef, "agent-rev-1", "3", "Platform Engineer", "gpt-5.6"))
			},
		},
		{
			name: "duplicate active agent binding",
			mutate: func(f *executionTargetFixture) {
				f.bindings = append(f.bindings, IdentityBinding{
					Authority:   currentAuthority("IdentityBinding", "hivecosm://identity-bindings/BIND-OTHER-EMPLOYEE-001", "binding-rev-1", "4", "Platform Engineer", "gpt-5.6"),
					EmployeeRef: "hivecosm://employees/EMP-OTHER-001",
					AgentRef:    agentRef,
					Active:      true,
				})
			},
		},
		{
			name: "duplicate agent authority snapshot",
			mutate: func(f *executionTargetFixture) {
				f.agents = append(f.agents, currentAuthority("Agent", agentRef, "agent-rev-22", "5", "Platform Engineer", "gpt-5.6"))
			},
		},
		{
			name: "missing exact agent never falls back to display model or first item",
			mutate: func(f *executionTargetFixture) {
				f.bindings[activeBindingIndex(f.bindings)].AgentRef = "/api/agents/55555555-5555-4555-8555-555555555555"
				// Both available candidates retain the same display/model hints. Neither
				// is the exact AgentRef named by the active binding.
			},
		},
		{
			name: "stale exact agent authority",
			mutate: func(f *executionTargetFixture) {
				f.agents[exactAgentIndex(f.agents)].Freshness = "stale"
			},
		},
		{
			name: "wrong exact agent authority kind",
			mutate: func(f *executionTargetFixture) {
				f.agents[exactAgentIndex(f.agents)].Kind = "Employee"
			},
		},
		{
			name: "missing input digest",
			mutate: func(f *executionTargetFixture) {
				f.inputDigest = ""
			},
		},
		{
			name: "malformed input sha256",
			mutate: func(f *executionTargetFixture) {
				f.inputDigest = "sha256:short"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := base.clone()
			tt.mutate(&f)

			if got, err := f.freeze(); err == nil {
				t.Fatalf("ValidateAndFreezeExecutionTarget accepted invalid or ambiguous authority chain: %+v", got)
			}
		})
	}
}
