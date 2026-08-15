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
func DedupeKey(workspaceID, goalRef, repo, revision, branchOrWorktree string) string {
	goal := strings.TrimSpace(goalRef)
	if goal != "" {
		return "goal:" + workspaceID + ":" + goal
	}
	return "repo:" + workspaceID + ":" + strings.TrimSpace(repo) + ":" +
		strings.TrimSpace(revision) + ":" + strings.TrimSpace(branchOrWorktree)
}
