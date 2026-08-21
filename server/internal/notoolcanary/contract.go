// Package notoolcanary defines the one closed prompt-policy exception used by
// the governed HIV-719 Canary-015 source candidate. It deliberately consumes
// only the existing handoff_note wire field; no API or persistence schema is
// widened.
package notoolcanary

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const (
	MarkerNamespace = "HIVECREW_NO_TOOL_CANARY_"
	MarkerPrefix    = MarkerNamespace + "V1 "

	CanaryID = "WO-C1-04-HIV719-QWEN-DGX-FRESH-CANARY-015"
	IssueID  = "ecfc6b9b-4eaf-4a74-8e05-c0065ac77a83"
	TaskKind = "work"

	Instruction    = "Without calling or requesting any tool, explain in one concise paragraph why a governed single-use canary must not retry after its authority has been consumed. Return a non-empty final response beginning exactly CANARY-015-DELIVERY:."
	DeliveryPrefix = "CANARY-015-DELIVERY:"
	ToolPolicy     = "deny"
	MaxToolCalls   = 0
	Provider       = "qwen"
)

// State distinguishes an ordinary handoff from a valid or rejected member of
// the governed no-tool marker namespace. Rejected markers never fall through
// to the ordinary tool-bearing prompt path.
type State uint8

const (
	NotPresent State = iota
	Valid
	Invalid
)

// Contract is ordered alphabetically by JSON key. Marker producers must emit
// this exact compact canonical representation; alternate key order, duplicate
// keys, whitespace, unknown fields, or compatibility variants are rejected.
type Contract struct {
	CanaryID       string `json:"canary_id"`
	DeliveryPrefix string `json:"delivery_prefix"`
	Instruction    string `json:"instruction"`
	IssueID        string `json:"issue_id"`
	MaxToolCalls   int    `json:"max_tool_calls"`
	Provider       string `json:"provider"`
	RequestSHA256  string `json:"request_sha256"`
	TaskKind       string `json:"task_kind"`
	ToolPolicy     string `json:"tool_policy"`
}

// CanonicalMarker is the single producer for the exact handoff representation
// accepted by Parse. It is intentionally limited to the future Request digest;
// every other field is fixed by this source candidate.
func CanonicalMarker(requestSHA256 string) (string, error) {
	if !isLowerSHA256(requestSHA256) {
		return "", errors.New("invalid request sha256")
	}
	contract := Contract{
		CanaryID:       CanaryID,
		DeliveryPrefix: DeliveryPrefix,
		Instruction:    Instruction,
		IssueID:        IssueID,
		MaxToolCalls:   MaxToolCalls,
		Provider:       Provider,
		RequestSHA256:  requestSHA256,
		TaskKind:       TaskKind,
		ToolPolicy:     ToolPolicy,
	}
	payload, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	return MarkerPrefix + string(payload), nil
}

// Parse accepts only the fresh Canary-015 contract on the exact HIV-719 work
// task. The future Request digest is carried in the marker and must be a lower-
// case SHA-256; its exact value is bound later by the static authorization
// package without changing this source policy.
func Parse(note, actualProvider, actualTaskKind, actualIssueID string) (State, Contract) {
	if !strings.HasPrefix(note, MarkerNamespace) {
		return NotPresent, Contract{}
	}
	if !strings.HasPrefix(note, MarkerPrefix) {
		return Invalid, Contract{}
	}
	payload := strings.TrimPrefix(note, MarkerPrefix)
	var contract Contract
	if err := json.Unmarshal([]byte(payload), &contract); err != nil {
		return Invalid, Contract{}
	}
	canonical, err := json.Marshal(contract)
	if err != nil || payload != string(canonical) {
		return Invalid, Contract{}
	}
	if contract.CanaryID != CanaryID ||
		contract.DeliveryPrefix != DeliveryPrefix ||
		contract.Instruction != Instruction ||
		contract.IssueID != IssueID ||
		contract.MaxToolCalls != MaxToolCalls ||
		contract.Provider != Provider ||
		contract.TaskKind != TaskKind ||
		contract.ToolPolicy != ToolPolicy ||
		actualTaskKind != TaskKind ||
		actualProvider != Provider ||
		actualIssueID != IssueID ||
		!isLowerSHA256(contract.RequestSHA256) {
		return Invalid, Contract{}
	}
	return Valid, contract
}

func isLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// Prompt is the complete per-turn prompt for the closed no-tool route. It is
// intentionally self-contained and carries no Multica CLI or other tool
// mandate.
func Prompt(contract Contract) string {
	return "You are executing the governed HiveCrew Canary-015 no-tool reasoning check.\n\n" +
		"Do not call, request, or simulate any tool. Do not inspect files, issue state, comments, the network, or the host. Use reasoning only.\n\n" +
		"Task: " + contract.Instruction + "\n\n" +
		"Your final stdout is the delivery captured by HiveCrew. It must be non-empty, begin exactly with " + contract.DeliveryPrefix + ", and contain your concise reasoning after that prefix. Do not add text before the prefix.\n\n" +
		"Canary ID: " + contract.CanaryID + "\n" +
		"Request SHA256: " + contract.RequestSHA256 + "\n"
}

// InvalidPrompt prevents stale, malformed, or mismatched marker attempts from
// falling back to the ordinary tool-bearing workflow.
func InvalidPrompt() string {
	return "GOVERNED NO-TOOL CANARY CONTRACT INVALID.\n\n" +
		"Do not call, request, or simulate any tool. Return exactly CANARY-015-CONTRACT-INVALID.\n"
}

// RuntimeBrief replaces the ordinary runtime workflow only for a valid or
// rejected no-tool marker. It contains no CLI inventory, issue workflow, MCP,
// repository, skill, or comment-delivery instructions.
func RuntimeBrief(state State, contract Contract) string {
	if state == Invalid {
		return "# HiveCrew No-Tool Canary Contract Rejected\n\n" +
			"Do not call, request, or simulate any tool. Follow the per-turn rejection prompt exactly.\n"
	}
	return "# HiveCrew No-Tool Canary Runtime\n\n" +
		"This is a closed, reasoning-only Canary-015 run. Do not call, request, or simulate any tool.\n\n" +
		"Complete only the exact task in the per-turn prompt. Your final stdout is the delivery; it must begin exactly with " + contract.DeliveryPrefix + ".\n"
}
