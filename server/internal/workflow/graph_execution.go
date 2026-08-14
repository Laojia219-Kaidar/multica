package workflow

import (
	"errors"
	"fmt"
)

var (
	// ErrGraphExecutionScope means that a candidate command did not carry a
	// complete, internally consistent workspace/project scope.
	ErrGraphExecutionScope = errors.New("workflow graph execution scope is invalid")
	// ErrGraphExecutionUnsupported means that the graph requires runtime
	// semantics the existing sequential stage engine cannot preserve.
	ErrGraphExecutionUnsupported = errors.New("workflow graph execution is unsupported")
)

// GraphExecutionScope is the execution boundary supplied by the authorized
// HTTP command. The compiler does not resolve or copy Project state; it only
// requires the caller to bind the run to one explicit workspace and project.
type GraphExecutionScope struct {
	WorkspaceID string
	ProjectID   string
}

// GraphExecutionCommand is the explicit candidate command for starting one
// published graph. Instance and idempotency identifiers are required here so
// this package never invents durable command identity.
type GraphExecutionCommand struct {
	Definition     WorkflowDefinitionVersion
	Scope          GraphExecutionScope
	Context        ContextRef
	InstanceID     string
	IdempotencyKey string
}

// GraphStagePlan is the lossless subset of a graph node that the existing
// stage engine can execute. Binding is copied from the published graph and is
// never resolved to a second employee registry here.
type GraphStagePlan struct {
	NodeID                   string
	Name                     string
	Kind                     NodeKind
	Binding                  *AgentBinding
	RequiresReviewEvidence   bool
	RequiresDecisionEvidence bool
}

// GraphExecutionPlan contains both the owner-facing graph identity and the
// ordered stage definition consumed by Engine. It is a candidate execution
// plan, not a formal Outcome or a publication record.
type GraphExecutionPlan struct {
	WorkspaceID       string
	ProjectID         string
	DefinitionID      string
	DefinitionVersion int
	Risk              RiskTier
	Stages            []GraphStagePlan
	Definition        WorkflowDefinition
}

