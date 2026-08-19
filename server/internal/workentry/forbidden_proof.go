package workentry

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ForbiddenProofFields are the 12 server-issued execution/artifact/outcome
// lineage keys a caller must never supply (API-AND-ADAPTER-CONTRACT §8.1).
// Caller-supplied proof is never authority: any occurrence — at any nesting
// depth, including WorkEventV1.event_payload and WorkHandoffV1 — is rejected
// fail-closed with reason_code=forbidden_proof_field.
var ForbiddenProofFields = []string{
	"task_id",
	"run_id",
	"initial_task_id",
	"current_task_id",
	"execution_receipt",
	"execution_state",
	"assignment_id",
	"candidate_id",
	"artifact_id",
	"formal_artifact_ref",
	"outcome_id",
	"input_digest",
}

// ErrForbiddenProofField is returned when a caller supplies a server-issued
// proof field. The offending key is embedded in the message.
var ErrForbiddenProofField = errors.New("forbidden proof field")

func forbiddenProofSet() map[string]bool {
	m := make(map[string]bool, len(ForbiddenProofFields))
	for _, k := range ForbiddenProofFields {
		m[k] = true
	}
	return m
}

// RejectForbiddenProofFields recursively scans a JSON request body and returns
// ErrForbiddenProofField wrapped with the offending key on the first hit.
// Unknown/non-proof fields are ignored (they are validated downstream).
func RejectForbiddenProofFields(raw []byte) error {
	return rejectForbiddenProofFields(raw, nil)
}

// RejectForbiddenProofFieldsForEvent applies the same recursive proof-field
// gate while allowing WorkEventV1's typed, top-level run_id. A run_id anywhere
// below the request root remains caller-supplied proof and is rejected.
func RejectForbiddenProofFieldsForEvent(raw []byte) error {
	return rejectForbiddenProofFields(raw, map[string]bool{"run_id": true})
}

func rejectForbiddenProofFields(raw []byte, allowedRoot map[string]bool) error {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		// Not valid JSON — let the normal decoder produce its own error.
		return nil
	}
	set := forbiddenProofSet()
	if key, ok := findForbidden(root, set, allowedRoot, ""); ok {
		return fmt.Errorf("%w: %q", ErrForbiddenProofField, key)
	}
	return nil
}

func findForbidden(v any, set, allowedRoot map[string]bool, path string) (string, bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if set[k] && !(path == "" && allowedRoot[k]) {
				return joinPath(path, k), true
			}
			if key, ok := findForbidden(val, set, allowedRoot, joinPath(path, k)); ok {
				return key, true
			}
		}
	case []any:
		for i, e := range t {
			if key, ok := findForbidden(e, set, allowedRoot, fmt.Sprintf("%s[%d]", path, i)); ok {
				return key, true
			}
		}
	}
	return "", false
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	if strings.HasPrefix(key, "[") {
		return path + key
	}
	return path + "." + key
}
