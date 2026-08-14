package continuousdispatch

import (
	"sort"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/readyfrontier"
	"github.com/multica-ai/multica/server/internal/routescore"
)

// WorkConservingReason explains a plan-level decision.  The planner is a
// shadow read model: these values describe why a candidate can or cannot be
// suggested, but never authorize a Task write.
const (
	ReasonPlanGoalMissing            Reason = "goal_evidence_missing"
	ReasonIssueGoalMismatch          Reason = "issue_goal_mismatch"
	ReasonIssueProvenanceMissing     Reason = "issue_provenance_missing"
	ReasonIssueAuthorityMissing      Reason = "issue_authority_missing"
	ReasonIssueWritePathEvidence     Reason = "issue_write_path_evidence_missing"
	ReasonIssueWritePathConflict     Reason = "issue_write_path_conflict"
	ReasonEmployeeHealthEvidence     Reason = "employee_health_evidence_missing"
	ReasonEmployeeUnhealthy          Reason = "employee_unhealthy"
	ReasonEmployeeIdleEvidence       Reason = "employee_idle_evidence_missing"
	ReasonEmployeeNotIdle            Reason = "employee_not_idle"
	ReasonEmployeeIdentityDuplicate  Reason = "employee_identity_duplicate"
	ReasonEmployeeSourceMissing      Reason = "employee_provenance_missing"
	ReasonEmployeeAuthorityMissing   Reason = "employee_authority_missing"
	ReasonEmployeeRuntimeEvidence    Reason = "employee_runtime_evidence_missing"
	ReasonEmployeeModelMissing       Reason = "employee_model_evidence_missing"
	ReasonEmployeeWritePathEvidence  Reason = "employee_write_path_evidence_missing"
	ReasonEmployeeWritePathConflict  Reason = "employee_write_path_conflict"
	ReasonEmployeeWritePathMismatch  Reason = "employee_write_path_mismatch"
	ReasonWorkPathAlreadyPlanned     Reason = "write_path_already_planned"
	ReasonNoHealthyIdleEmployee      Reason = "no_healthy_idle_employee"
	ReasonIssueIdentityDuplicate     Reason = "issue_identity_duplicate"
	ReasonEmployeeRuntimeUnavailable Reason = "runtime_unavailable"
	ReasonEmployeeQuotaUnknown       Reason = "quota_unknown"
	ReasonEmployeeQuotaStale         Reason = "quota_stale"
	ReasonEmployeeQuotaExhausted     Reason = "quota_exhausted"
)

// WorkConservingWritePath is the caller's read-only observation of the
// canonical worktree/write path.  Known=false is never treated as a free
// path.  ConflictWith identifies the current holder when the source knows it.
type WorkConservingWritePath struct {
	Known        bool   `json:"known"`
	Key          string `json:"key,omitempty"`
	Conflict     bool   `json:"conflict"`
	ConflictWith string `json:"conflict_with,omitempty"`
}

// WorkConservingWriteLock is an already observed active lock.  It is input
// evidence only; Plan never acquires, renews, or releases a lock.
type WorkConservingWriteLock struct {
	Key     string `json:"key"`
	IssueID string `json:"issue_id"`
	Owner   string `json:"owner,omitempty"`
	Active  bool   `json:"active"`
}

// WorkConservingIssue is the complete read-only evidence snapshot for one
// open issue in one Goal.  Provenance and Authority are explicit because a
// local title/assignee projection is not sufficient to route work safely.
type WorkConservingIssue struct {
	ID                  string                     `json:"issue_id"`
	GoalID              string                     `json:"goal_id"`
	PreferredEmployeeID string                     `json:"preferred_employee_id,omitempty"`
	Frontier            readyfrontier.IssueInput   `json:"-"`
	Requirement         routescore.TaskRequirement `json:"-"`
	RequiredBaseID      string                     `json:"required_base_id,omitempty"`
	Generation          GenerationEvidence         `json:"generation"`
	WIP                 WIPTruthEvidence           `json:"wip"`
	Lease               LeaseEvidence              `json:"lease"`
	ReviewAuthorKnown   bool                       `json:"review_author_known"`
	ProvenanceKnown     bool                       `json:"provenance_known"`
	AuthorityKnown      bool                       `json:"authority_known"`
	WritePath           WorkConservingWritePath    `json:"write_path"`
}

