// Package readyfrontier holds the read-only runnability classification for the
// self-evolution "ready frontier" queue sensor. It classifies an Issue or Task
// node into one of five states — ready, running, waiting, blocked, superseded —
// from canonical data only: issue status + review_state, prerequisite signals,
// agent/runtime health, execution capacity, task prepare-lease, and task status.
// It never writes a new truth table and never re-derives what a canonical
// column already records.
//
// It is a leaf package (no internal dependencies) so both the service layer —
// which composes the canonical evidence — and the handler layer — which
// serializes it to the wire — share one classification and can never drift
// (mirrors the dispatch package's reason-code contract).
//
// Classification is fail-closed: an input whose evidence is missing or unknown
// (an unrecognized status, an unverifiable health signal) is classified as NOT
// ready (blocked) with a missing_evidence reason, never optimistically ready.
package readyfrontier

// State is the runnability classification of an issue or task node. The five
// values are stable wire strings the frontend can localize.
type State string

const (
	// StateReady means the node can be dispatched/run right now: every
	// prerequisite, health, capacity, and lease gate has passed and no task is
	// actively executing it.
	StateReady State = "ready"
	// StateRunning means a runtime holds an active execution attempt for the
	// node (a dispatched/running task).
	StateRunning State = "running"
	// StateWaiting means the node is not executing and is not ready, but the
	// hold is transient and known: a queued task waiting for a runtime, a parked
	// backlog node, an unmet prerequisite, an unavailable execution slot, or a
	// missing assignee.
	StateWaiting State = "waiting"
	// StateBlocked means the node cannot proceed without intervention: an
	// explicit blocked status, a failed task, a blocked prerequisite, an
	// unhealthy agent/runtime, an expired prepare-lease, or a missing-evidence
	// fail-closed on an unrecognized input.
	StateBlocked State = "blocked"
	// StateSuperseded means the node is no longer part of the active frontier:
	// its canonical review_state is superseded/archived_history, or it has
	// reached a terminal status.
	StateSuperseded State = "superseded"
)

// Reason is a stable, client-localizable reason attached to a non-ready state.
// It is decided at the branch that produced the state and is never reverse-
// engineered from a human-readable string (mirrors dispatch.ReasonCode).
type Reason string

const (
	ReasonSuperseded            Reason = "superseded"
	ReasonArchivedHistory       Reason = "archived_history"
	ReasonTerminal              Reason = "terminal"
	ReasonRunning               Reason = "running"
	ReasonQueued                Reason = "queued"
	ReasonDeferred              Reason = "deferred"
	ReasonWaitingLocalDir       Reason = "waiting_local_directory"
	ReasonFailed                Reason = "failed"
	ReasonBacklog               Reason = "backlog"
	ReasonBlockedStatus         Reason = "blocked_status"
	ReasonPrerequisiteBlocked   Reason = "prerequisite_blocked"
	ReasonPrerequisiteUnmet     Reason = "prerequisite_not_met"
	ReasonUnassigned            Reason = "unassigned"
	ReasonAgentArchived         Reason = "agent_archived"
	ReasonRuntimeUnavailable    Reason = "agent_runtime_unavailable"
	ReasonCapacity              Reason = "capacity_unavailable"
	ReasonLeaseExpired          Reason = "prepare_lease_expired"
	ReasonSupersededBySuccessor Reason = "superseded_by_successor"
	ReasonMissingEvidence       Reason = "missing_evidence"
)

// Classification is the result of classifying one node: a state plus zero or
// more stable reasons. Ready nodes carry no reasons; every other state carries
// at least one.
type Classification struct {
	State   State
	Reasons []Reason
}

