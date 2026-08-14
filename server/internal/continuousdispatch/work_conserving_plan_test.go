package continuousdispatch

import (
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/readyfrontier"
	"github.com/multica-ai/multica/server/internal/routescore"
)

const workGoal = "goal-work-conserving"

func workIssue(id string) WorkConservingIssue {
	return WorkConservingIssue{
		ID: id, GoalID: workGoal,
		Frontier: readyfrontier.IssueInput{
			Status: "todo", HasAssignee: true, RuntimeBound: true,
			RuntimeOnline: true, CapacityKnown: true, CapacityFree: true,
		},
		Requirement:       routescore.TaskRequirement{RequiredRoles: []string{"implementation"}},
		Generation:        GenerationEvidence{Known: true},
		WIP:               WIPTruthEvidence{Required: true, Known: true, Reconciled: true, ProjectionAvailable: true},
		Lease:             LeaseEvidence{Required: true, Known: true, Available: true, LeaseID: "lease-" + id},
		ReviewAuthorKnown: true, ProvenanceKnown: true, AuthorityKnown: true,
		WritePath: WorkConservingWritePath{Known: true, Key: "worktree-" + id},
	}
}

func workEmployee(name, agent string, health bool, idle bool) WorkConservingEmployee {
	return WorkConservingEmployee{
		Candidate: Candidate{
			EmployeeID: name, Model: "model-" + name, AccountRef: "account-" + name,
			BaseID: "base-a", BaseKnown: true, WIPKnown: true, MaxWIP: 1,
			Route: routescore.Candidate{
				AgentID: uuid.MustParse(agent), AgentName: name, Roles: []string{"implementation"},
				RuntimeID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(agent)), RuntimeHealth: routescore.RuntimeOnline,
				Quota: routescore.QuotaFresh, QuotaCheckedAt: fixtureNow,
				SuccessCount: 10, FailureCount: 1, AvgLatencyMs: 100, CostPerTaskUSD: .01,
			},
		},
		HealthyKnown: true, Healthy: health, IdleKnown: true, Idle: idle,
		ProvenanceKnown: true, AuthorityKnown: true,
		WritePath: WorkConservingWritePath{Known: true, Key: ""},
	}
}

func TestPlanWorkConservingRoutesAroundUnavailablePreferred(t *testing.T) {
	issue := workIssue("issue-a")
	issue.PreferredEmployeeID = "offline"
	issue.WritePath.Key = ""
	offline := workEmployee("offline", "00000000-0000-0000-0000-000000000011", false, false)
	backup := workEmployee("backup", "00000000-0000-0000-0000-000000000012", true, true)
	got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{issue}, Employees: []WorkConservingEmployee{offline, backup}})
	if len(got.Suggestions) != 1 || got.Suggestions[0].EmployeeID != "backup" {
		t.Fatalf("suggestions = %+v, want backup fallback", got.Suggestions)
	}
	if got.Suggestions[0].FallbackReason != ReasonPreferredUnavailable {
		t.Fatalf("fallback reason = %q, want preferred unavailable", got.Suggestions[0].FallbackReason)
	}
	if got.Mismatch.PlannedIssues != 1 || len(got.BlockedBacklog) != 0 {
		t.Fatalf("mismatch/blocked = %+v/%+v", got.Mismatch, got.BlockedBacklog)
	}
}

func TestPlanWorkConservingBlocksActiveWritePath(t *testing.T) {
	issue := workIssue("issue-a")
	employee := workEmployee("idle", "00000000-0000-0000-0000-000000000013", true, true)
	got := planner().PlanWorkConserving(WorkConservingInput{
		GoalID: workGoal, Issues: []WorkConservingIssue{issue}, Employees: []WorkConservingEmployee{employee},
		ActiveLocks: []WorkConservingWriteLock{{Key: issue.WritePath.Key, IssueID: "other-issue", Owner: "other", Active: true}},
	})
	if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 1 {
		t.Fatalf("suggestions/blocked = %+v/%+v", got.Suggestions, got.BlockedBacklog)
	}
	if got.BlockedBacklog[0].Reasons[0] != ReasonIssueWritePathConflict {
		t.Fatalf("reasons = %+v, want write path conflict", got.BlockedBacklog[0].Reasons)
	}
	if got.Mismatch.ExecutableBacklog != 0 {
		t.Fatalf("executable backlog = %d, want 0 for locked issue", got.Mismatch.ExecutableBacklog)
	}
}

