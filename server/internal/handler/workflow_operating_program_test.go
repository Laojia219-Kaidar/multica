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

func TestCanonicalWorkflowOperatingProgramUUIDRejectsNonCanonical(t *testing.T) {
	for _, value := range []string{
		"11111111-1111-4111-8111-111111111111", // valid control
		"11111111111141118111111111111111",
		"11111111-1111-4111-8111-11111111111A",
	} {
		_, err := canonicalWorkflowOperatingProgramUUID(value)
		if value[8] == '-' && err != nil {
			t.Fatalf("canonical UUID rejected: %v", err)
		}
		if value[8] != '-' && err == nil {
			t.Fatalf("non-canonical UUID accepted: %q", value)
		}
	}
}
