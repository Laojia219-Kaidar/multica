package handler

import (
	"encoding/json"
	"testing"
)

func TestCompanyOpsArtifactReplicaLocationsResponseKeepsEmptyListExplicit(t *testing.T) {
	response := companyOpsArtifactReplicaLocationsResponse{
		SchemaVersion: companyOpsArtifactReplicaLocationsSchemaVersion,
		WorkspaceID:   "11111111-1111-4111-8111-111111111111",
		OutcomeID:     "a4d7525e-98ba-4aa2-8dc4-bf49c6bf5ed9",
		Items:         []companyOpsArtifactReplicaLocationResponse{},
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded["schema_version"]) != `"hivecrew.artifact-replica-locations.v1"` {
		t.Fatalf("schema_version = %s", decoded["schema_version"])
	}
	if string(decoded["items"]) != `[]` {
		t.Fatalf("items = %s, want explicit empty array", decoded["items"])
	}
}

func TestCompanyOpsArtifactReplicaLocationResponseDoesNotExposeMetadata(t *testing.T) {
	response := companyOpsArtifactReplicaLocationResponse{
		ID:                "b7222a44-d93d-4d57-9f4c-fbf6ca594c74",
		OutcomeID:         "a4d7525e-98ba-4aa2-8dc4-bf49c6bf5ed9",
		CandidateID:       "c4f9fbe4-c0fd-472b-b595-f2ea304b20b6",
		CandidateRevision: 1,
		LocationClass:     "nas-primary",
		LocationID:        "nas-01",
		StorageID:         "synology-main",
		ObjectRef:         "nas://candidate/draft.md",
		State:             "registered",
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) == "" || string(raw) == "null" {
		t.Fatal("response unexpectedly empty")
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := decoded["metadata"]; ok {
		t.Fatal("metadata must remain outside the read-only public projection")
	}
}
