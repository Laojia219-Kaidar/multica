// Package boundedworkspace defines the closed Phase-3 pilot contract for a
// Qwen employee that may edit one exact assigned worktree without shell, MCP,
// or network-capable tools. It uses the existing handoff_note wire field and
// does not widen the API or persistence schema.
package boundedworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const (
	MarkerNamespace = "HIVECREW_BOUNDED_WORKSPACE_"
	MarkerPrefix    = MarkerNamespace + "V2 "

	PilotID                    = "WO-C1-04-HIV719-QWEN-P3-BOUNDED-WORKSPACE-PILOT-002"
	TaskKind                   = "work"
	WorkspaceToolPolicy        = "bounded_workspace_noshell"
	ReadOnlyToolPolicy         = "bounded_read"
	WorkspaceMaxToolCalls      = 12
	ReadOnlyMaxToolCalls       = 8
	Provider                   = "qwen"
	WorktreeRoot               = "/srv/hivecosm/12-development-workspaces/users/williamdev/worktrees/"
	ExpectedUID                = 1006
	ExpectedGID                = 1006
	WorkspaceDeliveryPrefix    = "P3-BOUNDED-WORKSPACE-DELIVERY:"
	ReadOnlyDeliveryPrefix     = "P3-BOUNDED-READ-DELIVERY:"
	ServerGeneratedTaskBinding = "server_generated_uuid"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type State uint8

const (
	NotPresent State = iota
	Valid
	Invalid
)

// Contract is marshalled in this exact field order. Alternate key order,
// unknown fields, duplicate keys, whitespace, and compatibility variants are
// rejected by Parse.
type Contract struct {
	DeliveryPrefix string `json:"delivery_prefix"`
	IssueID        string `json:"issue_id"`
	MaxToolCalls   int    `json:"max_tool_calls"`
	Objective      string `json:"objective"`
	PilotID        string `json:"pilot_id"`
	Provider       string `json:"provider"`
	RequestSHA256  string `json:"request_sha256"`
	TaskBinding    string `json:"task_binding"`
	TaskKind       string `json:"task_kind"`
	ToolPolicy     string `json:"tool_policy"`
	WorkspaceID    string `json:"workspace_id"`
	Worktree       string `json:"worktree"`
}

// CanonicalMarker is the only supported external marker producer. It is
// intentionally constructible before dispatch creates the task UUID. Parse
// still requires and validates the actual server-generated task UUID.
func CanonicalMarker(toolPolicy, issueID, workspaceID, objective, worktree, requestSHA256 string) (string, error) {
	maxToolCalls, deliveryPrefix, ok := policyContract(toolPolicy)
	if !ok {
		return "", errors.New("unsupported bounded pilot tool policy")
	}
	contract := Contract{
		DeliveryPrefix: deliveryPrefix,
		IssueID:        issueID,
		MaxToolCalls:   maxToolCalls,
		Objective:      objective,
		PilotID:        PilotID,
		Provider:       Provider,
		RequestSHA256:  requestSHA256,
		TaskBinding:    ServerGeneratedTaskBinding,
		TaskKind:       TaskKind,
		ToolPolicy:     toolPolicy,
		WorkspaceID:    workspaceID,
		Worktree:       worktree,
	}
	if err := validateContract(contract); err != nil {
		return "", err
	}
	payload, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	return MarkerPrefix + string(payload), nil
}

func Parse(note, actualProvider, actualTaskKind, actualTaskID, actualIssueID, actualWorkspaceID string) (State, Contract) {
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
	if err != nil || payload != string(canonical) || validateContract(contract) != nil {
		return Invalid, Contract{}
	}
	if actualProvider != Provider || actualTaskKind != TaskKind ||
		!uuidPattern.MatchString(actualTaskID) || actualIssueID != contract.IssueID ||
		actualWorkspaceID != contract.WorkspaceID {
		return Invalid, Contract{}
	}
	return Valid, contract
}

func validateContract(contract Contract) error {
	maxToolCalls, deliveryPrefix, ok := policyContract(contract.ToolPolicy)
	if !ok || contract.DeliveryPrefix != deliveryPrefix || contract.MaxToolCalls != maxToolCalls ||
		contract.PilotID != PilotID || contract.Provider != Provider ||
		contract.TaskBinding != ServerGeneratedTaskBinding || contract.TaskKind != TaskKind {
		return errors.New("fixed bounded workspace identity mismatch")
	}
	if !uuidPattern.MatchString(contract.IssueID) || !uuidPattern.MatchString(contract.WorkspaceID) {
		return errors.New("issue and workspace identities must be lowercase UUIDs")
	}
	if !isLowerSHA256(contract.RequestSHA256) {
		return errors.New("invalid request sha256")
	}
	if contract.Objective != strings.TrimSpace(contract.Objective) || contract.Objective == "" ||
		len(contract.Objective) > 1200 || strings.ContainsAny(contract.Objective, "\x00\r") ||
		strings.Contains(contract.Objective, MarkerNamespace) {
		return errors.New("invalid bounded workspace objective")
	}
	if !validWorktreePath(contract.Worktree) {
		return errors.New("invalid bounded workspace path")
	}
	return nil
}

func policyContract(toolPolicy string) (maxToolCalls int, deliveryPrefix string, ok bool) {
	switch toolPolicy {
	case WorkspaceToolPolicy:
		return WorkspaceMaxToolCalls, WorkspaceDeliveryPrefix, true
	case ReadOnlyToolPolicy:
		return ReadOnlyMaxToolCalls, ReadOnlyDeliveryPrefix, true
	default:
		return 0, "", false
	}
}

func validWorktreePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		strings.HasPrefix(path, WorktreeRoot) && path != strings.TrimSuffix(WorktreeRoot, "/")
}

func isLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// ValidateAssignedWorktree binds the marker to the exact physical local
// directory selected by HiveCrew before StartTask performs its first write.
func ValidateAssignedWorktree(contract Contract, actual string, localDirectory bool) error {
	if !localDirectory || !validWorktreePath(actual) || actual != contract.Worktree {
		return errors.New("bounded workspace requires the exact assigned local worktree")
	}
	info, err := os.Lstat(actual)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("bounded workspace path identity invalid")
	}
	physical, err := filepath.EvalSymlinks(actual)
	if err != nil || physical != actual {
		return errors.New("bounded workspace path must be canonical and symlink-free")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != ExpectedUID || stat.Gid != ExpectedGID {
		return fmt.Errorf("bounded workspace owner identity invalid")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("bounded workspace must not be group/world writable")
	}
	return nil
}

func Prompt(contract Contract) string {
	if contract.ToolPolicy == ReadOnlyToolPolicy {
		return "You are executing the governed HiveCrew Phase-3 bounded read pilot.\n\n" +
			"Read only inside this exact assigned worktree: " + contract.Worktree + "\n" +
			"Use only read_file, glob, grep_search, and list_directory. " +
			"Do not call edit, write_file, run_shell_command, any network tool, MCP, agent, skill, or task-management tool. " +
			"Do not access home, credential, secret, runtime, daemon, Docker, systemd, package-manager, or sibling paths.\n\n" +
			"Objective: " + contract.Objective + "\n\n" +
			"Do not modify any file. Your final stdout is HiveCrew's automatic task delivery. It must be non-empty, begin exactly with " + contract.DeliveryPrefix + ", and summarize findings without including raw file contents or secrets.\n\n" +
			"Pilot ID: " + contract.PilotID + "\nRequest SHA256: " + contract.RequestSHA256 + "\n"
	}
	return "You are executing the governed HiveCrew Phase-3 bounded workspace pilot.\n\n" +
		"Work only inside this exact assigned worktree: " + contract.Worktree + "\n" +
		"Use only read_file, glob, grep_search, list_directory, edit, and write_file. " +
		"Do not call run_shell_command, any network tool, MCP, agent, skill, or task-management tool. " +
		"Do not access home, credential, secret, runtime, daemon, Docker, systemd, package-manager, or sibling paths.\n\n" +
		"Objective: " + contract.Objective + "\n\n" +
		"Make the smallest correct edits. Do not run tests yourself; a fixed trusted runner outside the model will verify them. " +
		"Your final stdout is HiveCrew's automatic task delivery. It must be non-empty, begin exactly with " + contract.DeliveryPrefix + ", and summarize changed files without including raw file contents or secrets.\n\n" +
		"Pilot ID: " + contract.PilotID + "\nRequest SHA256: " + contract.RequestSHA256 + "\n"
}

func InvalidPrompt() string {
	return "GOVERNED BOUNDED WORKSPACE CONTRACT INVALID.\n\n" +
		"Do not call any tool. Return exactly P3-BOUNDED-WORKSPACE-CONTRACT-INVALID.\n"
}

func RuntimeBrief(state State, contract Contract) string {
	if state == Invalid {
		return "# HiveCrew Bounded Workspace Contract Rejected\n\nDo not call any tool. Follow the rejection prompt exactly.\n"
	}
	if contract.ToolPolicy == ReadOnlyToolPolicy {
		return "# HiveCrew Phase-3 Bounded Read Runtime\n\n" +
			"Read only in the exact assigned worktree using read_file, glob, grep_search, and list_directory. " +
			"Edits, writes, shell, network, MCP, custom runtime inputs, and Multica CLI comments are outside this pilot.\n\n" +
			"Complete only the per-turn objective; final stdout must begin with " + contract.DeliveryPrefix + ".\n"
	}
	return "# HiveCrew Phase-3 Bounded Workspace Runtime\n\n" +
		"Work only in the exact assigned worktree using read_file, glob, grep_search, list_directory, edit, and write_file. " +
		"Shell, network, MCP, custom runtime inputs, and Multica CLI comments are outside this pilot.\n\n" +
		"Complete only the per-turn objective. The fixed trusted runner executes tests after the model; final stdout must begin with " + contract.DeliveryPrefix + ".\n"
}
