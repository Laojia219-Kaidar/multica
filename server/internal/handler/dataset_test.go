package handler

import (
	"strings"
	"testing"
)

// TestBuildDatasetUpdateParams_PartialPreservation pins the request contract of
// PATCH /api/datasets/{id}: absent fields stay nil (server keeps the stored
// value), provided fields are validated, and a present authorized_agent_ids —
// including an empty slice — replaces the set.
func TestBuildDatasetUpdateParams_PartialPreservation(t *testing.T) {
	id := uuidMustParse("6b9b0f8e-0000-0000-0000-000000000031")
	ws := uuidMustParse("6b9b0f8e-0000-0000-0000-000000000032")

	// All fields absent: nothing is marked valid, nothing is replaced.
	params, err := buildDatasetUpdateParams(id, ws, updateDatasetRequest{})
	if err != nil {
		t.Fatalf("empty request must be valid: %v", err)
	}
	if params.Name.Valid || params.Domain.Valid || params.ProductType.Valid || params.Version.Valid {
		t.Fatalf("absent fields must stay invalid: %+v", params)
	}
	if params.AuthorizedAgentIds != nil {
		t.Fatalf("absent authorization must stay nil, got %+v", params.AuthorizedAgentIds)
	}

	// Explicit empty authorization clears the set instead of preserving it.
	name := "产品手册集"
	ver := int32(3)
	params, err = buildDatasetUpdateParams(id, ws, updateDatasetRequest{
		Name:               &name,
		Version:            &ver,
		AuthorizedAgentIds: []string{},
	})
	if err != nil {
		t.Fatalf("valid request failed: %v", err)
	}
	if !params.Name.Valid || params.Name.String != name {
		t.Fatalf("name must be applied: %+v", params.Name)
	}
	if !params.Version.Valid || params.Version.Int32 != 3 {
		t.Fatalf("version must be applied: %+v", params.Version)
	}
	if params.AuthorizedAgentIds == nil || len(params.AuthorizedAgentIds) != 0 {
		t.Fatalf("empty slice must clear authorization: %+v", params.AuthorizedAgentIds)
	}

	// A provided authorization set parses each agent id.
	params, err = buildDatasetUpdateParams(id, ws, updateDatasetRequest{
		AuthorizedAgentIds: []string{"6b9b0f8e-0000-0000-0000-0000000000aa"},
	})
	if err != nil {
		t.Fatalf("authorization request failed: %v", err)
	}
	if len(params.AuthorizedAgentIds) != 1 || params.AuthorizedAgentIds[0] != uuidMustParse("6b9b0f8e-0000-0000-0000-0000000000aa") {
		t.Fatalf("authorization ids must map: %+v", params.AuthorizedAgentIds)
	}
}

// TestBuildDatasetUpdateParams_RejectsInvalid keeps malformed input from
// reaching the write query.
func TestBuildDatasetUpdateParams_RejectsInvalid(t *testing.T) {
	id := uuidMustParse("6b9b0f8e-0000-0000-0000-000000000031")
	ws := uuidMustParse("6b9b0f8e-0000-0000-0000-000000000032")
	empty := ""
	zero := int32(0)

	if _, err := buildDatasetUpdateParams(id, ws, updateDatasetRequest{Name: &empty}); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("empty name must be rejected: %v", err)
	}
	if _, err := buildDatasetUpdateParams(id, ws, updateDatasetRequest{Domain: &empty}); err == nil || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("empty domain must be rejected: %v", err)
	}
	if _, err := buildDatasetUpdateParams(id, ws, updateDatasetRequest{ProductType: &empty}); err == nil || !strings.Contains(err.Error(), "product_type") {
		t.Fatalf("empty product_type must be rejected: %v", err)
	}
	if _, err := buildDatasetUpdateParams(id, ws, updateDatasetRequest{Version: &zero}); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("non-positive version must be rejected: %v", err)
	}
	if _, err := buildDatasetUpdateParams(id, ws, updateDatasetRequest{AuthorizedAgentIds: []string{"not-a-uuid"}}); err == nil || !strings.Contains(err.Error(), "authorized_agent_ids") {
		t.Fatalf("malformed agent id must be rejected: %v", err)
	}
}