// CompileDefinitionVersion validates and compiles one published graph into a
// deterministic linear stage plan. Fan-out, fan-in and conditional edges are
// rejected instead of being silently flattened by the sequential engine.
func CompileDefinitionVersion(v WorkflowDefinitionVersion, scope GraphExecutionScope, ctx ContextRef) (GraphExecutionPlan, error) {
	if scope.WorkspaceID == "" || scope.ProjectID == "" || ctx.ProjectID == "" || ctx.ProjectID != scope.ProjectID {
		return GraphExecutionPlan{}, fmt.Errorf("%w: workspace, project and matching context project are required", ErrGraphExecutionScope)
	}
	if v.WorkspaceID != "" && v.WorkspaceID != scope.WorkspaceID {
		return GraphExecutionPlan{}, fmt.Errorf("%w: definition belongs to another workspace", ErrGraphExecutionScope)
	}
	if v.ProjectID != "" && v.ProjectID != scope.ProjectID {
		return GraphExecutionPlan{}, fmt.Errorf("%w: definition belongs to another project", ErrGraphExecutionScope)
	}
	if v.WorkspaceID != "" {
		if err := v.ValidatePublishedGraph(); err != nil {
			return GraphExecutionPlan{}, fmt.Errorf("%w: published graph validation failed: %v", ErrInvalidDefinition, err)
		}
	}
	if err := ValidateGraph(v); err != nil {
		return GraphExecutionPlan{}, err
	}

	nodes := make(map[string]GraphNode, len(v.Graph.Nodes))
	inDegree := make(map[string]int, len(v.Graph.Nodes))
	outDegree := make(map[string]int, len(v.Graph.Nodes))
	byFrom := make(map[string]GraphEdge, len(v.Graph.Edges))
	for _, node := range v.Graph.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range v.Graph.Edges {
		if edge.When != "" {
			return GraphExecutionPlan{}, fmt.Errorf("%w: conditional edge %q requires a decision router", ErrGraphExecutionUnsupported, edge.ID)
		}
		key := edge.From + "\x00" + edge.To
		if _, exists := byFrom[key]; exists {
			return GraphExecutionPlan{}, fmt.Errorf("%w: duplicate edge %q", ErrInvalidDefinition, edge.ID)
		}
		byFrom[key] = edge
		inDegree[edge.To]++
		outDegree[edge.From]++
		if outDegree[edge.From] > 1 {
			return GraphExecutionPlan{}, fmt.Errorf("%w: fan-out at node %q", ErrGraphExecutionUnsupported, edge.From)
		}
		if inDegree[edge.To] > 1 {
			return GraphExecutionPlan{}, fmt.Errorf("%w: fan-in at node %q", ErrGraphExecutionUnsupported, edge.To)
		}
	}

	roots := make([]string, 0, 1)
	sinks := make([]string, 0, 1)
	for _, node := range v.Graph.Nodes {
		if inDegree[node.ID] == 0 {
			roots = append(roots, node.ID)
		}
		if outDegree[node.ID] == 0 {
			sinks = append(sinks, node.ID)
		}
	}
	if len(roots) != 1 || len(sinks) != 1 {
		return GraphExecutionPlan{}, fmt.Errorf("%w: graph requires one root and one sink, got %d/%d", ErrGraphExecutionUnsupported, len(roots), len(sinks))
	}

	ordered := make([]GraphNode, 0, len(nodes))
	seen := make(map[string]bool, len(nodes))
	current := roots[0]
	for len(ordered) < len(nodes) {
		if seen[current] {
			return GraphExecutionPlan{}, fmt.Errorf("%w: graph traversal repeated node %q", ErrInvalidDefinition, current)
		}
		node, ok := nodes[current]
		if !ok {
			return GraphExecutionPlan{}, fmt.Errorf("%w: missing node %q", ErrInvalidDefinition, current)
		}
		seen[current] = true
		ordered = append(ordered, node)
		if outDegree[current] == 0 {
			break
		}
		for _, edge := range v.Graph.Edges {
			if edge.From == current {
				current = edge.To
				break
			}
		}
	}
	if len(ordered) != len(nodes) {
		return GraphExecutionPlan{}, fmt.Errorf("%w: graph is disconnected", ErrGraphExecutionUnsupported)
	}
	// ValidateGraph already checks cycles. This explicit check ensures the
	// deterministic single-chain walk also reached the one declared sink.
	if ordered[len(ordered)-1].ID != sinks[0] {
		return GraphExecutionPlan{}, fmt.Errorf("%w: graph walk did not reach sink", ErrInvalidDefinition)
	}

	stages := make([]GraphStagePlan, 0, len(ordered))
	engineStages := make([]Stage, 0, len(ordered))
	for _, node := range ordered {
		binding, err := cloneRuntimeBinding(node)
		if err != nil {
			return GraphExecutionPlan{}, err
		}
		stage := GraphStagePlan{
			NodeID:                   node.ID,
			Name:                     node.Name,
			Kind:                     node.Kind,
			Binding:                  binding,
			RequiresReviewEvidence:   node.Kind == NodeApproval,
			RequiresDecisionEvidence: node.Kind == NodeDecision,
		}
		stages = append(stages, stage)
		engineStages = append(engineStages, Stage{Name: node.Name})
	}
	return GraphExecutionPlan{
		WorkspaceID:       scope.WorkspaceID,
		ProjectID:         scope.ProjectID,
		DefinitionID:      v.DefinitionID,
		DefinitionVersion: v.Version,
		Risk:              v.Risk,
		Stages:            stages,
		Definition: WorkflowDefinition{
			ID: v.DefinitionID, Version: v.Version, Risk: v.Risk, Stages: engineStages,
		},
	}, nil
}

