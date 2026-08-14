// Package continuousdispatch composes the existing read-only frontier,
// routing, runtime/base, generation, and lease evidence into one explainable
// NextAction. It is deliberately shadow-only: it never creates Tasks, writes
// receipts, acquires leases, or mutates an Issue.
package continuousdispatch

import (
	"sort"

	"github.com/multica-ai/multica/server/internal/readyfrontier"
	"github.com/multica-ai/multica/server/internal/routescore"
)

// State is the stable shadow recommendation state.
type State string

const (
	StateReady             State = "ready"
	StateFallback          State = "fallback"
	StateWaiting           State = "waiting"
	StateBlocked           State = "blocked"
	StateRunning           State = "running"
	StateSuperseded        State = "superseded"
	StateAlreadyDispatched State = "already_dispatched"
)

// Reason is a stable, client-localizable explanation code.
type Reason string

const (
	ReasonGenerationEvidenceMissing   Reason = "generation_evidence_missing"
	ReasonDuplicateUnattributed       Reason = "duplicate_generation_unattributed"
	ReasonExistingOpenTask            Reason = "existing_open_task"
	ReasonBaseEvidenceMissing         Reason = "base_visibility_unknown"
	ReasonRuntimeBaseMismatch         Reason = "runtime_base_mismatch"
	ReasonCandidateIdentityDuplicate  Reason = "candidate_identity_duplicate"
	ReasonNoEligibleCandidate         Reason = "no_eligible_candidate"
	ReasonPreferredUnavailable        Reason = "preferred_candidate_unavailable"
	ReasonWIPTruthMissing             Reason = "wip_truth_missing"
	ReasonWIPReconciliationFailed     Reason = "wip_reconciliation_failed"
	ReasonWorkerProjectionUnavailable Reason = "worker_projection_unavailable"
	ReasonWIPUnknownEvidence          Reason = "wip_unknown_evidence"
	ReasonCandidateWIPUnknown         Reason = "candidate_wip_unknown"
	ReasonCandidateWIPExhausted       Reason = "candidate_wip_exhausted"
	ReasonReviewSourceTaskMissing     Reason = "review_source_task_missing"
	ReasonReviewAuthorEvidenceMissing Reason = "review_author_evidence_missing"
	ReasonLeaseEvidenceMissing        Reason = "write_lease_evidence_missing"
	ReasonLeaseUnavailable            Reason = "write_lease_unavailable"
)

// GenerationEvidence proves whether this Issue/stage/revision/generation
// already owns a Task. Unknown evidence fails closed; a proven open Task is
// returned instead of recommending another Task.
type GenerationEvidence struct {
	Known                 bool   `json:"known"`
	OpenTaskID            string `json:"open_task_id,omitempty"`
	DuplicateUnattributed bool   `json:"duplicate_unattributed"`
}

// DispatchIdentity is the canonical identity of one dispatchable unit. The
// tuple is intentionally explicit on the wire because Phase 2 persists the
// same five fields as its idempotency key; callers must never derive it from a
// display title, assignee, Task status, or an arbitrary client key.
type DispatchIdentity struct {
	WorkspaceID       string `json:"workspace_id"`
	IssueID           string `json:"issue_id"`
	Stage             string `json:"stage"`
	CandidateRevision string `json:"candidate_revision"`
	Generation        string `json:"generation"`
}

// Complete reports whether every component required by the Phase 2 unique
// key is present. It deliberately performs no normalization: producers must
// emit the canonical values already stored in the Issue/Goal projection.
func (i DispatchIdentity) Complete() bool {
	return i.WorkspaceID != "" && i.IssueID != "" && i.Stage != "" &&
		i.CandidateRevision != "" && i.Generation != ""
}

// LeaseEvidence is a read-only view of the canonical worktree lease. The
// planner never acquires or renews it.
type LeaseEvidence struct {
	Required  bool   `json:"required"`
	Known     bool   `json:"known"`
	Available bool   `json:"available"`
	LeaseID   string `json:"lease_id,omitempty"`
}

// WIPTruthEvidence is the minimal fail-closed summary produced by the pure
// WIP truth engine. Unknown rows or workers can hide active work and therefore
// cannot be treated as spare capacity.
type WIPTruthEvidence struct {
	Required            bool `json:"required"`
	Known               bool `json:"known"`
	Reconciled          bool `json:"reconciled"`
	ProjectionAvailable bool `json:"projection_available"`
	UnknownRows         int  `json:"unknown_rows"`
	UnknownWorkers      int  `json:"unknown_workers"`
}

// Candidate joins a stable employee identity to its route and base evidence.
// Route.AgentID remains the execution identity; EmployeeID remains the
// company identity.
type Candidate struct {
	EmployeeID string               `json:"employee_id"`
	Model      string               `json:"model,omitempty"`
	AccountRef string               `json:"account_ref,omitempty"`
	BaseID     string               `json:"base_id,omitempty"`
	BaseKnown  bool                 `json:"base_known"`
	WIPKnown   bool                 `json:"wip_known"`
	ActiveWIP  int                  `json:"active_wip"`
	MaxWIP     int                  `json:"max_wip"`
	Route      routescore.Candidate `json:"-"`
}

