package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestEmployeeToResponse_StableIDs pins VC-17: employee identity has a stable
// id and optional fields stay empty when unset.
func TestEmployeeToResponse_StableIDs(t *testing.T) {
	e := db.Employee{
		ID:     uuidMustParse("6b9b0f8e-0000-0000-0000-000000000011"),
		Name:   "测试员工",
		Status: "active",
		AgentID: uuidMustParse("6b9b0f8e-0000-0000-0000-000000000012"),
	}
	out := employeeToResponse(e)
	if out.ID == "" || out.Name == "" || out.Status != "active" {
		t.Fatalf("identity fields wrong: %+v", out)
	}
	if out.Position != "" || out.Department != "" {
		t.Fatalf("unset fields must stay empty: %+v", out)
	}
	if out.AgentID == "" {
		t.Fatalf("bound agent must map: %+v", out)
	}
}

// TestDatasetToResponse_StableIDs pins VC-17 for datasets.
func TestDatasetToResponse_StableIDs(t *testing.T) {
	id := uuidMustParse("6b9b0f8e-0000-0000-0000-000000000021")
	auth := []pgtype.UUID{uuidMustParse("6b9b0f8e-0000-0000-0000-000000000022")}
	out := datasetToResponse(id, "产品文档集", "项目成果", "rag_kb", 1, auth)
	if out.ID == "" || out.Name == "" || out.Domain != "项目成果" || out.Version != 1 || out.ProductType != "rag_kb" {
		t.Fatalf("dataset fields wrong: %+v", out)
	}
	if len(out.AuthorizedAgentIds) != 1 {
		t.Fatalf("authorization must map: %+v", out)
	}
}