func TestPlanWorkConservingIsOneToOneAndDeterministic(t *testing.T) {
	a := workIssue("issue-a")
	a.WritePath.Key = ""
	b := workIssue("issue-b")
	b.WritePath.Key = ""
	employees := []WorkConservingEmployee{
		workEmployee("emp-b", "00000000-0000-0000-0000-000000000022", true, true),
		workEmployee("emp-a", "00000000-0000-0000-0000-000000000021", true, true),
	}
	in := WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{b, a}, Employees: employees}
	got := planner().PlanWorkConserving(in)
	if len(got.Suggestions) != 2 {
		t.Fatalf("suggestions = %+v, want two", got.Suggestions)
	}
	if got.Suggestions[0].IssueID != "issue-a" || got.Suggestions[1].IssueID != "issue-b" {
		t.Fatalf("issue ordering = %+v, want issue-a then issue-b", got.Suggestions)
	}
	if got.Suggestions[0].EmployeeID == got.Suggestions[1].EmployeeID {
		t.Fatalf("employee reused: %+v", got.Suggestions)
	}
	repeat := planner().PlanWorkConserving(in)
	if repeat.Suggestions[0] != got.Suggestions[0] || repeat.Suggestions[1] != got.Suggestions[1] {
		t.Fatalf("non-deterministic suggestions: first=%+v repeat=%+v", got.Suggestions, repeat.Suggestions)
	}
}

func TestPlanWorkConservingAllBlockedDoesNotInventAvailability(t *testing.T) {
	missingAuthority := workIssue("issue-a")
	missingAuthority.WritePath.Key = ""
	missingAuthority.AuthorityKnown = false
	offline := workEmployee("offline", "00000000-0000-0000-0000-000000000031", false, false)
	got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{missingAuthority}, Employees: []WorkConservingEmployee{offline}})
	if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 1 {
		t.Fatalf("suggestions/blocked = %+v/%+v", got.Suggestions, got.BlockedBacklog)
	}
	if got.Mismatch.ExecutableBacklog != 0 || got.Mismatch.HealthyIdleEmployees != 0 {
		t.Fatalf("mismatch = %+v, must not count blocked/offline as available work", got.Mismatch)
	}
	if got.BlockedBacklog[0].Receiver != "authority-operator" {
		t.Fatalf("receiver = %q, want authority-operator", got.BlockedBacklog[0].Receiver)
	}
}

func TestPlanWorkConservingConsumesIssueGenerationWIPLeaseAndBaseEvidence(t *testing.T) {
	employee := workEmployee("worker", "00000000-0000-0000-0000-000000000041", true, true)
	tests := []struct {
		name   string
		mutate func(*WorkConservingIssue)
		want   Reason
	}{
		{"generation", func(issue *WorkConservingIssue) { issue.Generation.Known = false }, ReasonGenerationEvidenceMissing},
		{"wip", func(issue *WorkConservingIssue) { issue.WIP.Known = false }, ReasonWIPTruthMissing},
		{"lease", func(issue *WorkConservingIssue) { issue.Lease.Known = false }, ReasonLeaseEvidenceMissing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issue := workIssue("issue-" + tc.name)
			issue.WritePath.Key = ""
			tc.mutate(&issue)
			got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{issue}, Employees: []WorkConservingEmployee{employee}})
			if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 1 || got.BlockedBacklog[0].Reasons[0] != tc.want {
				t.Fatalf("plan = %+v, want blocked %q", got, tc.want)
			}
		})
	}

	baseIssue := workIssue("issue-base")
	baseIssue.RequiredBaseID = "base-required"
	baseIssue.WritePath.Key = ""
	got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{baseIssue}, Employees: []WorkConservingEmployee{employee}})
	if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 1 || got.Mismatch.ExecutableBacklog != 1 {
		t.Fatalf("required base plan = %+v, want no candidate but executable backlog", got)
	}
}

func TestPlanWorkConservingRejectsUnknownOrExhaustedEmployeeWIP(t *testing.T) {
	issue := workIssue("issue-a")
	issue.WritePath.Key = ""
	tests := []struct {
		name   string
		mutate func(*WorkConservingEmployee)
	}{
		{"unknown", func(employee *WorkConservingEmployee) { employee.Candidate.WIPKnown = false }},
		{"zero_max", func(employee *WorkConservingEmployee) { employee.Candidate.MaxWIP = 0 }},
		{"full", func(employee *WorkConservingEmployee) { employee.Candidate.ActiveWIP = 1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agentIDs := map[string]string{
				"unknown":  "00000000-0000-0000-0000-000000000051",
				"zero_max": "00000000-0000-0000-0000-000000000052",
				"full":     "00000000-0000-0000-0000-000000000053",
			}
			employee := workEmployee("worker", agentIDs[tc.name], true, true)
			tc.mutate(&employee)
			got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{issue}, Employees: []WorkConservingEmployee{employee}})
			if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 1 {
				t.Fatalf("plan = %+v, want blocked", got)
			}
		})
	}
}

func TestPlanWorkConservingRejectsDuplicateIssues(t *testing.T) {
	a := workIssue("issue-duplicate")
	a.WritePath.Key = ""
	b := a
	employee := workEmployee("worker", "00000000-0000-0000-0000-000000000061", true, true)
	got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{a, b}, Employees: []WorkConservingEmployee{employee}})
	if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 2 || got.Mismatch.OpenIssues != 2 || got.Mismatch.BlockedBacklog != 2 {
		t.Fatalf("duplicate issue plan = %+v, want both duplicate rows blocked", got)
	}
	for _, blocked := range got.BlockedBacklog {
		if blocked.Reasons[0] != ReasonIssueIdentityDuplicate {
			t.Fatalf("blocked = %+v, want duplicate reason", blocked)
		}
	}
}