// WorkConservingEmployee is an observed healthy/idle employee candidate.  The
// explicit evidence booleans prevent a missing projection from becoming
// accidental availability.  Candidate carries the canonical runtime, quota,
// model/account and route score inputs.
type WorkConservingEmployee struct {
	Candidate       Candidate               `json:"candidate"`
	HealthyKnown    bool                    `json:"healthy_known"`
	Healthy         bool                    `json:"healthy"`
	IdleKnown       bool                    `json:"idle_known"`
	Idle            bool                    `json:"idle"`
	ProvenanceKnown bool                    `json:"provenance_known"`
	AuthorityKnown  bool                    `json:"authority_known"`
	WritePath       WorkConservingWritePath `json:"write_path"`
}

// WorkConservingInput is one Goal-scoped, read-only planning snapshot.
type WorkConservingInput struct {
	GoalID      string                    `json:"goal_id"`
	Issues      []WorkConservingIssue     `json:"issues"`
	Employees   []WorkConservingEmployee  `json:"employees"`
	ActiveLocks []WorkConservingWriteLock `json:"active_locks,omitempty"`
}

// WorkConservingSuggestion is a unique employee -> issue candidate.  It is a
// recommendation only; no Task, queue row, lease, receipt, or DB write is
// created by this package.
type WorkConservingSuggestion struct {
	IssueID        string  `json:"issue_id"`
	GoalID         string  `json:"goal_id"`
	EmployeeID     string  `json:"employee_id"`
	AgentID        string  `json:"agent_id"`
	RuntimeID      string  `json:"runtime_id"`
	BaseID         string  `json:"base_id,omitempty"`
	Score          float64 `json:"score"`
	FallbackReason Reason  `json:"fallback_reason,omitempty"`
	Receiver       string  `json:"receiver"`
	WakeCondition  string  `json:"wake_condition"`
}

// WorkConservingBlockedIssue records an open issue that could not be planned,
// with an accountable receiver and an explicit wake condition.
type WorkConservingBlockedIssue struct {
	IssueID               string   `json:"issue_id"`
	GoalID                string   `json:"goal_id"`
	Reasons               []Reason `json:"reasons"`
	Receiver              string   `json:"receiver"`
	WakeCondition         string   `json:"wake_condition"`
	EligibleEmployeeCount int      `json:"eligible_employee_count"`
}

// WorkConservingMismatch measures work-conserving scheduling health without
// equating a Task count or a status projection with useful output.
type WorkConservingMismatch struct {
	OpenIssues                    int `json:"open_issues"`
	PlannedIssues                 int `json:"planned_issues"`
	BlockedBacklog                int `json:"blocked_backlog"`
	HealthyIdleEmployees          int `json:"healthy_idle_employees"`
	UnmatchedHealthyIdleEmployees int `json:"unmatched_healthy_idle_employees"`
	ExecutableBacklog             int `json:"executable_backlog"`
	IdleBacklogMismatch           int `json:"idle_backlog_mismatch"`
}

// WorkConservingPlan is deterministic output for one read-only planning pass.
type WorkConservingPlan struct {
	GoalID                  string                       `json:"goal_id"`
	GlobalReasons           []Reason                     `json:"global_reasons,omitempty"`
	Suggestions             []WorkConservingSuggestion   `json:"suggestions"`
	BlockedBacklog          []WorkConservingBlockedIssue `json:"blocked_backlog"`
	Mismatch                WorkConservingMismatch       `json:"mismatch"`
	executableBacklog       int
	invalidEmployeeIdentity bool
}