// CandidateDecision records why one candidate was accepted or rejected.
type CandidateDecision struct {
	EmployeeID string   `json:"employee_id"`
	AgentID    string   `json:"agent_id"`
	RuntimeID  string   `json:"runtime_id"`
	Model      string   `json:"model,omitempty"`
	AccountRef string   `json:"account_ref,omitempty"`
	BaseID     string   `json:"base_id,omitempty"`
	Quota      string   `json:"quota"`
	ActiveWIP  int      `json:"active_wip"`
	MaxWIP     int      `json:"max_wip"`
	Score      float64  `json:"score"`
	Eligible   bool     `json:"eligible"`
	Reasons    []Reason `json:"reasons,omitempty"`
}

// Input is a complete read-only evidence snapshot for one shadow decision.
type Input struct {
	Frontier            readyfrontier.IssueInput   `json:"-"`
	Requirement         routescore.TaskRequirement `json:"-"`
	Candidates          []Candidate                `json:"-"`
	PreferredEmployeeID string                     `json:"preferred_employee_id,omitempty"`
	RequiredBaseID      string                     `json:"required_base_id,omitempty"`
	Generation          GenerationEvidence         `json:"generation"`
	WIP                 WIPTruthEvidence           `json:"wip"`
	Lease               LeaseEvidence              `json:"lease"`
	ReviewAuthorKnown   bool                       `json:"review_author_known"`
}

// NextAction is the explainable shadow output consumed by a future dispatcher.
type NextAction struct {
	State          State               `json:"state"`
	Reasons        []Reason            `json:"reasons,omitempty"`
	Selected       *CandidateDecision  `json:"selected,omitempty"`
	Candidates     []CandidateDecision `json:"candidates,omitempty"`
	ExistingTaskID string              `json:"existing_task_id,omitempty"`
	WriteLeaseID   string              `json:"write_lease_id,omitempty"`
}

// Planner computes a shadow NextAction. It is deterministic for an identical
// input and has no side effects.
type Planner struct {
	scorer *routescore.Scorer
}

// NewPlanner constructs a planner with the canonical route weights.
func NewPlanner() *Planner {
	return &Planner{scorer: routescore.NewScorer(nil)}
}

// WithScorer supports a deterministic clock and weights in tests.
func (p *Planner) WithScorer(s *routescore.Scorer) *Planner {
	cp := *p
	cp.scorer = s
	return &cp
}

// Plan returns a recommendation without mutating canonical state.
func (p *Planner) Plan(in Input) NextAction {
	frontier := readyfrontier.ClassifyIssue(in.Frontier)
	// Terminal/historical truth wins even when dispatch identity evidence is
	// incomplete: a done/cancelled/superseded Issue must never re-enter the
	// active frontier.
	if frontier.State == readyfrontier.StateSuperseded {
		return NextAction{State: StateSuperseded, Reasons: frontierReasons(frontier.Reasons)}
	}

	// Generation attribution precedes the ordinary running/waiting projection.
	// Otherwise any old open Task would make the planner return "running" before
	// it could prove that the Task belongs to this exact stage/revision/generation.
	if !in.Generation.Known {
		return NextAction{State: StateBlocked, Reasons: []Reason{ReasonGenerationEvidenceMissing}}
	}
	if in.Generation.DuplicateUnattributed {
		return NextAction{State: StateBlocked, Reasons: []Reason{ReasonDuplicateUnattributed}}
	}
	if in.Generation.OpenTaskID != "" {
		return NextAction{
			State:          StateAlreadyDispatched,
			Reasons:        []Reason{ReasonExistingOpenTask},
			ExistingTaskID: in.Generation.OpenTaskID,
		}
	}

	switch frontier.State {
	case readyfrontier.StateRunning:
		return NextAction{State: StateRunning, Reasons: frontierReasons(frontier.Reasons)}
	case readyfrontier.StateWaiting:
		return NextAction{State: StateWaiting, Reasons: frontierReasons(frontier.Reasons)}
	case readyfrontier.StateBlocked:
		return NextAction{State: StateBlocked, Reasons: frontierReasons(frontier.Reasons)}
	}

	if in.WIP.Required {
		if !in.WIP.Known {
			return NextAction{State: StateBlocked, Reasons: []Reason{ReasonWIPTruthMissing}}
		}
		if !in.WIP.Reconciled {
			return NextAction{State: StateBlocked, Reasons: []Reason{ReasonWIPReconciliationFailed}}
		}
		if !in.WIP.ProjectionAvailable {
			return NextAction{State: StateBlocked, Reasons: []Reason{ReasonWorkerProjectionUnavailable}}
		}
		if in.WIP.UnknownRows > 0 || in.WIP.UnknownWorkers > 0 {
			return NextAction{State: StateBlocked, Reasons: []Reason{ReasonWIPUnknownEvidence}}
		}
	}
	if in.Requirement.NeedsReview && !in.ReviewAuthorKnown {
		return NextAction{State: StateBlocked, Reasons: []Reason{ReasonReviewAuthorEvidenceMissing}}
	}

	if in.Lease.Required {
		if !in.Lease.Known {
			return NextAction{State: StateBlocked, Reasons: []Reason{ReasonLeaseEvidenceMissing}}
		}
		if !in.Lease.Available {
			return NextAction{State: StateWaiting, Reasons: []Reason{ReasonLeaseUnavailable}}
		}
	}

	decisions, duplicate := p.scoreCandidates(in)
	if duplicate {
		return NextAction{State: StateBlocked, Reasons: []Reason{ReasonCandidateIdentityDuplicate}, Candidates: decisions}
	}

	var selected *CandidateDecision
	for i := range decisions {
		if decisions[i].Eligible {
			cp := decisions[i]
			selected = &cp
			break
		}
	}
	if selected == nil {
		return NextAction{State: StateBlocked, Reasons: []Reason{ReasonNoEligibleCandidate}, Candidates: decisions}
	}

	state := StateReady
	reasons := []Reason(nil)
	if in.PreferredEmployeeID != "" && selected.EmployeeID != in.PreferredEmployeeID {
		state = StateFallback
		reasons = []Reason{ReasonPreferredUnavailable}
	}
	return NextAction{
		State:        state,
		Reasons:      reasons,
		Selected:     selected,
		Candidates:   decisions,
		WriteLeaseID: in.Lease.LeaseID,
	}
}

