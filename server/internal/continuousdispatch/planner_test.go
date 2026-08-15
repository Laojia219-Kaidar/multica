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
		WIP: WIPTruthEvidence{
			Required:            true,
			Known:               true,
			Reconciled:          true,
			ProjectionAvailable: true,
		},
		Lease: LeaseEvidence{Required: true, Known: true, Available: true, LeaseID: "lease-1"},
	}
}

func TestDispatchIdentityCompleteRequiresCanonicalTuple(t *testing.T) {
	complete := DispatchIdentity{
		WorkspaceID:       "workspace-1",
		IssueID:           "issue-1",
		Stage:             "implementation",
		CandidateRevision: "abc123",
		Generation:        "g-1",
	}
	if !complete.Complete() {
		t.Fatalf("complete identity = %+v, want true", complete)
	}

	tests := map[string]DispatchIdentity{
		"workspace":          {IssueID: "issue-1", Stage: "implementation", CandidateRevision: "abc123", Generation: "g-1"},
		"issue":              {WorkspaceID: "workspace-1", Stage: "implementation", CandidateRevision: "abc123", Generation: "g-1"},
		"stage":              {WorkspaceID: "workspace-1", IssueID: "issue-1", CandidateRevision: "abc123", Generation: "g-1"},
		"candidate revision": {WorkspaceID: "workspace-1", IssueID: "issue-1", Stage: "implementation", Generation: "g-1"},
		"generation":         {WorkspaceID: "workspace-1", IssueID: "issue-1", Stage: "implementation", CandidateRevision: "abc123"},
	}
	for name, identity := range tests {
		t.Run(name, func(t *testing.T) {
			if identity.Complete() {
				t.Fatalf("incomplete identity = %+v, want false", identity)
			}
		})
	}
}

func candidate(employee, agent, base string, quota routescore.QuotaState) Candidate {
	return Candidate{
		EmployeeID: employee,
		Model:      "fixture-model",
		AccountRef: "fixture-account",
		BaseID:     base,
		BaseKnown:  true,
		WIPKnown:   true,
		ActiveWIP:  0,
		MaxWIP:     1,
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

func TestPlanCandidateWIPFailsClosedAndSelectsSpareFallback(t *testing.T) {
	full := candidate("preferred", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh)
	full.ActiveWIP = 1
	spare := candidate("fallback", "00000000-0000-0000-0000-000000000002", "base-a", routescore.QuotaFresh)
	in := readyInput(full, spare)
	in.PreferredEmployeeID = "preferred"

	got := planner().Plan(in)
	if got.State != StateFallback || got.Selected == nil || got.Selected.EmployeeID != "fallback" {
		t.Fatalf("plan = %+v, want spare fallback", got)
	}
	if got.Candidates[1].Reasons[0] != ReasonCandidateWIPExhausted {
		t.Fatalf("candidate explanations = %+v, want WIP exhausted", got.Candidates)
	}
}

func TestPlanReviewWithoutAuthorEvidenceFailsClosed(t *testing.T) {
	in := readyInput(candidate("reviewer", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh))
	in.Requirement.NeedsReview = true
	in.ReviewAuthorKnown = false

	got := planner().Plan(in)
	if got.State != StateBlocked || got.Reasons[0] != ReasonReviewAuthorEvidenceMissing {
		t.Fatalf("plan = %+v, want review author evidence failure", got)
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

func TestPlanWIPTruthFailsClosed(t *testing.T) {
	in := readyInput(candidate("emp", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh))

	in.WIP.Known = false
	if got := planner().Plan(in); got.State != StateBlocked || got.Reasons[0] != ReasonWIPTruthMissing {
		t.Fatalf("missing WIP plan = %+v", got)
	}

	in.WIP.Known = true
	in.WIP.Reconciled = false
	if got := planner().Plan(in); got.State != StateBlocked || got.Reasons[0] != ReasonWIPReconciliationFailed {
		t.Fatalf("unreconciled WIP plan = %+v", got)
	}

	in.WIP.Reconciled = true
	in.WIP.ProjectionAvailable = false
	if got := planner().Plan(in); got.State != StateBlocked || got.Reasons[0] != ReasonWorkerProjectionUnavailable {
		t.Fatalf("missing projection plan = %+v", got)
	}

	in.WIP.ProjectionAvailable = true
	in.WIP.UnknownRows = 1
	if got := planner().Plan(in); got.State != StateBlocked || got.Reasons[0] != ReasonWIPUnknownEvidence {
		t.Fatalf("unknown WIP plan = %+v", got)
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

func TestPlanTerminalFrontierDominatesMissingGenerationEvidence(t *testing.T) {
	in := readyInput(candidate("emp", "00000000-0000-0000-0000-000000000001", "base-a", routescore.QuotaFresh))
	in.Frontier.Status = "done"
	in.Generation.Known = false

	got := planner().Plan(in)
	if got.State != StateSuperseded {
		t.Fatalf("plan = %+v, want terminal frontier to remain superseded", got)
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