// PlanWorkConserving greedily computes a deterministic, one-to-one candidate
// matching for the supplied Goal.  It deliberately does not call a dispatcher
// or any persistence API.  Issue IDs and Agent IDs are the tie-breakers, so an
// identical evidence snapshot always produces identical recommendations.
func (p *Planner) PlanWorkConserving(in WorkConservingInput) WorkConservingPlan {
	plan := WorkConservingPlan{GoalID: in.GoalID}
	if in.GoalID == "" {
		plan.GlobalReasons = []Reason{ReasonPlanGoalMissing}
		for _, issue := range sortedWorkIssues(in.Issues) {
			if issue.ID == "" || workConservingFrontier(issue).State == readyfrontier.StateSuperseded {
				continue
			}
			plan.Mismatch.OpenIssues++
			plan.BlockedBacklog = append(plan.BlockedBacklog, blockedWorkIssue(issue, []Reason{ReasonPlanGoalMissing}, 0))
		}
		plan.Mismatch = p.workMismatch(in, plan)
		return plan
	}

	employees, duplicate := indexWorkEmployees(in.Employees)
	if duplicate {
		plan.invalidEmployeeIdentity = true
		plan.GlobalReasons = append(plan.GlobalReasons, ReasonEmployeeIdentityDuplicate)
		for _, issue := range sortedWorkIssues(in.Issues) {
			if issue.ID != "" && issue.GoalID == in.GoalID {
				plan.Mismatch.OpenIssues++
				plan.addBlocked(issue, []Reason{ReasonEmployeeIdentityDuplicate}, 0)
			}
		}
		plan.Mismatch = p.workMismatch(in, plan)
		return plan
	}
	locks := activeWorkLocks(in.ActiveLocks)
	usedEmployees := make(map[string]struct{}, len(employees))
	usedPaths := make(map[string]string)
	duplicateIssues := duplicateWorkIssueIDs(in.Issues)
	seenIssues := make(map[string]struct{}, len(in.Issues))

	for _, issue := range sortedWorkIssues(in.Issues) {
		if issue.ID == "" {
			continue
		}
		frontier := workConservingFrontier(issue)
		if frontier.State == readyfrontier.StateSuperseded {
			continue
		}
		plan.Mismatch.OpenIssues++
		if _, seen := seenIssues[issue.ID]; seen {
			plan.addBlocked(issue, []Reason{ReasonIssueIdentityDuplicate}, 0)
			continue
		}
		seenIssues[issue.ID] = struct{}{}
		if _, duplicate := duplicateIssues[issue.ID]; duplicate {
			plan.addBlocked(issue, []Reason{ReasonIssueIdentityDuplicate}, 0)
			continue
		}
		if issue.GoalID == "" || issue.GoalID != in.GoalID {
			plan.addBlocked(issue, []Reason{ReasonIssueGoalMismatch}, 0)
			continue
		}
		if !issue.ProvenanceKnown {
			plan.addBlocked(issue, []Reason{ReasonIssueProvenanceMissing}, 0)
			continue
		}
		if !issue.AuthorityKnown {
			plan.addBlocked(issue, []Reason{ReasonIssueAuthorityMissing}, 0)
			continue
		}
		if !issue.WritePath.Known {
			plan.addBlocked(issue, []Reason{ReasonIssueWritePathEvidence}, 0)
			continue
		}
		if issue.WritePath.Conflict {
			plan.addBlocked(issue, []Reason{ReasonIssueWritePathConflict}, 0)
			continue
		}
		if issue.WritePath.Key != "" {
			if lockIssue, ok := locks[issue.WritePath.Key]; ok && lockIssue != issue.ID {
				plan.addBlocked(issue, []Reason{ReasonIssueWritePathConflict}, 0)
				continue
			}
			if priorIssue, ok := usedPaths[issue.WritePath.Key]; ok {
				plan.addBlocked(issue, []Reason{ReasonWorkPathAlreadyPlanned}, 0)
				_ = priorIssue
				continue
			}
		}

		// An unavailable preferred employee is intentionally not a terminal
		// issue state. Candidate-level gates below decide whether a valid
		// fallback exists. Other frontier blockers remain strict.
		if frontier.State != readyfrontier.StateReady {
			plan.addBlocked(issue, frontierReasons(frontier.Reasons), 0)
			continue
		}
		if gate := p.Plan(Input{
			Frontier: workConservingFrontierInput(issue), Requirement: issue.Requirement,
			Generation: issue.Generation, WIP: issue.WIP, Lease: issue.Lease,
			ReviewAuthorKnown: issue.ReviewAuthorKnown,
		}); !workConservingGatePassed(gate) {
			plan.addBlocked(issue, gate.Reasons, 0)
			continue
		}
		plan.executableBacklog++

		best, count, candidateBlock := p.bestWorkCandidate(issue, in.Employees, employees, usedEmployees, usedPaths, locks)
		if best == nil {
			reasons := []Reason{ReasonNoEligibleCandidate}
			if count == 0 {
				reasons = []Reason{ReasonNoHealthyIdleEmployee}
			}
			if candidateBlock != "" {
				reasons = []Reason{candidateBlock}
			}
			plan.addBlocked(issue, reasons, count)
			continue
		}
		suggestion := WorkConservingSuggestion{
			IssueID: issue.ID, GoalID: issue.GoalID, EmployeeID: best.employee.Candidate.EmployeeID,
			AgentID: best.decision.AgentID, RuntimeID: best.decision.RuntimeID,
			BaseID: best.decision.BaseID, Score: best.decision.Score,
			Receiver: "dispatch-coordinator", WakeCondition: "dispatch coordinator confirms candidate and current write lease",
		}
		if issue.PreferredEmployeeID != "" && issue.PreferredEmployeeID != best.employee.Candidate.EmployeeID {
			suggestion.FallbackReason = ReasonPreferredUnavailable
		}
		plan.Suggestions = append(plan.Suggestions, suggestion)
		usedEmployees[best.employee.Candidate.EmployeeID] = struct{}{}
		if issue.WritePath.Key != "" {
			usedPaths[issue.WritePath.Key] = issue.ID
		}
		if best.employee.WritePath.Key != "" {
			usedPaths[best.employee.WritePath.Key] = issue.ID
		}
		plan.Mismatch.PlannedIssues++
	}

	plan.Mismatch = p.workMismatch(in, plan)
	return plan
}

