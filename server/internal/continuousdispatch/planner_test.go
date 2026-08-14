package continuousdispatch

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/readyfrontier"
	"github.com/multica-ai/multica/server/internal/routescore"
)

var fixtureNow = time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return fixtureNow }

func readyInput(candidates ...Candidate) Input {
	return Input{
		Frontier: readyfrontier.IssueInput{
			Status:        "todo",
			HasAssignee:   true,
			RuntimeBound:  true,
			RuntimeOnline: true,
			CapacityKnown: true,
			CapacityFree:  true,
		},
		Requirement: routescore.TaskRequirement{RequiredRoles: []string{"implementation"}},
		Candidates:  candidates,
		Generation:  GenerationEvidence{Known: true},
		Lease:       LeaseEvidence{Required: true, Known: true, Available: true, LeaseID: "lease-1"},
	}
}

func candidate(employee, agent, base string, quota routescore.QuotaState) Candidate {
	return Candidate{
		EmployeeID: employee,
		Model:      "fixture-model",
		AccountRef: "fixture-account",
		BaseID:     base,
		BaseKnown:  true,
		Route: routescore.Candidate{
			AgentID:        uuid.MustParse(agent),
			AgentName:      employee,
			Roles:          []string{"implementation"},
			RuntimeID:      uuid.NewSHA1(uuid.NameSpaceOID, []byte(agent)),
			RuntimeHealth:  routescore.RuntimeOnline,
			Quota:          quota,
			QuotaCheckedAt: fixtureNow,
			SuccessCount:   9,
			FailureCount:   1,
			AvgLatencyMs:   100,
			CostPerTaskUSD: 0.01,
		},
	}
}

func planner() *Planner {
	scorer := routescore.NewScorer(nil).WithClock(fixedClock{})
	return NewPlanner().WithScorer(scorer)
}

func TestPlanReadySelectsDeterministicCandidate(t *testing.T) {
	a := candidate("emp-a", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh)
	b := candidate("emp-b", "00000000-0000-0000-0000-000000000002", "base-a", routescore.QuotaFresh)
	in := readyInput(b, a)
	in.RequiredBaseID = "base-a"

	got := planner().Plan(in)
	if got.State != StateReady || got.Selected == nil || got.Selected.EmployeeID != "emp-a" {
		t.Fatalf("plan = %+v, want deterministic emp-a ready", got)
	}
	if got.WriteLeaseID != "lease-1" {
		t.Fatalf("lease = %q, want lease-1", got.WriteLeaseID)
	}
}

func TestPlanWrongBaseSelectsFallback(t *testing.T) {
	wrong := candidate("preferred", "00000000-0000-0000-0000-000000000001", "wrong-base", routescore.QuotaFresh)
	fallback := candidate("fallback", "00000000-0000-0000-0000-000000000002", "required-base", routescore.QuotaFresh)
	in := readyInput(wrong, fallback)
	in.RequiredBaseID = "required-base"
	in.PreferredEmployeeID = "preferred"

	got := planner().Plan(in)
	if got.State != StateFallback || got.Selected == nil || got.Selected.EmployeeID != "fallback" {
		t.Fatalf("plan = %+v, want fallback employee", got)
	}
	if len(got.Candidates) != 2 || got.Candidates[1].Reasons[0] != ReasonRuntimeBaseMismatch {
		t.Fatalf("candidate explanations = %+v, want runtime_base_mismatch", got.Candidates)
	}
}

func TestPlanQuotaExhaustedSelectsFallback(t *testing.T) {
	exhausted := candidate("preferred", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaExhausted)
	fallback := candidate("fallback", "00000000-0000-0000-0000-000000000002", "base-a", routescore.QuotaFresh)
	in := readyInput(exhausted, fallback)
	in.RequiredBaseID = "base-a"
	in.PreferredEmployeeID = "preferred"

	got := planner().Plan(in)
	if got.State != StateFallback || got.Selected == nil || got.Selected.EmployeeID != "fallback" {
		t.Fatalf("plan = %+v, want quota fallback", got)
	}
	if got.Candidates[1].Reasons[0] != Reason("quota_exhausted") {
		t.Fatalf("candidate explanations = %+v, want quota_exhausted", got.Candidates)
	}
}

func TestPlanDuplicateGenerationFailsClosed(t *testing.T) {
	in := readyInput(candidate("emp", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh))
	in.Generation.DuplicateUnattributed = true

	got := planner().Plan(in)
	if got.State != StateBlocked || len(got.Reasons) != 1 || got.Reasons[0] != ReasonDuplicateUnattributed {
		t.Fatalf("plan = %+v, want duplicate_generation_unattributed", got)
	}
}

func TestPlanExistingGenerationReturnsReceipt(t *testing.T) {
	in := readyInput(candidate("emp", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh))
	in.Generation.OpenTaskID = "task-existing"

	got := planner().Plan(in)
	if got.State != StateAlreadyDispatched || got.ExistingTaskID != "task-existing" {
		t.Fatalf("plan = %+v, want existing task receipt", got)
	}
}

func TestPlanMissingEvidenceAndLeaseFailClosed(t *testing.T) {
	in := readyInput(candidate("emp", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh))
	in.Generation.Known = false
	if got := planner().Plan(in); got.State != StateBlocked || got.Reasons[0] != ReasonGenerationEvidenceMissing {
		t.Fatalf("generation plan = %+v", got)
	}

	in.Generation.Known = true
	in.Lease.Known = false
	if got := planner().Plan(in); got.State != StateBlocked || got.Reasons[0] != ReasonLeaseEvidenceMissing {
		t.Fatalf("lease evidence plan = %+v", got)
	}

	in.Lease.Known = true
	in.Lease.Available = false
	if got := planner().Plan(in); got.State != StateWaiting || got.Reasons[0] != ReasonLeaseUnavailable {
		t.Fatalf("lease availability plan = %+v", got)
	}
}

func TestPlanFrontierStateDominates(t *testing.T) {
	in := readyInput(candidate("emp", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh))
	in.Frontier.HasTask = true
	in.Frontier.TaskStatus = "running"

	got := planner().Plan(in)
	if got.State != StateRunning || got.Reasons[0] != Reason(readyfrontier.ReasonRunning) {
		t.Fatalf("plan = %+v, want running frontier", got)
	}
}

func TestPlanDuplicateCandidateIdentityFailsClosed(t *testing.T) {
	a := candidate("emp-a", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh)
	b := candidate("emp-b", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh)
	in := readyInput(a, b)

	got := planner().Plan(in)
	if got.State != StateBlocked || got.Reasons[0] != ReasonCandidateIdentityDuplicate {
		t.Fatalf("plan = %+v, want duplicate identity block", got)
	}
}
