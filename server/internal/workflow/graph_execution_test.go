package workflow

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func graphVersionForExecution() WorkflowDefinitionVersion {
	return WorkflowDefinitionVersion{
		DefinitionID: "content.wechat",
		WorkspaceID:  "workspace-1",
		ProjectID:    "project-wechat",
		Version:      3,
		Risk:         RiskStandard,
		Graph: WorkflowGraph{
			Nodes: []GraphNode{
				{ID: "draft", Kind: NodeAgentTask, Name: "Draft", AgentBinding: &AgentBinding{Mode: "fixed_employee", EmployeeID: "emp-writer"}},
				{ID: "review", Kind: NodeApproval, Name: "Review"},
				{ID: "publish", Kind: NodeHumanTask, Name: "Publish", AgentBinding: &AgentBinding{Mode: "human"}},
			},
			Edges: []GraphEdge{
				{ID: "draft-review", From: "draft", To: "review"},
				{ID: "review-publish", From: "review", To: "publish"},
			},
		},
	}
}

func executionCommand(v WorkflowDefinitionVersion) GraphExecutionCommand {
	return GraphExecutionCommand{
		Definition: v,
		Scope: GraphExecutionScope{
			WorkspaceID: "workspace-1",
			ProjectID:   "project-wechat",
		},
		InstanceID:     "instance-1",
		IdempotencyKey: "start-1",
		Context:        ContextRef{ProjectID: "project-wechat"},
	}
}

