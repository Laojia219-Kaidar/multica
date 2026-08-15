package workflow

import "testing"

func validPublishedVersion() WorkflowDefinitionVersion {
	return WorkflowDefinitionVersion{
		DefinitionID: "content.wechat.v1", WorkspaceID: "00000000-0000-0000-0000-000000000001",
		Version: 1, Risk: RiskStandard,
		Graph: WorkflowGraph{
			Nodes: []GraphNode{
				{ID: "draft", Kind: NodeAgentTask, Name: "Draft", AgentBinding: &AgentBinding{Mode: "capability_pool", Capabilities: []string{"writing"}}},
				{ID: "review", Kind: NodeApproval, Name: "Review"},
			},
			Edges: []GraphEdge{{ID: "draft-review", From: "draft", To: "review"}},
		},
	}
}

func TestWorkflowDefinitionVersionRejectsInvalidPublishedGraphs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WorkflowDefinitionVersion)
	}{
		{"unknown node kind", func(v *WorkflowDefinitionVersion) { v.Graph.Nodes[0].Kind = "script" }},
		{"missing agent binding", func(v *WorkflowDefinitionVersion) { v.Graph.Nodes[0].AgentBinding = nil }},
		{"unknown edge target", func(v *WorkflowDefinitionVersion) { v.Graph.Edges[0].To = "missing" }},
		{"cycle", func(v *WorkflowDefinitionVersion) {
			v.Graph.Edges = append(v.Graph.Edges, GraphEdge{ID: "review-draft", From: "review", To: "draft"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := validPublishedVersion()
			tt.mutate(&v)
			if err := v.ValidatePublishedGraph(); err == nil {
				t.Fatal("expected publication validation error")
			}
		})
	}
}

func TestWorkflowDefinitionVersionAcceptsClosedAcyclicGraph(t *testing.T) {
	if err := validPublishedVersion().ValidatePublishedGraph(); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}
}