func (p *Planner) scoreCandidates(in Input) ([]CandidateDecision, bool) {
	type scoredCandidate struct {
		candidate Candidate
		result    routescore.Result
		decision  CandidateDecision
	}
	scored := make([]scoredCandidate, 0, len(in.Candidates))
	seen := make(map[string]struct{}, len(in.Candidates))
	duplicate := false
	for _, candidate := range in.Candidates {
		agentID := candidate.Route.AgentID.String()
		if _, ok := seen[agentID]; ok {
			duplicate = true
		}
		seen[agentID] = struct{}{}

		decision := CandidateDecision{
			EmployeeID: candidate.EmployeeID,
			AgentID:    agentID,
			RuntimeID:  candidate.Route.RuntimeID.String(),
			Model:      candidate.Model,
			AccountRef: candidate.AccountRef,
			BaseID:     candidate.BaseID,
			Quota:      string(candidate.Route.Quota),
			ActiveWIP:  candidate.ActiveWIP,
			MaxWIP:     candidate.MaxWIP,
		}
		if !candidate.WIPKnown || candidate.MaxWIP <= 0 {
			decision.Reasons = []Reason{ReasonCandidateWIPUnknown}
			scored = append(scored, scoredCandidate{candidate: candidate, decision: decision})
			continue
		}
		if candidate.ActiveWIP >= candidate.MaxWIP {
			decision.Reasons = []Reason{ReasonCandidateWIPExhausted}
			scored = append(scored, scoredCandidate{candidate: candidate, decision: decision})
			continue
		}
		if in.RequiredBaseID != "" && !candidate.BaseKnown {
			decision.Reasons = []Reason{ReasonBaseEvidenceMissing}
			scored = append(scored, scoredCandidate{candidate: candidate, decision: decision})
			continue
		}
		if in.RequiredBaseID != "" && candidate.BaseID != in.RequiredBaseID {
			decision.Reasons = []Reason{ReasonRuntimeBaseMismatch}
			scored = append(scored, scoredCandidate{candidate: candidate, decision: decision})
			continue
		}
		result := p.scorer.Score(candidate.Route, in.Requirement)
		decision.Score = result.TotalScore
		decision.Eligible = !result.FailClosed
		if result.FailClosed {
			decision.Reasons = []Reason{Reason(result.FailReason)}
		}
		scored = append(scored, scoredCandidate{candidate: candidate, result: result, decision: decision})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].decision.Eligible != scored[j].decision.Eligible {
			return scored[i].decision.Eligible
		}
		if scored[i].decision.Score != scored[j].decision.Score {
			return scored[i].decision.Score > scored[j].decision.Score
		}
		return scored[i].decision.AgentID < scored[j].decision.AgentID
	})
	decisions := make([]CandidateDecision, 0, len(scored))
	for _, item := range scored {
		decisions = append(decisions, item.decision)
	}
	return decisions, duplicate
}

func frontierReasons(in []readyfrontier.Reason) []Reason {
	out := make([]Reason, 0, len(in))
	for _, reason := range in {
		out = append(out, Reason(reason))
	}
	return out
}
