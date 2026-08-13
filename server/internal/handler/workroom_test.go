package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestWorkroomToResponse_StableIDs pins VC-17: every Workroom field that
// is present must map to a stable string ID, and optional bindings must be
// omitted (empty) rather than fabricating a value.
func TestWorkroomToResponse_StableIDs(t *testing.T) {
	wr := db.Workroom{
		ID:        uuidMustParse("6b9b0f8e-0000-0000-0000-000000000001"),
		Name:      "产品评审",
		CreatedBy: uuidMustParse("6b9b0f8e-0000-0000-0000-000000000002"),
		IssueID:   uuidMustParse("6b9b0f8e-0000-0000-0000-000000000003"),
		WorkOrderID: pgtype.Text{String: "WO-1", Valid: true},
		// ProjectID left invalid on purpose.
	}
	out := workroomToResponse(wr)
	if out.ID == "" || out.Name == "" || out.CreatedBy == "" {
		t.Fatalf("stable identity fields must be non-empty: %+v", out)
	}
	if out.IssueID == "" || out.WorkOrderID == "" {
		t.Fatalf("bound issue/workorder must map: %+v", out)
	}
	if out.ProjectID != "" {
		t.Fatalf("unset project_id must stay empty, got %q", out.ProjectID)
	}
}

func uuidMustParse(s string) pgtype.UUID {
	return parseUUID(s)
}