type workCandidateResult struct {
	employee WorkConservingEmployee
	decision CandidateDecision
}

func workConservingGatePassed(gate NextAction) bool {
	if gate.State == StateAlreadyDispatched || gate.State == StateRunning || gate.State == StateWaiting || gate.State == StateBlocked || gate.State == StateSuperseded {
		for _, reason := range gate.Reasons {
			if reason == ReasonNoEligibleCandidate {
				return true
			}
		}
		return false
	}
	return gate.State == StateReady || gate.State == StateFallback
}

func (p *Planner) bestWorkCandidate(issue WorkConservingIssue, all []WorkConservingEmployee, byID map[string]WorkConservingEmployee, used map[string]struct{}, usedPaths map[string]string, locks map[string]string) (*workCandidateResult, int, Reason) {
	var best *workCandidateResult
	eligible := 0
	pathBlocked := false
	var candidateBlock Reason
	for _, employee := range all {
		if _, ok := used[employee.Candidate.EmployeeID]; ok {
			continue
		}
		if issue.PreferredEmployeeID != "" && employee.Candidate.EmployeeID == issue.PreferredEmployeeID {
			// Preferred employees are scored normally; this branch only makes
			// the preference visible to the deterministic sort below.
		}
		if _, ok := byID[employee.Candidate.EmployeeID]; !ok {
			continue
		}
		if pathKey := employee.WritePath.Key; pathKey != "" {
			if lockIssue, ok := locks[pathKey]; ok && lockIssue != issue.ID {
				pathBlocked = true
				continue
			}
			if _, ok := usedPaths[pathKey]; ok {
				pathBlocked = true
				continue
			}
		}
		decision := workEmployeeDecision(employee, issue)
		if !decision.Eligible {
			candidateBlock = stableWorkBlockReason(candidateBlock, firstWorkDecisionReason(decision))
			continue
		}
		// Reuse the canonical route scorer for quota/runtime/role and
		// independence gates after local evidence gates have passed.
		scored := p.scorer.Score(employee.Candidate.Route, issue.Requirement)
		decision.Score = scored.TotalScore
		if scored.FailClosed {
			candidateBlock = stableWorkBlockReason(candidateBlock, Reason(scored.FailReason))
			continue
		}
		eligible++
		candidate := &workCandidateResult{employee: employee, decision: decision}
		if best == nil || betterWorkCandidate(candidate, best, issue.PreferredEmployeeID) {
			best = candidate
		}
	}
	if best == nil && pathBlocked {
		return nil, eligible, ReasonWorkPathAlreadyPlanned
	}
	return best, eligible, candidateBlock
}

