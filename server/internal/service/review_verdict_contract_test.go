package service

import (
	"encoding/json"
	"errors"
	"testing"
)

func completedReviewResult(t *testing.T, output string) []byte {
	t.Helper()
	result, err := json.Marshal(map[string]any{"output": output})
	if err != nil {
		t.Fatalf("marshal completed review result: %v", err)
	}
	return result
}

func TestParseCompletedReviewVerdict(t *testing.T) {
	validRevise := "evidence\n" + completedReviewVerdictMarkerV1 + ` {"verdict":"revise","notes":"needs a fix","repair_requirements":["add the missing test"]}`
	validPass := completedReviewVerdictMarkerV1 + ` {"verdict":"pass","notes":"all evidence passed","repair_requirements":[]}`

	tests := []struct {
		name        string
		result      []byte
		wantVerdict string
		wantErr     error
	}{
		{name: "valid revise", result: completedReviewResult(t, validRevise), wantVerdict: "revise"},
		{name: "valid pass", result: completedReviewResult(t, validPass), wantVerdict: "pass"},
		{name: "missing marker", result: completedReviewResult(t, "REVISE because it is broken"), wantErr: ErrCompletedReviewVerdictMissing},
		{name: "marker is not final", result: completedReviewResult(t, validPass+"\nextra prose"), wantErr: ErrCompletedReviewVerdictMissing},
		{name: "missing output", result: []byte(`{"other":"value"}`), wantErr: ErrCompletedReviewVerdictMissing},
		{name: "invalid result json", result: []byte(`{`), wantErr: ErrCompletedReviewVerdictMalformed},
		{name: "unknown field", result: completedReviewResult(t, completedReviewVerdictMarkerV1+` {"verdict":"pass","notes":"ok","repair_requirements":[],"extra":true}`), wantErr: ErrCompletedReviewVerdictMalformed},
		{name: "revise without repair requirements", result: completedReviewResult(t, completedReviewVerdictMarkerV1+` {"verdict":"revise","notes":"broken","repair_requirements":[]}`), wantErr: ErrCompletedReviewVerdictMalformed},
		{name: "pass with repair requirements", result: completedReviewResult(t, completedReviewVerdictMarkerV1+` {"verdict":"pass","notes":"contradiction","repair_requirements":["fix"]}`), wantErr: ErrCompletedReviewVerdictMalformed},
		{name: "empty notes", result: completedReviewResult(t, completedReviewVerdictMarkerV1+` {"verdict":"pass","notes":" ","repair_requirements":[]}`), wantErr: ErrCompletedReviewVerdictMalformed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCompletedReviewVerdict(tt.result)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCompletedReviewVerdict: %v", err)
			}
			if got.Verdict != tt.wantVerdict {
				t.Fatalf("verdict = %q, want %q", got.Verdict, tt.wantVerdict)
			}
		})
	}
}

func TestMergeCompletedReviewVerdictResultPreservesRuntimeOutput(t *testing.T) {
	rawOutput := "evidence\n" + completedReviewVerdictMarkerV1 + ` {"verdict":"revise","notes":"fix","repair_requirements":["test"]}`
	raw := completedReviewResult(t, rawOutput)
	merged, err := mergeCompletedReviewVerdictResult(raw, verdictReceipt{
		Verdict:            "revise",
		ReviewState:        ReviewStateReviseRequested,
		ReviewerAgentID:    "reviewer",
		CandidateTaskID:    "candidate",
		Notes:              "fix",
		RepairRequirements: []string{"test"},
	})
	if err != nil {
		t.Fatalf("mergeCompletedReviewVerdictResult: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("decode merged result: %v", err)
	}
	if got["output"] != rawOutput {
		t.Fatalf("runtime output was not preserved: %#v", got["output"])
	}
	if got["verdict"] != "revise" || got["verdict_contract"] != completedReviewVerdictMarkerV1 {
		t.Fatalf("structured verdict missing: %#v", got)
	}
}

func TestCompletedReviewResultHasVerdictRequiresSystemContract(t *testing.T) {
	if completedReviewResultHasVerdict([]byte(`{"verdict":"pass"}`)) {
		t.Fatalf("unversioned runtime verdict must not be treated as a canonical receipt")
	}
	if !completedReviewResultHasVerdict([]byte(`{"verdict":"pass","verdict_contract":"HIVECREW_REVIEW_VERDICT_V1"}`)) {
		t.Fatalf("versioned canonical verdict was not recognized")
	}
}
