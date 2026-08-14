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
