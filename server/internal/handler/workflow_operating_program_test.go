package handler

import (
	"encoding/json"
	"testing"
)

func TestWorkflowOperatingProgramResponseKeepsProjectIDsExplicit(t *testing.T) {
	response := workflowOperatingProgramResponse{
		ID: "11111111-1111-4111-8111-111111111111", WorkspaceID: "22222222-2222-4222-8222-222222222222",
		Name: "公众号运营", ProjectIDs: []string{},
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded["project_ids"]) != `[]` {
		t.Fatalf("project_ids = %s, want []", decoded["project_ids"])
	}
}

// TestCanonicalWorkflowOperatingProgramUUIDRejectsNonCanonical pins the real
// product contract of canonicalWorkflowOperatingProgramUUID: the input must
// parse as a UUID and be byte-identical to its canonical lowercase string
// form. The cases below are evidence categories only — empty, malformed,
// dashless, uppercase — and deliberately assert no RFC version/variant
// restriction, because the product source enforces none.
func TestCanonicalWorkflowOperatingProgramUUIDRejectsNonCanonical(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"canonical lowercase control", "11111111-1111-4111-8111-111111111111", false},
		{"empty", "", true},
		{"malformed trailing character", "11111111-1111-4111-8111-11111111111A", true},
		{"malformed truncated", "11111111-1111-4111-8111", true},
		{"dashless hex", "11111111111141118111111111111111", true},
		{"uppercase", "11111111-1111-4111-8111-11111111111F", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalWorkflowOperatingProgramUUID(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("non-canonical UUID accepted: %q", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonical UUID rejected: %v", err)
			}
			if got != tc.value {
				t.Fatalf("canonical UUID rewritten: got %q, want %q", got, tc.value)
			}
		})
	}
}
