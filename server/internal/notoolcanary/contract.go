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

	ZaraCanaryID       = "WO-C1-04-ZARA-QODERCN-STRICT-CANARY-R4"
	ZaraIssueID        = "3ec06127-a2e7-46b0-8ae8-115a97fe9a23"
	ZaraTaskKind       = "work"
	ZaraInstruction    = "Return exactly ZARA_QODERCN_STRICT_R4_20260824_BE5B39F9_OK and nothing else."
	ZaraDeliveryPrefix = "ZARA_QODERCN_STRICT_R4_20260824_BE5B39F9_OK"
	ZaraToolPolicy     = "deny"
	ZaraMaxToolCalls   = 0
	ZaraProvider       = "qoder"
	ZaraRequestSHA256  = "c2b257f7b1cc94fea8463464b6aaea53904e2d53d5b3541650b5a6d0a912f098"
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

func zaraContract() Contract {
	return Contract{
		CanaryID:       ZaraCanaryID,
		DeliveryPrefix: ZaraDeliveryPrefix,
		Instruction:    ZaraInstruction,
		IssueID:        ZaraIssueID,
		MaxToolCalls:   ZaraMaxToolCalls,
		Provider:       ZaraProvider,
		RequestSHA256:  ZaraRequestSHA256,
		TaskKind:       ZaraTaskKind,
		ToolPolicy:     ZaraToolPolicy,
	}
}

// IsSingleUseIssue identifies the exact source-bound Canary whose authority
// permits one run only. Backend retry eligibility uses this same constant so a
// failed first attempt cannot produce an automatic retry child.
func IsSingleUseIssue(issueID string) bool {
	return issueID == ZaraIssueID
}

// Parse accepts the legacy marker-bound Canary-015 contract on HIV-719 and the
// source-bound Zara R4 contract on HIV-964. The former carries a lower-case
// Request digest in its marker; the latter binds its exact digest in source
// because the existing assignment API has no handoff-note input.
func Parse(note, actualProvider, actualTaskKind, actualIssueID string) (State, Contract) {
	// HIV-964 cannot carry a handoff marker through the existing assignment API,
	// so its request digest and full policy are compiled into this source
	// candidate. Bind the exception to all three live fields and reject every
	// mismatch instead of falling through to the ordinary tool-bearing route.
	if IsSingleUseIssue(actualIssueID) {
		if actualProvider != ZaraProvider ||
			actualTaskKind != ZaraTaskKind ||
			note != "" {
			return Invalid, Contract{}
		}
		return Valid, zaraContract()
	}
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
	if contract.CanaryID == ZaraCanaryID {
		return "You are executing the governed HiveCrew Zara Qoder CN strict no-tool Canary R4.\n\n" +
			"Tools are denied and max_tool_calls is 0. Do not call, request, or simulate any tool. Do not inspect files, issue state, comments, MCP, the network, or the host.\n\n" +
			contract.Instruction + "\n\n" +
			"Canary ID: " + contract.CanaryID + "\n" +
			"Request SHA256: " + contract.RequestSHA256 + "\n"
	}
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
	if contract.CanaryID == ZaraCanaryID {
		return "# HiveCrew Zara Qoder CN Strict No-Tool Canary R4\n\n" +
			"This is a closed single-use run. Tools are denied and max_tool_calls is 0. Do not call, request, or simulate any tool.\n\n" +
			"Follow only the per-turn prompt and return exactly " + contract.DeliveryPrefix + ".\n"
	}
	return "# HiveCrew No-Tool Canary Runtime\n\n" +
		"This is a closed, reasoning-only Canary-015 run. Do not call, request, or simulate any tool.\n\n" +
		"Complete only the exact task in the per-turn prompt. Your final stdout is the delivery; it must begin exactly with " + contract.DeliveryPrefix + ".\n"
}
