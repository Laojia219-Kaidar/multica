package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// WeChat content production contract — strict JSON decoding
// (HIVECREW-WECHAT-REAL-OPERATIONS-V1 / WO-10R).
//
// The typed Go validators cannot see forged or unknown fields that encoding/json
// would silently drop, so the wire entry point decodes in two passes:
//  1. a raw deep scan that rejects caller-supplied execution/artifact/outcome
//     proof keys (task_id, run_id, execution_receipt, input_digest, ...) at ANY
//     nesting depth;
//  2. a strict typed decode (DisallowUnknownFields) so unknown fields fail
//     closed instead of being silently discarded;
//  3. the pure typed validator.
//
// This mirrors the TS pure validator's recursive forged-proof scan and the
// Zod strict wire schemas. Nothing here creates a Task/Run/Artifact/Outcome.
// ---------------------------------------------------------------------------

// wechatContentForbiddenCallerProofKeys mirrors
// WECHAT_CONTENT_FORBIDDEN_CALLER_PROOF_KEYS in
// packages/core/workflow/content-node-contract.ts. input_digest is forbidden
// because the server computes it from the exact handoff note; browsers never
// supply or choose an authority digest.
var wechatContentForbiddenCallerProofKeys = map[string]struct{}{
	"task_id":             {},
	"run_id":              {},
	"initial_task_id":     {},
	"current_task_id":     {},
	"execution_receipt":   {},
	"execution_state":     {},
	"assignment_id":       {},
	"candidate_id":        {},
	"artifact_id":         {},
	"formal_artifact_ref": {},
	"outcome_id":          {},
	"input_digest":        {},
}

// scanWechatContentForbiddenProofKeys walks arbitrary decoded JSON and flags
// every forbidden caller-proof key at any depth (objects and arrays).
func scanWechatContentForbiddenProofKeys(value any, path string, errs *[]error) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			childPath := path + "." + key
			if _, forbidden := wechatContentForbiddenCallerProofKeys[key]; forbidden {
				*errs = append(*errs, fmt.Errorf(
					"caller-supplied %s at %s is server-issued execution/artifact/outcome proof; caller refs never prove authority",
					key, childPath,
				))
			}
			scanWechatContentForbiddenProofKeys(item, childPath, errs)
		}
	case []any:
		for index, item := range typed {
			scanWechatContentForbiddenProofKeys(item, fmt.Sprintf("%s[%d]", path, index), errs)
		}
	}
}

// decodeWechatContentJSONStrict performs the raw forged-proof scan plus the
// strict typed decode. It never mutates state.
func decodeWechatContentJSONStrict(data []byte, target any) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var proofErrs []error
	scanWechatContentForbiddenProofKeys(raw, "$", &proofErrs)
	if len(proofErrs) > 0 {
		return errors.Join(proofErrs...)
	}
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return fmt.Errorf("strict contract decode failed (unknown or mistyped field): %w", err)
	}
	return nil
}

// DecodeWechatContentProductionRequestJSON is the fail-closed wire entry
// point for a WeChat content production request: raw forged-proof scan,
// strict unknown-field decode, then the pure typed validator.
func DecodeWechatContentProductionRequestJSON(data []byte) (WechatContentProductionRequest, error) {
	var req WechatContentProductionRequest
	if err := decodeWechatContentJSONStrict(data, &req); err != nil {
		return WechatContentProductionRequest{}, err
	}
	if err := ValidateWechatContentProductionRequest(req); err != nil {
		return WechatContentProductionRequest{}, err
	}
	return req, nil
}

// DecodeWechatContentNodePlanJSON is the fail-closed wire entry point for a
// caller-submitted node plan: raw forged-proof scan, strict unknown-field
// decode, then the pure typed plan validator.
func DecodeWechatContentNodePlanJSON(data []byte) ([]WechatContentNodeContract, error) {
	var nodes []WechatContentNodeContract
	if err := decodeWechatContentJSONStrict(data, &nodes); err != nil {
		return nil, err
	}
	if err := ValidateWechatContentNodePlan(nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}
