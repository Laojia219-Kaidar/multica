package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const repairCandidateMarkerV1 = "HIVECREW_REPAIR_CANDIDATE_V1"

var (
	ErrRepairCandidateMarkerMissing   = errors.New("repair candidate marker is missing")
	ErrRepairCandidateMarkerMalformed = errors.New("repair candidate marker is malformed")
	ErrRepairCandidateIdentityDrift   = errors.New("repair candidate identity drift")
)

type repairCandidateRuntimeResult struct {
	Output string `json:"output"`
}

// repairCandidatePayload is deliberately independent from DispatchIdentity:
// the base identity is evidence the server must compare to the old work Task,
// while the new identity is the candidate declaration emitted by the repair.
type repairCandidatePayload struct {
	RepairTaskID          string `json:"repair_task_id"`
	BaseTaskID            string `json:"base_task_id"`
	BaseCandidateRevision string `json:"base_candidate_revision"`
	BaseGeneration        string `json:"base_generation"`
	CandidateRevision     string `json:"candidate_revision"`
	Generation            string `json:"generation"`
}

func repairCandidateHandoffNote(baseTaskID string) string {
	return fmt.Sprintf(
		"Repair only the exact base candidate Task %s. The current repair Task ID is supplied by the runtime task context. Do not reuse the old implementation comment or identity. Keep the Issue status unchanged: it is already in_review with revise_requested, so do not move it to in_progress, in_review, done, or any other status. Persist exactly one Agent Comment whose source_task_id is the current repair Task ID and whose final non-empty line is the marker below. The Task result is captured from your final assistant response, not from that Comment: your final assistant response MUST repeat the identical marker as its own final non-empty line, with no summary or text after it. The marker is exactly %s followed by one JSON object with repair_task_id, base_task_id, base_candidate_revision, base_generation, candidate_revision, and generation. All six JSON field values MUST be double-quoted JSON strings. In particular, write generation fields like \"base_generation\":\"1\" and \"generation\":\"2\"; numeric literals such as \"base_generation\":1 or \"generation\":2 are malformed. candidate_revision must change and generation must be the base numeric generation plus one. Missing, additional, malformed, duplicate, comment-only, result-only, mismatched, or non-final markers fail closed.",
		baseTaskID, repairCandidateMarkerV1,
	)
}

func parseRepairCandidateResult(result []byte) (repairCandidatePayload, error) {
	var runtimeResult repairCandidateRuntimeResult
	if len(result) == 0 || json.Unmarshal(result, &runtimeResult) != nil {
		return repairCandidatePayload{}, ErrRepairCandidateMarkerMalformed
	}
	return parseRepairCandidateOutput(runtimeResult.Output)
}

func parseRepairCandidateOutput(rawOutput string) (repairCandidatePayload, error) {
	output := strings.TrimSpace(rawOutput)
	if output == "" {
		return repairCandidatePayload{}, ErrRepairCandidateMarkerMissing
	}
	lines := strings.Split(output, "\n")
	prefix := repairCandidateMarkerV1 + " "
	markerCount := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), repairCandidateMarkerV1) {
			markerCount++
		}
	}
	if markerCount != 1 {
		if markerCount == 0 {
			return repairCandidatePayload{}, ErrRepairCandidateMarkerMissing
		}
		return repairCandidatePayload{}, ErrRepairCandidateMarkerMalformed
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(last, prefix) {
		return repairCandidatePayload{}, ErrRepairCandidateMarkerMissing
	}
	payloadJSON := strings.TrimSpace(strings.TrimPrefix(last, prefix))
	if payloadJSON == "" {
		return repairCandidatePayload{}, ErrRepairCandidateMarkerMalformed
	}
	decoder := json.NewDecoder(strings.NewReader(payloadJSON))
	decoder.DisallowUnknownFields()
	var payload repairCandidatePayload
	if err := decoder.Decode(&payload); err != nil {
		return repairCandidatePayload{}, ErrRepairCandidateMarkerMalformed
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return repairCandidatePayload{}, ErrRepairCandidateMarkerMalformed
	}
	if !canonicalUUID(payload.RepairTaskID) || !canonicalUUID(payload.BaseTaskID) ||
		!canonicalString(payload.BaseCandidateRevision) || !canonicalString(payload.BaseGeneration) ||
		!canonicalString(payload.CandidateRevision) || !canonicalString(payload.Generation) {
		return repairCandidatePayload{}, ErrRepairCandidateMarkerMalformed
	}
	if payload.CandidateRevision == payload.BaseCandidateRevision {
		return repairCandidatePayload{}, ErrRepairCandidateIdentityDrift
	}
	base, err := canonicalUint(payload.BaseGeneration)
	if err != nil {
		return repairCandidatePayload{}, ErrRepairCandidateIdentityDrift
	}
	if base == ^uint64(0) {
		return repairCandidatePayload{}, ErrRepairCandidateIdentityDrift
	}
	next, err := canonicalUint(payload.Generation)
	if err != nil || next != base+1 {
		return repairCandidatePayload{}, ErrRepairCandidateIdentityDrift
	}
	return payload, nil
}

func canonicalString(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func canonicalUUID(value string) bool {
	if !canonicalString(value) {
		return false
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func canonicalUint(value string) (uint64, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return 0, errors.New("generation is not canonical")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("generation is not canonical")
	}
	return parsed, nil
}

func repairCandidateMarkerLine(payload repairCandidatePayload) string {
	raw, _ := json.Marshal(payload)
	return repairCandidateMarkerV1 + " " + string(raw)
}
