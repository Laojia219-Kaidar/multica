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
	if !policy.NoTools || !policy.SandboxRequired || policy.ToolPolicy != "deny" {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestDecodeAgentExecutionPolicyRejectsPartialQwenPolicy(t *testing.T) {
	for _, raw := range []string{
		`{"execution_policy":{"tools":"deny"}}`,
		`{"execution_policy":{"tools":"allow","sandbox":"required"}}`,
		`{"execution_policy":{"tools":"deny","sandbox":"optional"}}`,
	} {
		if _, err := decodeAgentExecutionPolicy("qwen", &AgentData{RuntimeConfig: json.RawMessage(raw)}); err == nil {
			t.Fatalf("partial policy %s did not fail closed", raw)
		}
	}
}
