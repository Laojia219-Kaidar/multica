package service

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestParseRepairCandidateResultStrictContract(t *testing.T) {
	payload := repairCandidatePayload{
		RepairTaskID:          "01972f7e-7e8d-77ef-a13d-1b0ce3e9c012",
		BaseTaskID:            "01972f7e-7e8d-77ef-a13d-1b0ce3e9c013",
		BaseCandidateRevision: "candidate-old",
		BaseGeneration:        "7",
		CandidateRevision:     "candidate-new",
		Generation:            "8",
	}
	marker := repairCandidateMarkerLine(payload)
	raw, err := json.Marshal(map[string]string{"output": "evidence\n" + marker})
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseRepairCandidateResult(raw)
	if err != nil || got != payload {
		t.Fatalf("parse = %#v, err=%v, want %#v", got, err, payload)
	}

	tests := []struct {
		name string
		out  string
		want error
	}{
		{name: "missing", out: "evidence", want: ErrRepairCandidateMarkerMissing},
		{name: "not final", out: marker + "\nmore", want: ErrRepairCandidateMarkerMissing},
		{name: "duplicate", out: marker + "\n" + marker, want: ErrRepairCandidateMarkerMalformed},
		{name: "unknown field", out: repairCandidateMarkerV1 + ` {"repair_task_id":"01972f7e-7e8d-77ef-a13d-1b0ce3e9c012","base_task_id":"01972f7e-7e8d-77ef-a13d-1b0ce3e9c013","base_candidate_revision":"candidate-old","base_generation":"7","candidate_revision":"candidate-new","generation":"8","extra":"no"}`, want: ErrRepairCandidateMarkerMalformed},
		{name: "same revision", out: repairCandidateMarkerLine(repairCandidatePayload{RepairTaskID: payload.RepairTaskID, BaseTaskID: payload.BaseTaskID, BaseCandidateRevision: "same", BaseGeneration: "7", CandidateRevision: "same", Generation: "8"}), want: ErrRepairCandidateIdentityDrift},
		{name: "non numeric generation", out: repairCandidateMarkerLine(repairCandidatePayload{RepairTaskID: payload.RepairTaskID, BaseTaskID: payload.BaseTaskID, BaseCandidateRevision: "old", BaseGeneration: "g-7", CandidateRevision: "new", Generation: "g-8"}), want: ErrRepairCandidateIdentityDrift},
		{name: "not incremented", out: repairCandidateMarkerLine(repairCandidatePayload{RepairTaskID: payload.RepairTaskID, BaseTaskID: payload.BaseTaskID, BaseCandidateRevision: "old", BaseGeneration: "7", CandidateRevision: "new", Generation: "9"}), want: ErrRepairCandidateIdentityDrift},
		{name: "generation overflow", out: repairCandidateMarkerLine(repairCandidatePayload{RepairTaskID: payload.RepairTaskID, BaseTaskID: payload.BaseTaskID, BaseCandidateRevision: "old", BaseGeneration: "18446744073709551615", CandidateRevision: "new", Generation: "0"}), want: ErrRepairCandidateIdentityDrift},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]string{"output": tt.out})
			if err != nil {
				t.Fatal(err)
			}
			_, gotErr := parseRepairCandidateResult(input)
			if !errors.Is(gotErr, tt.want) {
				t.Fatalf("err=%v, want errors.Is(%v)", gotErr, tt.want)
			}
		})
	}
}

func TestRepairCandidateHandoffNoteRequiresExactMarker(t *testing.T) {
	note := repairCandidateHandoffNote("01972f7e-7e8d-77ef-a13d-1b0ce3e9c013")
	if !containsString(note, repairCandidateMarkerV1) || !containsString(note, "generation must be the base numeric generation plus one") {
		t.Fatalf("handoff note = %q", note)
	}
}

func TestRepairCandidateReplayIdentityRequiresFullIssueTuple(t *testing.T) {
	issueID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000301"), Valid: true}
	workspaceID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000101"), Valid: true}
	issue := db.Issue{ID: issueID, WorkspaceID: workspaceID}
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: uuid.MustParse("00000000-0000-0000-0000-000000000101").String(),
		IssueID:     uuid.MustParse("00000000-0000-0000-0000-000000000301").String(),
		Stage:       "implementation", CandidateRevision: "candidate-new", Generation: "2",
	}
	if !repairCandidateDispatchIdentityMatchesIssue(identity, issue, "implementation", "candidate-new", "2") {
		t.Fatal("canonical identity should match issue")
	}
	for name, mutate := range map[string]func(*continuousdispatch.DispatchIdentity){
		"workspace": func(value *continuousdispatch.DispatchIdentity) {
			value.WorkspaceID = "00000000-0000-0000-0000-000000000102"
		},
		"issue": func(value *continuousdispatch.DispatchIdentity) {
			value.IssueID = "00000000-0000-0000-0000-000000000302"
		},
		"stage":    func(value *continuousdispatch.DispatchIdentity) { value.Stage = "review" },
		"revision": func(value *continuousdispatch.DispatchIdentity) { value.CandidateRevision = "candidate-other" },
		"generation": func(value *continuousdispatch.DispatchIdentity) {
			value.Generation = "3"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := identity
			mutate(&candidate)
			if repairCandidateDispatchIdentityMatchesIssue(candidate, issue, "implementation", "candidate-new", "2") {
				t.Fatalf("identity %#v unexpectedly matched after %s drift", candidate, name)
			}
		})
	}
}
