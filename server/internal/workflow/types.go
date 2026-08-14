// Package workflow implements the W4 workflow kernel contract
// (HIVECREW-WORKFLOW-MEMORY-OS-V1, Phase 1). It is a pure orchestration layer:
// it organizes and references existing Project/Issue/Task/Run/Outcome state,
// and never copies or becomes a second source of that business truth.
//
// Slice-W1 scope: in-memory, no schema, no migration. Persistence (Slice-W2)
// is gated on the Owner migration-counter decision.
package workflow

import "time"

// RiskTier is the workflow risk classification (FAST/STANDARD/OWNER).
type RiskTier string

const (
	RiskFast     RiskTier = "fast"
	RiskStandard RiskTier = "standard"
	RiskOwner    RiskTier = "owner"
)

func (t RiskTier) Valid() bool {
	switch t {
	case RiskFast, RiskStandard, RiskOwner:
		return true
	default:
		return false
	}
}

// InstanceStatus is the closed workflow-instance state.
type InstanceStatus string

const (
	StatusRunning   InstanceStatus = "running"
	StatusPaused    InstanceStatus = "paused"
	StatusStopped   InstanceStatus = "stopped"
	StatusCompleted InstanceStatus = "completed"
	StatusFailed    InstanceStatus = "failed"
)

// Stage is one ordered step of a WorkflowDefinition.
type Stage struct {
	Name string
	SLA  time.Duration // 0 = no SLA
}

// WorkflowDefinition is a versioned workflow template.
type WorkflowDefinition struct {
	ID      string
	Version int
	Risk    RiskTier
	Stages  []Stage
}

// NodeKind is the minimal versioned graph contract. Graph definitions are
// candidate workflow configuration; they reference, but do not duplicate,
// HiveCrew Employee/Task/Run state.
type NodeKind string

const (
	NodeAgentTask NodeKind = "agent_task"
	NodeHumanTask NodeKind = "human_task"
	NodeApproval  NodeKind = "approval"
	NodeDecision  NodeKind = "decision"
)

func (k NodeKind) Valid() bool {
	switch k {
	case NodeAgentTask, NodeHumanTask, NodeApproval, NodeDecision:
		return true
	default:
		return false
	}
}

type AgentBinding struct {
	Mode         string   `json:"mode"` // fixed_employee, capability_pool, project_default, human
	EmployeeID   string   `json:"employee_id,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type GraphNode struct {
	ID           string        `json:"id"`
	Kind         NodeKind      `json:"kind"`
	Name         string        `json:"name"`
	AgentBinding *AgentBinding `json:"agent_binding,omitempty"`
}

type GraphEdge struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	When string `json:"when,omitempty"`
}

type WorkflowGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// WorkflowDefinitionVersion is immutable once published. It is deliberately
// separate from WorkflowDefinition's legacy stage representation so the
// designer can evolve without creating a second runtime engine.
type WorkflowDefinitionVersion struct {
	DefinitionID string        `json:"definition_id"`
	Version      int           `json:"version"`
	Risk         RiskTier      `json:"risk"`
	Graph        WorkflowGraph `json:"graph"`
}

// ContextRef references the business objects a workflow instance is about,
// WITHOUT copying their state.
type ContextRef struct {
	ProjectID string
	IssueID   string
	OutcomeID string
}

// WorkflowInstance is one running instance of a definition.
type WorkflowInstance struct {
	ID string
	// WorkspaceID scopes the orchestration record to the HiveCrew workspace.
	// It is a reference only; Project/Issue/Task/Run/Employee remain owned by
	// their respective authorities.
	WorkspaceID       string
	DefinitionID      string
	DefinitionVersion int
	Context           ContextRef
	StageIndex        int
	Status            InstanceStatus
}

// StageExecution records one stage execution with exact Task/Run/actor/runtime
// and evidence references.
type StageExecution struct {
	InstanceID string
	StageIndex int
	StageName  string
	EnteredAt  time.Time
	TaskID     string
	RunID      string
	ActorID    string
	RuntimeID  string
	Evidence   []string
}

// Event is an append-only fact event with an idempotency key.
type Event struct {
	Sequence       int64
	InstanceID     string
	Kind           string
	SourceRef      string
	Actor          string
	OccurredAt     time.Time
	ObservedAt     time.Time
	IdempotencyKey string
}

// Receipt is a command receipt. Changed=false means the command was already
// applied under the same idempotency key (no double transition).
type Receipt struct {
	Command        string
	InstanceID     string
	IdempotencyKey string
	Accepted       bool
	Changed        bool
	Reason         string
}

// AdvanceEvidence is the caller-supplied proof for a stage advance. The engine
// only enforces the risk gate; it does not invent business conditions.
type AdvanceEvidence struct {
	ReviewPassed  bool // required for STANDARD
	OwnerApproved bool // required for OWNER
	TaskID        string
	RunID         string
	ActorID       string
	RuntimeID     string
	Notes         []string
}