func workEmployeeDecision(employee WorkConservingEmployee, issue WorkConservingIssue) CandidateDecision {
	c := employee.Candidate
	d := CandidateDecision{EmployeeID: c.EmployeeID, AgentID: c.Route.AgentID.String(), RuntimeID: c.Route.RuntimeID.String(), Model: c.Model, AccountRef: c.AccountRef, BaseID: c.BaseID, Quota: string(c.Route.Quota), ActiveWIP: c.ActiveWIP, MaxWIP: c.MaxWIP}
	if !employee.HealthyKnown {
		d.Reasons = []Reason{ReasonEmployeeHealthEvidence}
		return d
	}
	if !employee.Healthy {
		d.Reasons = []Reason{ReasonEmployeeUnhealthy}
		return d
	}
	if !employee.IdleKnown {
		d.Reasons = []Reason{ReasonEmployeeIdleEvidence}
		return d
	}
	if !employee.Idle || c.ActiveWIP != 0 {
		d.Reasons = []Reason{ReasonEmployeeNotIdle}
		return d
	}
	if !employee.ProvenanceKnown {
		d.Reasons = []Reason{ReasonEmployeeSourceMissing}
		return d
	}
	if !employee.AuthorityKnown {
		d.Reasons = []Reason{ReasonEmployeeAuthorityMissing}
		return d
	}
	if c.Route.AgentID == uuid.Nil || c.Route.RuntimeID == uuid.Nil {
		d.Reasons = []Reason{ReasonEmployeeRuntimeEvidence}
		return d
	}
	if c.Route.RuntimeHealth != routescore.RuntimeOnline {
		d.Reasons = []Reason{ReasonEmployeeRuntimeUnavailable}
		return d
	}
	switch c.Route.Quota {
	case routescore.QuotaFresh:
		// Fresh is the only quota state that is schedulable.
	case routescore.QuotaStale:
		d.Reasons = []Reason{ReasonEmployeeQuotaStale}
		return d
	case routescore.QuotaExhausted:
		d.Reasons = []Reason{ReasonEmployeeQuotaExhausted}
		return d
	case routescore.QuotaUnknown:
		d.Reasons = []Reason{ReasonEmployeeQuotaUnknown}
		return d
	default:
		d.Reasons = []Reason{ReasonEmployeeQuotaUnknown}
		return d
	}
	if c.Model == "" {
		d.Reasons = []Reason{ReasonEmployeeModelMissing}
		return d
	}
	if !c.WIPKnown || c.MaxWIP <= 0 {
		d.Reasons = []Reason{ReasonCandidateWIPUnknown}
		return d
	}
	if c.ActiveWIP >= c.MaxWIP {
		d.Reasons = []Reason{ReasonCandidateWIPExhausted}
		return d
	}
	if !c.BaseKnown {
		d.Reasons = []Reason{ReasonBaseEvidenceMissing}
		return d
	}
	if issue.RequiredBaseID != "" && c.BaseID != issue.RequiredBaseID {
		d.Reasons = []Reason{ReasonRuntimeBaseMismatch}
		return d
	}
	if !employee.WritePath.Known {
		d.Reasons = []Reason{ReasonEmployeeWritePathEvidence}
		return d
	}
	if employee.WritePath.Conflict {
		d.Reasons = []Reason{ReasonEmployeeWritePathConflict}
		return d
	}
	if issue.WritePath.Key != "" && employee.WritePath.Key != issue.WritePath.Key {
		d.Reasons = []Reason{ReasonEmployeeWritePathMismatch}
		return d
	}
	d.Eligible = true
	return d
}

func betterWorkCandidate(a, b *workCandidateResult, preferred string) bool {
	aPreferred := preferred != "" && a.employee.Candidate.EmployeeID == preferred
	bPreferred := preferred != "" && b.employee.Candidate.EmployeeID == preferred
	if aPreferred != bPreferred {
		return aPreferred
	}
	if a.decision.Score != b.decision.Score {
		return a.decision.Score > b.decision.Score
	}
	return a.decision.AgentID < b.decision.AgentID
}

func workConservingFrontier(issue WorkConservingIssue) readyfrontier.Classification {
	return readyfrontier.ClassifyIssue(workConservingFrontierInput(issue))
}

func workConservingFrontierInput(issue WorkConservingIssue) readyfrontier.IssueInput {
	frontier := issue.Frontier
	// Assignment/runtime/capacity describe the preferred route, not the
	// issue's intrinsic readiness. Allow the candidate pool to provide a
	// fallback while preserving status, task, prerequisite and unknown-capacity
	// evidence as hard gates.
	frontier.HasAssignee = true
	frontier.AgentArchived = false
	frontier.RuntimeBound = true
	frontier.RuntimeOnline = true
	if frontier.CapacityKnown {
		frontier.CapacityFree = true
	}
	return frontier
}

func indexWorkEmployees(in []WorkConservingEmployee) (map[string]WorkConservingEmployee, bool) {
	out := make(map[string]WorkConservingEmployee, len(in))
	agents := make(map[string]struct{}, len(in))
	duplicate := false
	for _, employee := range in {
		id := employee.Candidate.EmployeeID
		if id == "" {
			continue
		}
		if _, exists := out[id]; exists {
			duplicate = true
			continue
		}
		agentID := employee.Candidate.Route.AgentID.String()
		if agentID == uuid.Nil.String() {
			// The ordinary candidate evidence gate will explain this row;
			// only a repeated non-zero identity is a plan-wide collision.
		} else if _, exists := agents[agentID]; exists {
			duplicate = true
			continue
		}
		agents[agentID] = struct{}{}
		out[id] = employee
	}
	return out, duplicate
}

