// Package workflow implements the W4 workflow kernel contract
// (HIVECREW-WORKFLOW-MEMORY-OS-V1, Phase 1). It is a pure orchestration layer:
// it organizes and references existing Project/Issue/Task/Run/Outcome state,
// and never copies or becomes a second source of that business truth.
//
// Slice-W1 scope: in-memory, no schema, no migration. Persistence (Slice-W2)
// is gated on the Owner migration-counter decision.
package workflow

import (
	"fmt"
	"time"
)

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
	Name string        `json:"name"`
	SLA  time.Duration `json:"sla_ns,omitempty"` // 0 = no SLA
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
	Role         string   `json:"role,omitempty"`
	Capability   string   `json:"capability,omitempty"`
}

// GraphPosition is presentation metadata for a published graph. It is
// persisted with the immutable version so the visual workflow can be reopened
// without deriving its layout from node order.
type GraphPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type GraphNode struct {
	ID           string        `json:"id"`
	Kind         NodeKind      `json:"kind"`
	Name         string        `json:"name"`
	AgentBinding *AgentBinding `json:"agent_binding,omitempty"`
	Position     *GraphPosition `json:"position,omitempty"`
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
	WorkspaceID  string        `json:"workspace_id"`
	ProjectID    string        `json:"project_id,omitempty"`
	Version      int           `json:"version"`
	Risk         RiskTier      `json:"risk"`
	Stages       []Stage       `json:"stages"`
	Graph        WorkflowGraph `json:"graph"`
	Digest       string        `json:"digest"`
	CreatedAt    time.Time     `json:"created_at"`
	PublishedAt  time.Time     `json:"published_at"`
}

// ValidatePublishedGraph enforces the closed graph contract at the publish
// boundary. A published graph is immutable, so accepting an invalid graph
// here would permanently create an unusable workflow definition version.
func (v WorkflowDefinitionVersion) ValidatePublishedGraph() error {
	if v.DefinitionID == "" || v.WorkspaceID == "" {
		return fmt.Errorf("definition_id and workspace_id are required")
	}
	if v.Version < 1 {
		return fmt.Errorf("version must be positive")
	}
	if !v.Risk.Valid() {
		return fmt.Errorf("invalid risk %q", v.Risk)
	}
	if len(v.Graph.Nodes) == 0 {
		return fmt.Errorf("graph must contain at least one node")
	}
	nodes := make(map[string]GraphNode, len(v.Graph.Nodes))
	for _, n := range v.Graph.Nodes {
		if n.ID == "" || n.Name == "" {
			return fmt.Errorf("graph nodes require id and name")
		}
		if _, exists := nodes[n.ID]; exists {
			return fmt.Errorf("duplicate graph node %q", n.ID)
		}
		if !n.Kind.Valid() {
			return fmt.Errorf("invalid graph node kind %q", n.Kind)
		}
		if err := validateAgentBinding(n); err != nil {
			return fmt.Errorf("node %q: %w", n.ID, err)
		}
		nodes[n.ID] = n
	}
	indegree := make(map[string]int, len(nodes))
	adj := make(map[string][]string, len(nodes))
	seenEdges := make(map[string]struct{}, len(v.Graph.Edges))
	for _, e := range v.Graph.Edges {
		if e.ID == "" || e.From == "" || e.To == "" {
			return fmt.Errorf("graph edges require id, from and to")
		}
		if e.From == e.To {
			return fmt.Errorf("graph edge %q is self-referential", e.ID)
		}
		if _, ok := nodes[e.From]; !ok {
			return fmt.Errorf("graph edge %q references unknown from node %q", e.ID, e.From)
		}
		if _, ok := nodes[e.To]; !ok {
			return fmt.Errorf("graph edge %q references unknown to node %q", e.ID, e.To)
		}
		key := e.From + "\x00" + e.To
		if _, exists := seenEdges[key]; exists {
			return fmt.Errorf("duplicate graph edge %q -> %q", e.From, e.To)
		}
		seenEdges[key] = struct{}{}
		adj[e.From] = append(adj[e.From], e.To)
		indegree[e.To]++
	}
	queue := make([]string, 0, len(nodes))
	for id := range nodes {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range adj[id] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(nodes) {
		return fmt.Errorf("graph must be acyclic")
	}
	return nil
}

func validateAgentBinding(n GraphNode) error {
	if n.Kind == NodeHumanTask {
		if n.AgentBinding != nil && n.AgentBinding.Mode != "human" {
			return fmt.Errorf("human_task binding must use human mode")
		}
		return nil
	}
	if n.Kind != NodeAgentTask {
		if n.AgentBinding != nil {
			return fmt.Errorf("agent binding is only allowed on task nodes")
		}
		return nil
	}
	if n.AgentBinding == nil {
		return fmt.Errorf("agent_task requires agent_binding")
	}
	switch n.AgentBinding.Mode {
	case "fixed_employee":
		if n.AgentBinding.EmployeeID == "" {
			return fmt.Errorf("fixed_employee requires employee_id")
		}
	case "capability_pool":
		if len(n.AgentBinding.Capabilities) == 0 {
			return fmt.Errorf("capability_pool requires capabilities")
		}
	case "role_pool":
		if n.AgentBinding.Role == "" {
			return fmt.Errorf("role_pool requires role")
		}
	case "project_default":
	default:
		return fmt.Errorf("invalid agent binding mode %q", n.AgentBinding.Mode)
	}
	return nil
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
