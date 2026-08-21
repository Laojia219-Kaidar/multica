package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type agentExecutionPolicy struct {
	ToolPolicy      string
	GovernedTools   bool
	SandboxRequired bool
}

type agentRuntimeExecutionPolicy struct {
	ExecutionPolicy *struct {
		Tools   string `json:"tools"`
		Sandbox string `json:"sandbox"`
	} `json:"execution_policy"`
}

// decodeAgentExecutionPolicy recognizes one deliberately narrow governed
// policy for Qwen. Unknown runtime_config fields remain provider-owned, while
// a present but partial execution_policy fails closed before any process is
// launched.
func decodeAgentExecutionPolicy(provider string, data *AgentData) (agentExecutionPolicy, error) {
	if provider != "qwen" || data == nil {
		return agentExecutionPolicy{}, nil
	}
	raw := bytes.TrimSpace(data.RuntimeConfig)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte("{}")) {
		return agentExecutionPolicy{}, nil
	}
	var cfg agentRuntimeExecutionPolicy
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return agentExecutionPolicy{}, fmt.Errorf("qwen execution policy is invalid: %w", err)
	}
	if cfg.ExecutionPolicy == nil {
		return agentExecutionPolicy{}, nil
	}
	if cfg.ExecutionPolicy.Sandbox != "required" {
		return agentExecutionPolicy{}, errors.New("qwen execution_policy sandbox must be required")
	}
	switch cfg.ExecutionPolicy.Tools {
	case "deny", "bounded_read", "bounded_workspace_noshell":
		return agentExecutionPolicy{ToolPolicy: cfg.ExecutionPolicy.Tools, GovernedTools: true, SandboxRequired: true}, nil
	default:
		return agentExecutionPolicy{}, errors.New("qwen execution_policy tools must be deny, bounded_read, or bounded_workspace_noshell")
	}
}