func sortedWorkIssues(in []WorkConservingIssue) []WorkConservingIssue {
	out := append([]WorkConservingIssue(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func duplicateWorkIssueIDs(in []WorkConservingIssue) map[string]struct{} {
	counts := make(map[string]int, len(in))
	for _, issue := range in {
		if issue.ID != "" {
			counts[issue.ID]++
		}
	}
	out := make(map[string]struct{})
	for id, count := range counts {
		if count > 1 {
			out[id] = struct{}{}
		}
	}
	return out
}

func activeWorkLocks(in []WorkConservingWriteLock) map[string]string {
	out := make(map[string]string, len(in))
	for _, lock := range in {
		if lock.Active && lock.Key != "" {
			out[lock.Key] = lock.IssueID
		}
	}
	return out
}

func (p *WorkConservingPlan) addBlocked(issue WorkConservingIssue, reasons []Reason, count int) {
	if len(reasons) == 0 {
		reasons = []Reason{ReasonNoEligibleCandidate}
	}
	receiver, wake := workReceiverWake(reasons)
	p.BlockedBacklog = append(p.BlockedBacklog, WorkConservingBlockedIssue{IssueID: issue.ID, GoalID: issue.GoalID, Reasons: reasons, Receiver: receiver, WakeCondition: wake, EligibleEmployeeCount: count})
}

func blockedWorkIssue(issue WorkConservingIssue, reasons []Reason, count int) WorkConservingBlockedIssue {
	receiver, wake := workReceiverWake(reasons)
	return WorkConservingBlockedIssue{IssueID: issue.ID, GoalID: issue.GoalID, Reasons: reasons, Receiver: receiver, WakeCondition: wake, EligibleEmployeeCount: count}
}

func workReceiverWake(reasons []Reason) (string, string) {
	for _, reason := range reasons {
		switch reason {
		case ReasonIssueAuthorityMissing, ReasonIssueProvenanceMissing, ReasonEmployeeAuthorityMissing, ReasonEmployeeSourceMissing:
			return "authority-operator", "current Authority and provenance evidence is readable and request-matching"
		case ReasonIssueWritePathConflict, ReasonIssueWritePathEvidence, ReasonEmployeeWritePathConflict, ReasonEmployeeWritePathEvidence, ReasonWorkPathAlreadyPlanned:
			return "write-lease-owner", "canonical write path is known and no conflicting active owner remains"
		case ReasonNoHealthyIdleEmployee, ReasonEmployeeNotIdle, ReasonEmployeeUnhealthy, ReasonEmployeeHealthEvidence, ReasonEmployeeIdleEvidence:
			return "dispatch-coordinator", "a healthy idle employee with fresh runtime, quota, WIP, and lease evidence is observed"
		case ReasonIssueGoalMismatch, ReasonPlanGoalMissing:
			return "goal-owner", "issue is linked to the requested Goal with current provenance"
		}
	}
	return "dispatch-coordinator", "an eligible non-conflicting candidate is observed and current dispatch evidence is re-read"
}

func (p *Planner) workMismatch(in WorkConservingInput, plan WorkConservingPlan) WorkConservingMismatch {
	m := plan.Mismatch
	m.BlockedBacklog = len(plan.BlockedBacklog)
	if !plan.invalidEmployeeIdentity {
		for _, employee := range in.Employees {
			decision := workEmployeeDecision(employee, WorkConservingIssue{})
			if decision.Eligible && !p.scorer.Score(employee.Candidate.Route, routescore.TaskRequirement{}).FailClosed {
				m.HealthyIdleEmployees++
			}
		}
	}
	m.UnmatchedHealthyIdleEmployees = m.HealthyIdleEmployees - len(plan.Suggestions)
	if m.UnmatchedHealthyIdleEmployees < 0 {
		m.UnmatchedHealthyIdleEmployees = 0
	}
	m.ExecutableBacklog = plan.executableBacklog
	if m.ExecutableBacklog > m.HealthyIdleEmployees {
		m.IdleBacklogMismatch = m.ExecutableBacklog - m.HealthyIdleEmployees
	}
	return m
}

func firstWorkDecisionReason(decision CandidateDecision) Reason {
	if len(decision.Reasons) == 0 {
		return ""
	}
	return decision.Reasons[0]
}

func stableWorkBlockReason(current, next Reason) Reason {
	if next == "" || (current != "" && current <= next) {
		return current
	}
	return next
}
