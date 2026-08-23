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

// RejectForbiddenProofFieldsForSync allows run_id only on the typed event
// contained by an event SyncEntry. It accepts both the CLI's raw entry array
// and the HTTP sync envelope, while register/unknown entries remain closed.
func RejectForbiddenProofFieldsForSync(raw []byte) error {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	allowed := make(map[string]bool)
	switch value := root.(type) {
	case []any:
		allowSyncEventRunIDs(value, "", allowed)
	case map[string]any:
		if entries, ok := value["entries"].([]any); ok {
			allowSyncEventRunIDs(entries, "entries", allowed)
		}
	}
	return rejectForbiddenProofValue(root, allowed)
}

// RejectForbiddenProofFieldsForMCPCall allows the same typed run_id through
// the tools/call envelope for work.event and work.sync only.
func RejectForbiddenProofFieldsForMCPCall(raw []byte) error {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	allowed := make(map[string]bool)
	call, _ := root.(map[string]any)
	name, _ := call["name"].(string)
	arguments, _ := call["arguments"].(map[string]any)
	switch name {
	case "work.event":
		if arguments != nil {
			allowed["arguments.run_id"] = true
		}
	case "work.sync":
		if entries, ok := arguments["entries"].([]any); ok {
			allowSyncEventRunIDs(entries, "arguments.entries", allowed)
		}
	}
	return rejectForbiddenProofValue(root, allowed)
}

func allowSyncEventRunIDs(entries []any, prefix string, allowed map[string]bool) {
	for index, rawEntry := range entries {
		entry, _ := rawEntry.(map[string]any)
		verb, _ := entry["verb"].(string)
		if verb != "event" {
			continue
		}
		base := fmt.Sprintf("[%d]", index)
		if prefix != "" {
			base = fmt.Sprintf("%s[%d]", prefix, index)
		}
		allowed[base+".canonical_payload.event.run_id"] = true
	}
}

func rejectForbiddenProofFields(raw []byte, allowed map[string]bool) error {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		// Not valid JSON — let the normal decoder produce its own error.
		return nil
	}
	return rejectForbiddenProofValue(root, allowed)
}

func rejectForbiddenProofValue(root any, allowed map[string]bool) error {
	set := forbiddenProofSet()
	if key, ok := findForbidden(root, set, allowed, ""); ok {
		return fmt.Errorf("%w: %q", ErrForbiddenProofField, key)
	}
	return nil
}

func findForbidden(v any, set, allowedRoot map[string]bool, path string) (string, bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			keyPath := joinPath(path, k)
			if set[k] && !allowedRoot[keyPath] {
				return keyPath, true
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
