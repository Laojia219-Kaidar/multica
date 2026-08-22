package daemon

import (
	"encoding/json"
	"testing"
)

func TestDecodeAgentExecutionPolicyQwenNoToolsSandbox(t *testing.T) {
	policy, err := decodeAgentExecutionPolicy("qwen", &AgentData{RuntimeConfig: json.RawMessage(`{
		"execution_policy":{"tools":"deny","sandbox":"required"},
		"unrelated":"preserved"
	}`)})
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	if !policy.GovernedTools || !policy.SandboxRequired || policy.ToolPolicy != "deny" {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestDecodeAgentExecutionPolicyQwenBoundedReadSandbox(t *testing.T) {
	policy, err := decodeAgentExecutionPolicy("qwen", &AgentData{RuntimeConfig: json.RawMessage(`{
		"execution_policy":{"tools":"bounded_read","sandbox":"required"}
	}`)})
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	if !policy.GovernedTools || !policy.SandboxRequired || policy.ToolPolicy != "bounded_read" {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestDecodeAgentExecutionPolicyQwenBoundedWorkspaceNoShell(t *testing.T) {
	policy, err := decodeAgentExecutionPolicy("qwen", &AgentData{RuntimeConfig: json.RawMessage(`{
		"execution_policy":{"tools":"bounded_workspace_noshell","sandbox":"required"}
	}`)})
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	if !policy.GovernedTools || !policy.SandboxRequired || policy.ToolPolicy != "bounded_workspace_noshell" {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestDecodeAgentExecutionPolicyQwenWorkspaceDevelopment(t *testing.T) {
	policy, err := decodeAgentExecutionPolicy("qwen", &AgentData{RuntimeConfig: json.RawMessage(`{
		"execution_policy":{"tools":"bounded_workspace","sandbox":"required"}
	}`)})
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	if !policy.GovernedTools || !policy.SandboxRequired || policy.ToolPolicy != "bounded_workspace" {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestDecodeAgentExecutionPolicyRejectsPartialQwenPolicy(t *testing.T) {
	for _, raw := range []string{
		`{"execution_policy":{"tools":"deny"}}`,
		`{"execution_policy":{"tools":"allow","sandbox":"required"}}`,
		`{"execution_policy":{"tools":"bounded_workspace_shell","sandbox":"required"}}`,
		`{"execution_policy":{"tools":"bounded_read","sandbox":"optional"}}`,
		`{"execution_policy":{"tools":"deny","sandbox":"optional"}}`,
	} {
		if _, err := decodeAgentExecutionPolicy("qwen", &AgentData{RuntimeConfig: json.RawMessage(raw)}); err == nil {
			t.Fatalf("partial policy %s did not fail closed", raw)
		}
	}
}
