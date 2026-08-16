package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const completedReviewVerdictMarkerV1 = "HIVECREW_REVIEW_VERDICT_V1"

var (
	ErrCompletedReviewVerdictMissing   = errors.New("review cell: completed review verdict marker is missing")
	ErrCompletedReviewVerdictMalformed = errors.New("review cell: completed review verdict marker is malformed")
)

type completedReviewRuntimeResult struct {
	Output string `json:"output"`
}

type completedReviewVerdictPayload struct {
	Verdict            string   `json:"verdict"`
	Notes              string   `json:"notes"`
	RepairRequirements []string `json:"repair_requirements,omitempty"`
}

func completedReviewVerdictHandoffNote(candidateTaskID string) string {
	return fmt.Sprintf(
		"Review only the exact candidate Task %s. Do not implement or repair it during this run. Your final non-empty output line MUST be exactly %s followed by one JSON object with verdict, notes, and repair_requirements. Allowed contracts: %s {\"verdict\":\"revise\",\"notes\":\"evidence-based reason\",\"repair_requirements\":[\"specific required change\"]} or %s {\"verdict\":\"pass\",\"notes\":\"evidence-based reason\",\"repair_requirements\":[]}. Any missing, additional, malformed, or non-final marker fails closed and cannot create a verdict.",
		candidateTaskID,
		completedReviewVerdictMarkerV1,
		completedReviewVerdictMarkerV1,
		completedReviewVerdictMarkerV1,
	)
}

func parseCompletedReviewVerdict(result []byte) (VerdictInput, error) {
	var runtimeResult completedReviewRuntimeResult
	if len(result) == 0 || json.Unmarshal(result, &runtimeResult) != nil {
		return VerdictInput{}, ErrCompletedReviewVerdictMalformed
	}
	output := strings.TrimSpace(runtimeResult.Output)
	if output == "" {
		return VerdictInput{}, ErrCompletedReviewVerdictMissing
	}
	lines := strings.Split(output, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	prefix := completedReviewVerdictMarkerV1 + " "
	if !strings.HasPrefix(last, prefix) {
		return VerdictInput{}, ErrCompletedReviewVerdictMissing
	}
	payloadJSON := strings.TrimSpace(strings.TrimPrefix(last, prefix))
	if payloadJSON == "" {
		return VerdictInput{}, ErrCompletedReviewVerdictMalformed
	}
	decoder := json.NewDecoder(strings.NewReader(payloadJSON))
	decoder.DisallowUnknownFields()
	var payload completedReviewVerdictPayload
	if err := decoder.Decode(&payload); err != nil {
		return VerdictInput{}, ErrCompletedReviewVerdictMalformed
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return VerdictInput{}, ErrCompletedReviewVerdictMalformed
	}
	payload.Verdict = strings.ToLower(strings.TrimSpace(payload.Verdict))
	payload.Notes = strings.TrimSpace(payload.Notes)
	if payload.Notes == "" {
		return VerdictInput{}, ErrCompletedReviewVerdictMalformed
	}
	for i := range payload.RepairRequirements {
		payload.RepairRequirements[i] = strings.TrimSpace(payload.RepairRequirements[i])
		if payload.RepairRequirements[i] == "" {
			return VerdictInput{}, ErrCompletedReviewVerdictMalformed
		}
	}
	switch payload.Verdict {
	case "revise":
		if len(payload.RepairRequirements) == 0 {
			return VerdictInput{}, ErrCompletedReviewVerdictMalformed
		}
	case "pass":
		if len(payload.RepairRequirements) != 0 {
			return VerdictInput{}, ErrCompletedReviewVerdictMalformed
		}
	default:
		return VerdictInput{}, ErrCompletedReviewVerdictMalformed
	}
	return VerdictInput{
		Verdict:            payload.Verdict,
		Notes:              payload.Notes,
		RepairRequirements: append([]string(nil), payload.RepairRequirements...),
	}, nil
}

func mergeCompletedReviewVerdictResult(raw []byte, receipt verdictReceipt) ([]byte, error) {
	var result map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, ErrCompletedReviewVerdictMalformed
	}
	if _, exists := result["verdict"]; exists {
		return nil, fmt.Errorf("%w: verdict already exists", ErrCompletedReviewVerdictMalformed)
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(receiptJSON, &fields); err != nil {
		return nil, err
	}
	for key, value := range fields {
		result[key] = value
	}
	result["verdict_contract"] = completedReviewVerdictMarkerV1
	return json.Marshal(result)
}

func completedReviewResultHasVerdict(result []byte) bool {
	if len(result) == 0 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(result))
	var value map[string]any
	if decoder.Decode(&value) != nil {
		return false
	}
	verdict, ok := value["verdict"].(string)
	contract, contractOK := value["verdict_contract"].(string)
	return ok && contractOK && contract == completedReviewVerdictMarkerV1 && (verdict == "pass" || verdict == "revise")
}
