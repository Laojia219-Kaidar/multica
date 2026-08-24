// Package orcabridge implements the HiveCrew -> Orca execution bridge
// contract (WO-P2-ORCA-BRIDGE A1).
//
// Authority boundaries:
//
//   - HiveCrew stays the control truth for the Project -> Issue -> Task ->
//     Assignment -> Run chain. The bridge only maps that chain onto the Orca
//     orchestration chain Run -> Task -> Dispatch -> Worker (supervised agent
//     terminal inside an isolated worktree) and appends governed result
//     evidence back into HiveCrew.
//   - Orca owns placement, terminal, and dispatch lifecycle mechanics. The
//     bridge never treats Orca state as company truth; it reconciles Orca
//     identifiers by deterministic provenance markers so crashed or replayed
//     bridge calls converge to exactly one Orca object per HiveCrew object.
//
// Mapping (one Orca object per HiveCrew object, all idempotent):
//
//	HiveCrew project        -> Orca Run namespace
//	HiveCrew issue          -> provenance inside the Orca Task spec (no object)
//	HiveCrew task           -> Orca Task
//	HiveCrew assignment cmd -> Orca Dispatch
//	HiveCrew run attempt    -> Orca Worker (terminal + isolated worktree)
//
// This package is intentionally self-contained: it adds no edits to existing
// services, handlers, or generated code, so it can land under an isolated
// non-overlapping writer scope.
package orcabridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ContractVersion freezes the mapping semantics. Markers, digests, and table
// rows produced by one version are only reconcilable inside that version; a
// future incompatible mapping must start a new version and new mapping rows.
const ContractVersion = "hivecrew-orca-bridge/v1"

// MarkerPrefix opens the machine-readable provenance marker embedded in Orca
// run objectives and task specs. Reconciliation scans for this exact prefix.
const MarkerPrefix = "[hivecrew-orca-bridge/v1"

