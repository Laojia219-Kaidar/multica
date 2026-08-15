package workentry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// DigestPrefix is the canonical sha256 prefix frozen by the contract.
const DigestPrefix = "sha256:"

// CanonicalJSON returns the deterministic JSON encoding used to compute
// payload digests. encoding/json emits struct fields in declaration order and
// map keys in sorted order, which is the canonical form frozen for this slice.
func CanonicalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonical JSON encode: %w", err)
	}
	return b, nil
}

// DigestSHA256 returns "sha256:<64-hex>" for the given bytes.
func DigestSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// Digest returns the canonical "sha256:<64-hex>" digest of v.
func Digest(v any) (string, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	return DigestSHA256(b), nil
}

// digestPayload is the frozen canonical shape covered by a register digest:
// actor identity + intent (selectors are part of WorkIntentV1).
type digestPayload struct {
	ActorIdentity WorkActorIdentityV1 `json:"actor_identity"`
	Intent        WorkIntentV1        `json:"intent"`
}

// ReceiptDigest computes the dedupe digest for a register request.
func ReceiptDigest(actor WorkActorIdentityV1, intent WorkIntentV1) (string, error) {
	return Digest(digestPayload{ActorIdentity: actor, Intent: intent})
}

// FormatWorkRef renders the frozen work_ref shape
// (WORK-ACTOR-CONTRACT §6). workspaceID is the tenant scope; projectID may be
// empty for inbox work.
func FormatWorkRef(workspaceID, projectID, issueID, taskID string) string {
	proj := projectID
	if strings.TrimSpace(proj) == "" {
		proj = "inbox"
	}
	ref := fmt.Sprintf("hivecrew://%s/work/%s", workspaceID, proj)
	if strings.TrimSpace(issueID) != "" {
		ref += "/" + issueID
	}
	if strings.TrimSpace(taskID) != "" {
		ref += "/" + taskID
	}
	return ref
}

// DedupeKey builds the frozen dedupe key. Priority follows API-AND-ADAPTER-
// CONTRACT §3: a Goal/WorkOrder ref wins; otherwise repo+revision+branch/worktree
// is the selector. workspace_id is always the tenant scope.
// ParseWorkRef splits hivecrew://<ws>/work/<project>/<issue>[/<task>] into its
// parts. Unknown/foreign formats yield empty fields.
func ParseWorkRef(workRef string) (workspaceID, projectID, issueID, taskID string) {
	rest := strings.TrimPrefix(workRef, "hivecrew://")
	if rest == workRef {
		return "", "", "", ""
	}
	parts := strings.Split(rest, "/")
	// rest = "<ws>/work/<project>/<issue>[/<task>]"
	if len(parts) < 4 || parts[1] != "work" {
		return "", "", "", ""
	}
	workspaceID = parts[0]
	projectID = parts[2]
	issueID = parts[3]
	if len(parts) >= 5 && parts[4] != "" {
		taskID = parts[4]
	}
	return workspaceID, projectID, issueID, taskID
}

func DedupeKey(workspaceID, actorID, goalRef, repo, revision, branchOrWorktree string) string {
	// The key is actor-scoped so two different actors working the same Goal can
	// each register (and resolve/continue onto the same project) without
	// colliding on the receipt idempotency anchor (VC-08 multi-actor, VC-03
	// same-actor exact replay).
	goal := strings.TrimSpace(goalRef)
	if goal != "" {
		return "goal:" + workspaceID + ":" + strings.TrimSpace(actorID) + ":" + goal
	}
	return "repo:" + workspaceID + ":" + strings.TrimSpace(actorID) + ":" +
		strings.TrimSpace(repo) + ":" + strings.TrimSpace(revision) + ":" + strings.TrimSpace(branchOrWorktree)
}