func TestCompileDefinitionVersionProducesDeterministicStagePlan(t *testing.T) {
	v := graphVersionForExecution()
	plan, err := CompileDefinitionVersion(v, GraphExecutionScope{
		WorkspaceID: "workspace-1",
		ProjectID:   "project-wechat",
	}, ContextRef{ProjectID: "project-wechat"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if plan.WorkspaceID != "workspace-1" || plan.ProjectID != "project-wechat" {
		t.Fatalf("scope was not retained: %+v", plan)
	}
	if got, want := len(plan.Stages), 3; got != want {
		t.Fatalf("stage count = %d, want %d", got, want)
	}
	for i, want := range []struct {
		id   string
		kind NodeKind
	}{
		{id: "draft", kind: NodeAgentTask},
		{id: "review", kind: NodeApproval},
		{id: "publish", kind: NodeHumanTask},
	} {
		if plan.Stages[i].NodeID != want.id || plan.Stages[i].Kind != want.kind {
			t.Fatalf("stage %d = %+v, want %s/%s", i, plan.Stages[i], want.id, want.kind)
		}
	}
	if plan.Stages[0].Binding == nil || plan.Stages[0].Binding.EmployeeID != "emp-writer" {
		t.Fatalf("agent binding was not preserved: %+v", plan.Stages[0].Binding)
	}
	if !plan.Stages[1].RequiresReviewEvidence {
		t.Fatal("approval stage must require explicit review evidence")
	}
	if plan.Definition.ID != v.DefinitionID || plan.Definition.Version != v.Version {
		t.Fatalf("runtime definition identity was changed: %+v", plan.Definition)
	}
	if err := ValidateDefinition(plan.Definition); err != nil {
		t.Fatalf("compiled definition is not executable by stage engine: %v", err)
	}
}

func TestCompileDefinitionVersionRejectsUnsafeDAGShapes(t *testing.T) {
	base := graphVersionForExecution()
	tests := []struct {
		name   string
		mutate func(*WorkflowDefinitionVersion)
	}{
		{
			name: "fan out",
			mutate: func(v *WorkflowDefinitionVersion) {
				v.Graph.Nodes = append(v.Graph.Nodes, GraphNode{ID: "alt", Kind: NodeAgentTask, Name: "Alt", AgentBinding: &AgentBinding{Mode: "project_default"}})
				v.Graph.Edges = append(v.Graph.Edges, GraphEdge{ID: "draft-alt", From: "draft", To: "alt"})
			},
		},
		{
			name: "fan in",
			mutate: func(v *WorkflowDefinitionVersion) {
				v.Graph.Nodes = append(v.Graph.Nodes, GraphNode{ID: "alt", Kind: NodeAgentTask, Name: "Alt", AgentBinding: &AgentBinding{Mode: "project_default"}})
				v.Graph.Edges = append(v.Graph.Edges, GraphEdge{ID: "alt-publish", From: "alt", To: "publish"})
			},
		},
		{
			name: "conditional edge",
			mutate: func(v *WorkflowDefinitionVersion) {
				v.Graph.Edges[0].When = "approved"
			},
		},
		{
			name: "decision node",
			mutate: func(v *WorkflowDefinitionVersion) {
				v.Graph.Nodes[1] = GraphNode{ID: "decision", Kind: NodeDecision, Name: "Choose"}
				v.Graph.Edges[0].To = "decision"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := base
			v.Graph.Nodes = append([]GraphNode(nil), base.Graph.Nodes...)
			v.Graph.Edges = append([]GraphEdge(nil), base.Graph.Edges...)
			tt.mutate(&v)
			_, err := CompileDefinitionVersion(v, GraphExecutionScope{WorkspaceID: "workspace-1", ProjectID: "project-wechat"}, ContextRef{ProjectID: "project-wechat"})
			if err == nil {
				t.Fatal("unsafe graph must be rejected")
			}
			if !errors.Is(err, ErrGraphExecutionUnsupported) && !errors.Is(err, ErrInvalidDefinition) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestStartPublishedGraphAndAdvanceFailClosed(t *testing.T) {
	v := graphVersionForExecution()
	e := NewEngine()
	inst, receipt, plan, err := e.StartPublishedGraph(executionCommand(v))
	if err != nil {
		t.Fatalf("start published graph: %v", err)
	}
	if !receipt.Accepted || !receipt.Changed || inst.DefinitionID != v.DefinitionID || inst.DefinitionVersion != v.Version {
		t.Fatalf("unexpected start result: inst=%+v receipt=%+v", inst, receipt)
	}
	if plan.Stages[0].Binding == nil || plan.Stages[0].Binding.EmployeeID != "emp-writer" {
		t.Fatal("start plan lost agent binding")
	}
	// Draft advances with the definition's STANDARD review gate.
	if _, _, err := e.Advance(inst.ID, AdvanceEvidence{ReviewPassed: true}, "advance-1"); err != nil {
		t.Fatalf("draft advance: %v", err)
	}
	// Approval requires review even though the definition is STANDARD and the
	// generic risk gate would otherwise be enough only when ReviewPassed=true.
	inst, rejected, err := e.Advance(inst.ID, AdvanceEvidence{}, "advance-2")
	if err != nil {
		t.Fatalf("approval rejection must be a receipt, not an error: %v", err)
	}
	if rejected.Accepted || inst.StageIndex != 1 || rejected.Reason == "" {
		t.Fatalf("approval must fail closed: inst=%+v receipt=%+v", inst, rejected)
	}
	events := e.Events(inst.ID)
	if len(events) == 0 || events[len(events)-1].Kind != "workflow.advance_rejected" || events[len(events)-1].IdempotencyKey != "advance-2" {
		t.Fatalf("rejected approval must append a visible control event: %+v", events)
	}
	inst, accepted, err := e.Advance(inst.ID, AdvanceEvidence{ReviewPassed: true}, "advance-3")
	if err != nil || !accepted.Accepted || inst.StageIndex != 2 {
		t.Fatalf("review evidence should advance approval: inst=%+v receipt=%+v err=%v", inst, accepted, err)
	}
}

func TestStartPublishedGraphRejectsScopeMismatch(t *testing.T) {
	v := graphVersionForExecution()
	cmd := executionCommand(v)
	cmd.Context.ProjectID = "other-project"
	_, _, _, err := NewEngine().StartPublishedGraph(cmd)
	if !errors.Is(err, ErrGraphExecutionScope) {
		t.Fatalf("scope mismatch error = %v, want ErrGraphExecutionScope", err)
	}
}

func TestCompileDefinitionVersionRejectsPublishedScopeMismatch(t *testing.T) {
	v := graphVersionForExecution()
	_, err := CompileDefinitionVersion(v, GraphExecutionScope{WorkspaceID: "workspace-other", ProjectID: "project-wechat"}, ContextRef{ProjectID: "project-wechat"})
	if !errors.Is(err, ErrGraphExecutionScope) {
		t.Fatalf("workspace mismatch error = %v, want ErrGraphExecutionScope", err)
	}
	v = graphVersionForExecution()
	_, err = CompileDefinitionVersion(v, GraphExecutionScope{WorkspaceID: "workspace-1", ProjectID: "project-other"}, ContextRef{ProjectID: "project-other"})
	if !errors.Is(err, ErrGraphExecutionScope) {
		t.Fatalf("project mismatch error = %v, want ErrGraphExecutionScope", err)
	}
}

func TestDecisionStageRequiresExplicitEvidence(t *testing.T) {
	v := graphVersionForExecution()
	v.Risk = RiskFast
	v.Graph.Nodes[1] = GraphNode{ID: "decision", Kind: NodeDecision, Name: "Choose"}
	v.Graph.Edges[0].To = "decision"
	v.Graph.Edges[1].From = "decision"
	// Keep the decision linear: conditional routing and fan-out are rejected by
	// the compiler, while the decision evidence gate remains testable.
	e := NewEngine()
	inst, _, _, err := e.StartPublishedGraph(executionCommand(v))
	if err != nil {
		t.Fatalf("start decision graph: %v", err)
	}
	if _, _, err = e.Advance(inst.ID, AdvanceEvidence{}, "decision-draft"); err != nil {
		t.Fatalf("draft advance: %v", err)
	}
	inst, receipt, err := e.Advance(inst.ID, AdvanceEvidence{}, "decision-missing")
	if err != nil || receipt.Accepted || inst.StageIndex != 1 {
		t.Fatalf("decision must fail closed without evidence: inst=%+v receipt=%+v err=%v", inst, receipt, err)
	}
	inst, receipt, err = e.Advance(inst.ID, AdvanceEvidence{DecisionOutcome: "publish"}, "decision-present")
	if err != nil || !receipt.Accepted || inst.StageIndex != 2 {
		t.Fatalf("decision evidence should advance linear decision: inst=%+v receipt=%+v err=%v", inst, receipt, err)
	}
	inst, receipt, err = e.Advance(inst.ID, AdvanceEvidence{}, "decision-publish")
	if err != nil || !receipt.Accepted || inst.Status != StatusCompleted {
		t.Fatalf("final stage should complete: inst=%+v receipt=%+v err=%v", inst, receipt, err)
	}
	execs := e.StageExecutions(inst.ID)
	if len(execs) != 3 || execs[1].DecisionOutcome != "publish" {
		t.Fatalf("decision evidence not retained: %+v", execs)
	}
}

func TestPublishedGraphVersionCannotBeReplacedAndPinsRunningInstance(t *testing.T) {
	v3 := graphVersionForExecution()
	e := NewEngine()
	first, _, _, err := e.StartPublishedGraph(executionCommand(v3))
	if err != nil {
		t.Fatalf("start v3: %v", err)
	}
	v4 := graphVersionForExecution()
	v4.Version = 4
	v4.Graph.Nodes[0].Name = "Draft v4"
	cmd4 := executionCommand(v4)
	cmd4.InstanceID = "instance-v4"
	cmd4.IdempotencyKey = "start-v4"
	if _, _, _, err := e.StartPublishedGraph(cmd4); err != nil {
		t.Fatalf("start v4: %v", err)
	}
	// A different graph under an already-published immutable version is not
	// accepted, even if the stage count happens to remain the same.
	replaced := v3
	replaced.Graph.Nodes[0].Name = "Tampered"
	cmdTampered := executionCommand(replaced)
	cmdTampered.InstanceID = "instance-tampered"
	cmdTampered.IdempotencyKey = "start-tampered"
	if _, _, _, err := e.StartPublishedGraph(cmdTampered); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("tampered version error = %v, want ErrIllegalTransition", err)
	}
	// The v3 instance remains pinned to Draft, not the v4 stage.
	first, receipt, err := e.Advance(first.ID, AdvanceEvidence{ReviewPassed: true}, "v3-advance")
	if err != nil || !receipt.Accepted {
		t.Fatalf("advance v3: inst=%+v receipt=%+v err=%v", first, receipt, err)
	}
	if execs := e.StageExecutions(first.ID); len(execs) != 1 || execs[0].StageName != "Draft" {
		t.Fatalf("v3 instance was not pinned to its version: %+v", execs)
	}
}

func workspaceGraphVersion(workspaceID, projectID, draftName string) WorkflowDefinitionVersion {
	v := graphVersionForExecution()
	v.WorkspaceID = workspaceID
	v.ProjectID = projectID
	v.Graph.Nodes[0].Name = draftName
	return v
}

func TestPublishedGraphsWithSameDefinitionVersionStayWorkspaceScopedConcurrently(t *testing.T) {
	v1 := workspaceGraphVersion("workspace-1", "project-one", "Draft One")
	v2 := workspaceGraphVersion("workspace-2", "project-two", "Draft Two")
	e := NewEngine()

	commands := []GraphExecutionCommand{
		{Definition: v1, Scope: GraphExecutionScope{WorkspaceID: v1.WorkspaceID, ProjectID: v1.ProjectID}, Context: ContextRef{ProjectID: v1.ProjectID}, InstanceID: "instance-one", IdempotencyKey: "start-one"},
		{Definition: v2, Scope: GraphExecutionScope{WorkspaceID: v2.WorkspaceID, ProjectID: v2.ProjectID}, Context: ContextRef{ProjectID: v2.ProjectID}, InstanceID: "instance-two", IdempotencyKey: "start-two"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(commands))
	for _, cmd := range commands {
		cmd := cmd
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, receipt, _, err := e.StartPublishedGraph(cmd); err != nil || !receipt.Accepted {
				errs <- fmt.Errorf("start %s: receipt=%+v err=%v", cmd.Scope.WorkspaceID, receipt, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	for _, want := range []struct {
		instanceID string
		stageName  string
	}{
		{instanceID: "instance-one", stageName: "Draft One"},
		{instanceID: "instance-two", stageName: "Draft Two"},
	} {
		if _, receipt, err := e.Advance(want.instanceID, AdvanceEvidence{ReviewPassed: true}, "advance-"+want.instanceID); err != nil || !receipt.Accepted {
			t.Fatalf("advance %s: receipt=%+v err=%v", want.instanceID, receipt, err)
		}
		execs := e.StageExecutions(want.instanceID)
		if len(execs) != 1 || execs[0].StageName != want.stageName {
			t.Fatalf("workspace graph crossed at %s: %+v", want.instanceID, execs)
		}
	}
}

func TestHydratePublishedGraphsWithSameDefinitionVersionStayWorkspaceScoped(t *testing.T) {
	v1 := workspaceGraphVersion("workspace-1", "project-one", "Hydrated One")
	v2 := workspaceGraphVersion("workspace-2", "project-two", "Hydrated Two")
	e := NewEngine()
	versions := []struct {
		version WorkflowDefinitionVersion
		ctx     ContextRef
	}{
		{version: v1, ctx: ContextRef{ProjectID: v1.ProjectID}},
		{version: v2, ctx: ContextRef{ProjectID: v2.ProjectID}},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(versions))
	for _, item := range versions {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.hydratePublishedGraphDefinition(item.version, GraphExecutionScope{WorkspaceID: item.version.WorkspaceID, ProjectID: item.version.ProjectID}, item.ctx)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	for _, item := range versions {
		instanceID := "hydrated-" + item.version.WorkspaceID
		if _, _, err := e.startVersionForWorkspace(item.version.DefinitionID, item.version.Version, instanceID, item.ctx, item.version.WorkspaceID, "hydrate-start-"+item.version.WorkspaceID); err != nil {
			t.Fatalf("start hydrated %s: %v", item.version.WorkspaceID, err)
		}
		if _, receipt, err := e.Advance(instanceID, AdvanceEvidence{ReviewPassed: true}, "hydrate-advance-"+item.version.WorkspaceID); err != nil || !receipt.Accepted {
			t.Fatalf("advance hydrated %s: receipt=%+v err=%v", item.version.WorkspaceID, receipt, err)
		}
		execs := e.StageExecutions(instanceID)
		if len(execs) != 1 || execs[0].StageName != item.version.Graph.Nodes[0].Name {
			t.Fatalf("hydrated workspace graph crossed at %s: %+v", item.version.WorkspaceID, execs)
		}
	}
}