// IssueInput carries the canonical (or caller-resolved) evidence for one issue
// node. Fields that cannot be read from a single row — prerequisite and stage
// barrier resolution, agent/runtime health, capacity — are resolved by the
// caller from existing canonical queries and passed as booleans so this leaf
// package stays free of database and service dependencies.
type IssueInput struct {
	// Status is issue.status.
	Status string
	// ReviewState is issue.review_state ("" when unset).
	ReviewState string

	// Prerequisites — caller-resolved from parent status and sibling stage
	// barrier (see issue_child_done.go stageBarrierClosed for the frontier
	// semantics).
	PrerequisiteBlocked bool // a prerequisite is blocked/failed (e.g. parent blocked)
	PrerequisiteUnmet   bool // a prerequisite is not yet met (e.g. parent parked, or a later stage)

	// Health — caller-resolved from agent + runtime rows.
	HasAssignee   bool // the issue has an agent/squad assignee
	AgentArchived bool // agent.archived_at IS NOT NULL
	RuntimeBound  bool // agent.runtime_id IS NOT NULL
	RuntimeOnline bool // the bound runtime's status is 'online'

	// Capacity — caller-resolved from agent/runtime concurrency limits vs the
	// active task count.
	CapacityKnown bool // whether capacity was determinable
	CapacityFree  bool // a slot is available (only meaningful when CapacityKnown)

	// Latest task — the most recently created agent_task_queue row, if any.
	HasTask    bool
	TaskStatus string
	// PrepareLeaseExpired is caller-resolved from the latest task's
	// prepare_lease_expires_at in the past. Only meaningful for a claimed task
	// (dispatched/running); ignored otherwise.
	PrepareLeaseExpired bool
}

// TaskInput carries the canonical (or caller-resolved) evidence for one task
// node (an agent_task_queue row).
type TaskInput struct {
	// Status is agent_task_queue.status.
	Status string

	// SupersededByNewer is caller-resolved: a newer task in the same lineage
	// (retry_of_task_id / rerun_of_task_id / escalation_for_task_id) points at
	// this task, so this row is historical.
	SupersededByNewer bool

	// PrepareLeaseExpired is caller-resolved from prepare_lease_expires_at in
	// the past. Only meaningful for a claimed task (dispatched/running).
	PrepareLeaseExpired bool
}

// reviewStateSuperseded / reviewStateArchivedHistory mirror the canonical
// service.ReviewState* constants (review_pipeline.go). They are duplicated as
// plain literals so this leaf package does not import service (which will
// import readyfrontier) and no cycle forms.
const (
	reviewStateSuperseded      = "superseded"
	reviewStateArchivedHistory = "archived_history"
)