// Sentinel errors.
var (
	// ErrInvalidChain means a HiveCrew chain reference failed closed
	// validation.
	ErrInvalidChain = errors.New("orcabridge: invalid HiveCrew chain reference")
	// ErrMappingConflict means the same mapping scope is already committed on
	// the existing work chain with a different immutable payload digest. The
	// caller must fail closed.
	ErrMappingConflict = errors.New("orcabridge: mapping scope already committed with a different payload digest")
	// ErrNotBridgeManaged means the HiveCrew object carries no bridge
	// assignment linkage, so the bridge refuses to manage it.
	ErrNotBridgeManaged = errors.New("orcabridge: object is not managed by this bridge")
)

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	// Orca handle grammars observed from the Orca CLI (run_*, task_*, ctx_*,
	// term_* UUIDs, wtr_*). Values are interpolated as single argv entries,
	// never through a shell; the grammar check additionally blocks values
	// that could be mistaken for CLI flags.
	orcaRunPattern       = regexp.MustCompile(`^run_[a-z0-9]+$`)
	orcaTaskPattern      = regexp.MustCompile(`^task_[a-z0-9]+$`)
	orcaDispatchPattern  = regexp.MustCompile(`^ctx_[a-z0-9]+$`)
	orcaTerminalPattern  = regexp.MustCompile(`^term_[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	orcaMessagePattern   = regexp.MustCompile(`^msg_[a-z0-9]+$`)
	orcaWorktreePattern  = regexp.MustCompile(`^wtr_[a-z0-9]+$`)
	orcaWorktreeNamePart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Chain is the HiveCrew control-truth lineage that authorizes one bridge
// call. Every identifier is a canonical lowercase HiveCrew UUID string.
type Chain struct {
	// WorkspaceID scopes all HiveCrew rows. Required.
	WorkspaceID string
	// ProjectID is the HiveCrew project that owns the orchestration Run.
	ProjectID string
	// IssueID is the HiveCrew issue the task belongs to. Required for task
	// level operations; carried as provenance, not as an Orca object.
	IssueID string
	// TaskID is the HiveCrew task row (the Run row in companyops terms).
	TaskID string
	// AssignmentID is the HiveCrew assignment command id that authorizes one
	// run attempt. Required for dispatch level operations.
	AssignmentID string
}

// ValidateApply checks the identifiers required to map a project Run.
func (c Chain) ValidateProjectScope() error {
	if !isUUID(c.WorkspaceID) {
		return fmt.Errorf("%w: workspace id %q is not a canonical uuid", ErrInvalidChain, c.WorkspaceID)
	}
	if !isUUID(c.ProjectID) {
		return fmt.Errorf("%w: project id %q is not a canonical uuid", ErrInvalidChain, c.ProjectID)
	}
	return nil
}

// ValidateTaskScope checks the identifiers required to map an Orca Task.
func (c Chain) ValidateTaskScope() error {
	if err := c.ValidateProjectScope(); err != nil {
		return err
	}
	if !isUUID(c.IssueID) {
		return fmt.Errorf("%w: issue id %q is not a canonical uuid", ErrInvalidChain, c.IssueID)
	}
	if !isUUID(c.TaskID) {
		return fmt.Errorf("%w: task id %q is not a canonical uuid", ErrInvalidChain, c.TaskID)
	}
	return nil
}

// ValidateDispatchScope checks the identifiers the existing dispatch entry
// needs: workspace, project, and assignment command. The issue and task ids
// are derived from the committed dispatch receipt.
func (c Chain) ValidateDispatchScope() error {
	if err := c.ValidateProjectScope(); err != nil {
		return err
	}
	if !isUUID(c.AssignmentID) {
		return fmt.Errorf("%w: assignment command id %q is not a canonical uuid", ErrInvalidChain, c.AssignmentID)
	}
	return nil
}

// ValidateAssignmentScope checks the identifiers required to map one Orca
// Dispatch + supervised Worker for a single run attempt.
func (c Chain) ValidateAssignmentScope() error {
	if err := c.ValidateTaskScope(); err != nil {
		return err
	}
	if !isUUID(c.AssignmentID) {
		return fmt.Errorf("%w: assignment command id %q is not a canonical uuid", ErrInvalidChain, c.AssignmentID)
	}
	return nil
}

// RunMarker is the exact substring embedded in the Orca Run objective that
// reconciles one Orca Run to one HiveCrew project.
func (c Chain) RunMarker() string {
	return MarkerPrefix + " ws=" + c.WorkspaceID + " prj=" + c.ProjectID + "]"
}

// TaskMarker is the exact substring embedded in the Orca Task spec that
// reconciles one Orca Task to one HiveCrew issue task.
func (c Chain) TaskMarker() string {
	return MarkerPrefix + " ws=" + c.WorkspaceID + " prj=" + c.ProjectID +
		" issue=" + c.IssueID + " task=" + c.TaskID + "]"
}

// ProjectRunObjective builds the deterministic Orca Run objective for one
// HiveCrew project. The marker must stay the last element so list scans can
// match it exactly.
func ProjectRunObjective(displayObjective string, c Chain) string {
	objective := strings.TrimSpace(displayObjective)
	if objective == "" {
		objective = "HiveCrew project orchestration"
	}
	return objective + " " + c.RunMarker()
}

// TaskSpec builds the deterministic Orca Task spec for one HiveCrew issue
// task: the provenance marker header followed by the worker instructions.
func TaskSpec(instructions string, c Chain) string {
	body := strings.TrimSpace(instructions)
	if body == "" {
		body = "Execute the HiveCrew task per its issue assignment."
	}
	return c.TaskMarker() + "\n\n" + body
}

// RunMarkerScan extracts the HiveCrew (workspace, project) pair from an Orca
// Run objective, if the objective carries a bridge marker.
func RunMarkerScan(objective string) (workspaceID, projectID string, ok bool) {
	_, rest, found := strings.Cut(objective, MarkerPrefix)
	if !found {
		return "", "", false
	}
	fields := strings.Split(strings.TrimSpace(rest), "]")
	if len(fields) == 0 {
		return "", "", false
	}
	return markerPair(fields[0], "ws", "prj")
}

// TaskMarkerScan extracts the HiveCrew (workspace, project, issue, task)
// tuple from an Orca Task spec, if the spec carries a bridge marker.
func TaskMarkerScan(spec string) (workspaceID, projectID, issueID, taskID string, ok bool) {
	firstLine := spec
	if idx := strings.IndexByte(spec, '\n'); idx >= 0 {
		firstLine = spec[:idx]
	}
	_, rest, found := strings.Cut(firstLine, MarkerPrefix)
	if !found {
		return "", "", "", "", false
	}
	fields := strings.Split(strings.TrimSpace(rest), "]")
	if len(fields) == 0 {
		return "", "", "", "", false
	}
	workspaceID, projectID, ok = markerPair(fields[0], "ws", "prj")
	if !ok {
		return "", "", "", "", false
	}
	issueID, taskID, ok = markerPair(fields[0], "issue", "task")
	if !ok {
		return "", "", "", "", false
	}
	return workspaceID, projectID, issueID, taskID, true
}

func scanMarkerFields(markerBody string, names ...string) (map[string]string, bool) {
	out := make(map[string]string, len(names))
	for _, token := range strings.Fields(markerBody) {
		key, value, found := strings.Cut(token, "=")
		if !found {
			continue
		}
		out[key] = value
	}
	for _, name := range names {
		if out[name] == "" {
			return nil, false
		}
	}
	return out, true
}

// scanMarkerFields returning a map is awkward for two-name calls; wrap the
// common arities below.
func markerPair(markerBody, first, second string) (string, string, bool) {
	values, ok := scanMarkerFields(markerBody, first, second)
	if !ok {
		return "", "", false
	}
	return values[first], values[second], true
}

// CanonicalDigest hashes the canonical JSON encoding of v (sorted keys, no
// insignificant whitespace) with the sha256: prefix used across HiveCrew
// evidence ledgers.
func CanonicalDigest(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("orcabridge: canonicalize digest payload: %w", err)
	}
	var canonical any
	if err := json.Unmarshal(payload, &canonical); err != nil {
		return "", fmt.Errorf("orcabridge: normalize digest payload: %w", err)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("orcabridge: encode canonical digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ObjectiveInput is the immutable payload frozen by the project Run mapping.
// A replay with a different digest fails closed with ErrMappingConflict.
type ObjectiveInput struct {
	ContractVersion  string `json:"contract_version"`
	WorkspaceID      string `json:"workspace_id"`
	ProjectID        string `json:"project_id"`
	DisplayObjective string `json:"display_objective"`
}

// Digest returns the frozen-payload digest for the run mapping row.
func (o ObjectiveInput) Digest() (string, error) {
	if o.ContractVersion == "" {
		o.ContractVersion = ContractVersion
	}
	return CanonicalDigest(o)
}

// SpecInput is the immutable payload frozen by the task mapping.
type SpecInput struct {
	ContractVersion string `json:"contract_version"`
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	IssueID         string `json:"issue_id"`
	TaskID          string `json:"task_id"`
	Instructions    string `json:"instructions"`
}

// Digest returns the frozen-payload digest for the task mapping row.
func (s SpecInput) Digest() (string, error) {
	if s.ContractVersion == "" {
		s.ContractVersion = ContractVersion
	}
	return CanonicalDigest(s)
}

// PlacementInput is the immutable worker placement decision frozen by the
// dispatch mapping: which carrier agent, model, and isolated worktree one
// HiveCrew run attempt is executed in. The HiveCrew task id is deliberately
// absent: it is derived from the dispatch receipt, and the assignment command
// id alone identifies the run attempt. This is execution evidence only; the
// HiveCrew assignment stays the authority for the binding decision.
type PlacementInput struct {
	ContractVersion string `json:"contract_version"`
	WorkspaceID     string `json:"workspace_id"`
	AssignmentID    string `json:"assignment_id"`
	WorktreeMode    string `json:"worktree_mode"` // new-child | new-top-level | current
	WorktreeName    string `json:"worktree_name"`
	RepoSelector    string `json:"repo_selector"`
	BaseBranch      string `json:"base_branch,omitempty"`
	Agent           string `json:"agent"`
	Model           string `json:"model,omitempty"`
	Effort          string `json:"effort,omitempty"`
	SetupPolicy     string `json:"setup_policy,omitempty"`
}

// Digest returns the frozen-payload digest for the dispatch mapping row.
func (p PlacementInput) Digest() (string, error) {
	if p.ContractVersion == "" {
		p.ContractVersion = ContractVersion
	}
	return CanonicalDigest(p)
}

// Validate checks a placement input for the isolated-worktree contract.
// Placement evidence carries only the identifiers it freezes: workspace and
// assignment command.
func (p PlacementInput) Validate() error {
	for label, value := range map[string]string{
		"workspace id":          p.WorkspaceID,
		"assignment command id": p.AssignmentID,
	} {
		if !isUUID(value) {
			return fmt.Errorf("%w: %s %q is not a canonical uuid", ErrInvalidChain, label, value)
		}
	}
	switch p.WorktreeMode {
	case "new-child", "new-top-level":
		if p.WorktreeName == "" {
			return fmt.Errorf("%w: worktree mode %q requires a deterministic worktree name", ErrInvalidChain, p.WorktreeMode)
		}
		if !orcaWorktreeNamePart.MatchString(p.WorktreeName) {
			return fmt.Errorf("%w: worktree name %q is not a safe single token", ErrInvalidChain, p.WorktreeName)
		}
		if p.RepoSelector == "" {
			return fmt.Errorf("%w: worktree mode %q requires an explicit repo selector", ErrInvalidChain, p.WorktreeMode)
		}
	case "current":
		if p.WorktreeName != "" {
			return fmt.Errorf("%w: worktree mode current must not carry a new worktree name", ErrInvalidChain)
		}
	default:
		return fmt.Errorf("%w: unsupported worktree mode %q", ErrInvalidChain, p.WorktreeMode)
	}
	if p.Agent == "" {
		return fmt.Errorf("%w: placement requires an agent carrier id", ErrInvalidChain)
	}
	for label, value := range map[string]string{
		"agent":         p.Agent,
		"model":         p.Model,
		"effort":        p.Effort,
		"repo selector": p.RepoSelector,
	} {
		if value != "" && strings.HasPrefix(value, "-") {
			return fmt.Errorf("%w: %s %q looks like a CLI flag", ErrInvalidChain, label, value)
		}
	}
	if p.Effort != "" && p.Model == "" {
		return fmt.Errorf("%w: effort requires a model", ErrInvalidChain)
	}
	return nil
}

// ValidateOrcaRunID, ValidateOrcaTaskID, ValidateOrcaDispatchID,
// ValidateOrcaTerminalHandle, ValidateOrcaMessageID and ValidateOrcaWorktreeID
// fail closed on identifiers that do not match the observed Orca handle
// grammars before they are interpolated into CLI argv or persisted.
func ValidateOrcaRunID(id string) error {
	return validateHandle("orca run id", id, orcaRunPattern)
}

func ValidateOrcaTaskID(id string) error {
	return validateHandle("orca task id", id, orcaTaskPattern)
}

func ValidateOrcaDispatchID(id string) error {
	return validateHandle("orca dispatch id", id, orcaDispatchPattern)
}

func ValidateOrcaTerminalHandle(handle string) error {
	return validateHandle("orca terminal handle", handle, orcaTerminalPattern)
}

func ValidateOrcaMessageID(id string) error {
	return validateHandle("orca message id", id, orcaMessagePattern)
}

func ValidateOrcaWorktreeID(id string) error {
	return validateHandle("orca worktree id", id, orcaWorktreePattern)
}

func validateHandle(label, value string, pattern *regexp.Regexp) error {
	if !pattern.MatchString(value) {
		return fmt.Errorf("%w: %s %q does not match the expected handle grammar", ErrInvalidChain, label, value)
	}
	return nil
}

func isUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

// IsValidUUID reports whether value is a canonical lowercase HiveCrew UUID.
func IsValidUUID(value string) bool { return isUUID(value) }

// ValidateHiveCrewTaskID fails closed unless value is a canonical HiveCrew
// task (run row) uuid before it is forwarded to a lifecycle verb.
func ValidateHiveCrewTaskID(taskID string) error {
	if !isUUID(taskID) {
		return fmt.Errorf("%w: hivecrew task id %q is not a canonical uuid", ErrInvalidChain, taskID)
	}
	return nil
}