func TestPlanWorkConservingEmployeePathCollisionWhenIssuePathIsEmpty(t *testing.T) {
	a := workIssue("issue-a")
	a.WritePath.Key = ""
	b := workIssue("issue-b")
	b.WritePath.Key = ""
	employeeA := workEmployee("worker-a", "00000000-0000-0000-0000-000000000071", true, true)
	employeeA.WritePath.Key = "shared-worktree"
	employeeB := workEmployee("worker-b", "00000000-0000-0000-0000-000000000072", true, true)
	employeeB.WritePath.Key = "shared-worktree"
	got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{b, a}, Employees: []WorkConservingEmployee{employeeB, employeeA}})
	if len(got.Suggestions) != 1 || got.Suggestions[0].IssueID != "issue-a" || len(got.BlockedBacklog) != 1 {
		t.Fatalf("path collision plan = %+v, want one deterministic suggestion", got)
	}
	if got.BlockedBacklog[0].Reasons[0] != ReasonWorkPathAlreadyPlanned {
		t.Fatalf("blocked = %+v, want work path already planned", got.BlockedBacklog[0])
	}
}

func TestPlanWorkConservingGoalMissingMetrics(t *testing.T) {
	issue := workIssue("issue-a")
	issue.GoalID = ""
	issue.WritePath.Key = ""
	employee := workEmployee("worker", "00000000-0000-0000-0000-000000000081", true, true)
	got := planner().PlanWorkConserving(WorkConservingInput{Issues: []WorkConservingIssue{issue}, Employees: []WorkConservingEmployee{employee}})
	if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 1 {
		t.Fatalf("goal missing plan = %+v, want one blocked issue", got)
	}
	if got.Mismatch.OpenIssues != 1 || got.Mismatch.BlockedBacklog != 1 || got.Mismatch.ExecutableBacklog != 0 {
		t.Fatalf("goal missing mismatch = %+v, want open=1 blocked=1 executable=0", got.Mismatch)
	}
}

func TestPlanWorkConservingRejectsEmptyRuntimeAndQuotaEvenWithCurrentObservation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkConservingEmployee)
		want   Reason
	}{
		{
			name: "runtime empty",
			mutate: func(employee *WorkConservingEmployee) {
				employee.Candidate.Route.RuntimeHealth = ""
			},
			want: ReasonEmployeeRuntimeUnavailable,
		},
		{
			name: "quota empty current timestamp",
			mutate: func(employee *WorkConservingEmployee) {
				employee.Candidate.Route.Quota = ""
				employee.Candidate.Route.QuotaCheckedAt = fixtureNow
			},
			want: ReasonEmployeeQuotaUnknown,
		},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issue := workIssue("issue-empty-evidence")
			issue.WritePath.Key = ""
			employee := workEmployee("worker", uuid.MustParse("00000000-0000-0000-0000-00000000009"+string(rune('0'+i+1))).String(), true, true)
			tc.mutate(&employee)
			got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{issue}, Employees: []WorkConservingEmployee{employee}})
			if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 1 || got.BlockedBacklog[0].Reasons[0] != tc.want {
				t.Fatalf("plan = %+v, want blocked reason %q", got, tc.want)
			}
			if got.Mismatch.HealthyIdleEmployees != 0 {
				t.Fatalf("healthy idle mismatch = %+v, must not count incomplete runtime/quota evidence", got.Mismatch)
			}
		})
	}
}

func TestPlanWorkConservingDuplicateEmployeeIDZeroesIdentityMismatch(t *testing.T) {
	issue := workIssue("issue-duplicate-employee")
	issue.WritePath.Key = ""
	a := workEmployee("same-employee", "00000000-0000-0000-0000-000000000091", true, true)
	b := workEmployee("same-employee", "00000000-0000-0000-0000-000000000092", true, true)
	got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{issue}, Employees: []WorkConservingEmployee{a, b}})
	if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 1 || got.Mismatch.HealthyIdleEmployees != 0 || got.Mismatch.UnmatchedHealthyIdleEmployees != 0 {
		t.Fatalf("duplicate employee plan = %+v, want blocked and zero identity metrics", got)
	}
}

func TestPlanWorkConservingDuplicateAgentIDZeroesIdentityMismatch(t *testing.T) {
	issue := workIssue("issue-duplicate-agent")
	issue.WritePath.Key = ""
	a := workEmployee("employee-a", "00000000-0000-0000-0000-000000000093", true, true)
	b := workEmployee("employee-b", "00000000-0000-0000-0000-000000000093", true, true)
	got := planner().PlanWorkConserving(WorkConservingInput{GoalID: workGoal, Issues: []WorkConservingIssue{issue}, Employees: []WorkConservingEmployee{a, b}})
	if len(got.Suggestions) != 0 || len(got.BlockedBacklog) != 1 || got.Mismatch.HealthyIdleEmployees != 0 || got.Mismatch.UnmatchedHealthyIdleEmployees != 0 {
		t.Fatalf("duplicate agent plan = %+v, want blocked and zero identity metrics", got)
	}
}