// CompilePublishedGraph is the descriptive alias used by integration code.
func CompilePublishedGraph(v WorkflowDefinitionVersion, scope GraphExecutionScope, ctx ContextRef) (GraphExecutionPlan, error) {
	return CompileDefinitionVersion(v, scope, ctx)
}

func cloneRuntimeBinding(node GraphNode) (*AgentBinding, error) {
	if node.Kind == NodeApproval {
		// Approval authority is supplied by the risk gate/evidence, not by an
		// arbitrary agent binding. A binding on an approval node would be a
		// second, ambiguous authority and is rejected.
		if node.AgentBinding != nil {
			return nil, fmt.Errorf("%w: approval node %q cannot carry an agent binding", ErrGraphExecutionScope, node.ID)
		}
		return nil, nil
	}
	if node.Kind == NodeDecision {
		// A linear decision is still executable only with explicit evidence; a
		// conditional edge was already rejected above.
		if node.AgentBinding != nil {
			return nil, fmt.Errorf("%w: decision node %q cannot carry an agent binding", ErrGraphExecutionScope, node.ID)
		}
		return nil, nil
	}
	if node.Kind == NodeAgentTask {
		if node.AgentBinding == nil {
			return nil, fmt.Errorf("%w: agent node %q requires a binding", ErrGraphExecutionScope, node.ID)
		}
		binding := *node.AgentBinding
		binding.Capabilities = append([]string(nil), node.AgentBinding.Capabilities...)
		switch binding.Mode {
		case "fixed_employee":
			if binding.EmployeeID == "" {
				return nil, fmt.Errorf("%w: fixed employee binding is empty for node %q", ErrGraphExecutionScope, node.ID)
			}
		case "capability_pool":
			if len(binding.Capabilities) == 0 {
				return nil, fmt.Errorf("%w: capability pool binding is empty for node %q", ErrGraphExecutionScope, node.ID)
			}
		case "role_pool":
			if binding.Role == "" {
				return nil, fmt.Errorf("%w: role pool binding is empty for node %q", ErrGraphExecutionScope, node.ID)
			}
		case "project_default":
		default:
			return nil, fmt.Errorf("%w: unsupported agent binding mode %q", ErrGraphExecutionScope, binding.Mode)
		}
		return &binding, nil
	}
	if node.Kind == NodeHumanTask && node.AgentBinding != nil {
		if node.AgentBinding.Mode != "human" {
			return nil, fmt.Errorf("%w: human node %q must use human binding mode", ErrGraphExecutionScope, node.ID)
		}
		binding := *node.AgentBinding
		binding.Capabilities = append([]string(nil), node.AgentBinding.Capabilities...)
		return &binding, nil
	}
	return nil, nil
}

// StartPublishedGraph compiles and starts a graph without advancing it. The
// returned plan is the execution receipt for the graph-to-stage translation;
// the normal Engine Advance command remains the only way to execute stages.
func (e *Engine) StartPublishedGraph(cmd GraphExecutionCommand) (WorkflowInstance, Receipt, GraphExecutionPlan, error) {
	if cmd.InstanceID == "" || cmd.IdempotencyKey == "" {
		return WorkflowInstance{}, Receipt{}, GraphExecutionPlan{}, fmt.Errorf("%w: instance and idempotency keys are required", ErrGraphExecutionScope)
	}
	plan, err := CompileDefinitionVersion(cmd.Definition, cmd.Scope, cmd.Context)
	if err != nil {
		return WorkflowInstance{}, Receipt{}, GraphExecutionPlan{}, err
	}
	if err := e.registerGraphPlan(plan); err != nil {
		return WorkflowInstance{}, Receipt{}, GraphExecutionPlan{}, err
	}
	inst, receipt, err := e.startVersionForWorkspace(plan.Definition.ID, plan.Definition.Version, cmd.InstanceID, cmd.Context, plan.WorkspaceID, cmd.IdempotencyKey)
	if err != nil {
		return WorkflowInstance{}, Receipt{}, GraphExecutionPlan{}, err
	}
	return inst, receipt, plan, nil
}