// ClassifyIssue maps an issue node's evidence to its frontier state.
//
// Order of evaluation is significant: the most specific / terminal signals win
// first (superseded review_state, then terminal status, then the active task),
// followed by the explicit blocked status, then backlog, then — for actionable
// statuses only — the prerequisite, health, and capacity gates. Any status
// outside the known set fails closed to blocked/missing_evidence.
func ClassifyIssue(in IssueInput) Classification {
	// Canonical historical terminal review_state wins over everything.
	if in.ReviewState == reviewStateSuperseded {
		return Classification{State: StateSuperseded, Reasons: []Reason{ReasonSuperseded}}
	}
	if in.ReviewState == reviewStateArchivedHistory {
		return Classification{State: StateSuperseded, Reasons: []Reason{ReasonArchivedHistory}}
	}

	// Terminal status: the node has left the runnable frontier.
	if in.Status == "done" || in.Status == "cancelled" {
		return Classification{State: StateSuperseded, Reasons: []Reason{ReasonTerminal}}
	}

	// An active execution attempt dominates the classification. The prepare
	// lease is only meaningful on a claimed (dispatched/running) task: a lease
	// that died mid-claim means the run is wedged, not merely waiting.
	if in.HasTask {
		if in.PrepareLeaseExpired && (in.TaskStatus == "dispatched" || in.TaskStatus == "running") {
			return Classification{State: StateBlocked, Reasons: []Reason{ReasonLeaseExpired}}
		}
		switch in.TaskStatus {
		case "dispatched", "running":
			return Classification{State: StateRunning, Reasons: []Reason{ReasonRunning}}
		case "queued":
			return Classification{State: StateWaiting, Reasons: []Reason{ReasonQueued}}
		case "deferred":
			return Classification{State: StateWaiting, Reasons: []Reason{ReasonDeferred}}
		case "waiting_local_directory":
			return Classification{State: StateWaiting, Reasons: []Reason{ReasonWaitingLocalDir}}
		case "failed":
			return Classification{State: StateBlocked, Reasons: []Reason{ReasonFailed}}
		default:
			// Unknown task status: fail closed.
			return Classification{State: StateBlocked, Reasons: []Reason{ReasonMissingEvidence}}
		}
	}

	// Explicit blocked status.
	if in.Status == "blocked" {
		return Classification{State: StateBlocked, Reasons: []Reason{ReasonBlockedStatus}}
	}

	// Parked in backlog.
	if in.Status == "backlog" {
		return Classification{State: StateWaiting, Reasons: []Reason{ReasonBacklog}}
	}

	// Only actionable statuses reach the gate checks. Anything else is
	// unrecognized and fails closed rather than optimistically ready.
	switch in.Status {
	case "todo", "in_progress", "in_review":
	default:
		return Classification{State: StateBlocked, Reasons: []Reason{ReasonMissingEvidence}}
	}

	// Prerequisite gates.
	if in.PrerequisiteBlocked {
		return Classification{State: StateBlocked, Reasons: []Reason{ReasonPrerequisiteBlocked}}
	}
	if in.PrerequisiteUnmet {
		return Classification{State: StateWaiting, Reasons: []Reason{ReasonPrerequisiteUnmet}}
	}

	// Health gates.
	if !in.HasAssignee {
		return Classification{State: StateWaiting, Reasons: []Reason{ReasonUnassigned}}
	}
	if in.AgentArchived {
		return Classification{State: StateBlocked, Reasons: []Reason{ReasonAgentArchived}}
	}
	if !in.RuntimeBound || !in.RuntimeOnline {
		return Classification{State: StateBlocked, Reasons: []Reason{ReasonRuntimeUnavailable}}
	}

	// Capacity gate — fail-closed: when capacity could not be determined
	// (e.g. the CountRunningTasks query failed in the caller), the node must
	// not bypass this gate and fall through to ready. Classify as
	// blocked/missing_evidence instead so an evidence failure never produces
	// an optimistic ready (HIV-459 advisory).
	if !in.CapacityKnown {
		return Classification{State: StateBlocked, Reasons: []Reason{ReasonMissingEvidence}}
	}
	if !in.CapacityFree {
		return Classification{State: StateWaiting, Reasons: []Reason{ReasonCapacity}}
	}

	return Classification{State: StateReady}
}

// ClassifyTask maps a task node's evidence to its frontier state.
func ClassifyTask(in TaskInput) Classification {
	if in.SupersededByNewer {
		return Classification{State: StateSuperseded, Reasons: []Reason{ReasonSupersededBySuccessor}}
	}
	if in.PrepareLeaseExpired && (in.Status == "dispatched" || in.Status == "running") {
		return Classification{State: StateBlocked, Reasons: []Reason{ReasonLeaseExpired}}
	}
	switch in.Status {
	case "dispatched", "running":
		return Classification{State: StateRunning, Reasons: []Reason{ReasonRunning}}
	case "queued":
		return Classification{State: StateWaiting, Reasons: []Reason{ReasonQueued}}
	case "deferred":
		return Classification{State: StateWaiting, Reasons: []Reason{ReasonDeferred}}
	case "waiting_local_directory":
		return Classification{State: StateWaiting, Reasons: []Reason{ReasonWaitingLocalDir}}
	case "failed":
		return Classification{State: StateBlocked, Reasons: []Reason{ReasonFailed}}
	case "completed", "cancelled":
		return Classification{State: StateSuperseded, Reasons: []Reason{ReasonTerminal}}
	default:
		// Unknown task status: fail closed.
		return Classification{State: StateBlocked, Reasons: []Reason{ReasonMissingEvidence}}
	}
}
