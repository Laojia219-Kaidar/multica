package service

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestCompanyOpsOutcomeCursorRoundTrip(t *testing.T) {
	cursor := CompanyOpsOutcomeCursor{
		Version:   companyOpsOutcomeCursorVersion,
		CreatedAt: "2026-08-13T12:00:00.123456789Z",
		CommandID: "11111111-1111-4111-8111-111111111111",
	}
	token := encodeCompanyOpsOutcomeCursor(cursor)
	if token == nil {
		t.Fatal("encodeCompanyOpsOutcomeCursor returned nil")
	}
	if strings.ContainsAny(*token, "+/=") {
		t.Fatalf("cursor must be URL-safe, got %q", *token)
	}
	decoded, err := decodeCompanyOpsOutcomeCursor(*token)
	if err != nil {
		t.Fatalf("decodeCompanyOpsOutcomeCursor: %v", err)
	}
	if decoded != cursor {
		t.Fatalf("round-trip mismatch: got %+v want %+v", decoded, cursor)
	}
}

func TestCompanyOpsOutcomeCursorRejectsInvalidTokens(t *testing.T) {
	valid := CompanyOpsOutcomeCursor{
		Version:   companyOpsOutcomeCursorVersion,
		CreatedAt: "2026-08-13T12:00:00Z",
		CommandID: "11111111-1111-4111-8111-111111111111",
	}
	cases := map[string]string{
		"not base64":      "%%%not-base64%%%",
		"not json":        "bm90LWpzb24",
		"empty":           "",
	}
	for name, token := range cases {
		if _, err := decodeCompanyOpsOutcomeCursor(token); err == nil {
			t.Fatalf("%s: expected error, got nil", name)
		}
	}

	// wrong version
	wrongVersion := valid
	wrongVersion.Version = 99
	if tok := encodeCompanyOpsOutcomeCursor(wrongVersion); tok != nil {
		if _, err := decodeCompanyOpsOutcomeCursor(*tok); err == nil {
			t.Fatal("expected unsupported version error")
		}
	}

	// missing keyset fields
	missing := valid
	missing.CommandID = ""
	if tok := encodeCompanyOpsOutcomeCursor(missing); tok != nil {
		if _, err := decodeCompanyOpsOutcomeCursor(*tok); err == nil {
			t.Fatal("expected missing keyset fields error")
		}
	}
}

func TestCompanyOpsOutcomeCursorRejectsWhitespaceCommandID(t *testing.T) {
	cursor := CompanyOpsOutcomeCursor{
		Version:   companyOpsOutcomeCursorVersion,
		CreatedAt: "2026-08-13T12:00:00Z",
		CommandID: " 11111111-1111-4111-8111-111111111111 ",
	}
	token := encodeCompanyOpsOutcomeCursor(cursor)
	if token == nil {
		t.Fatal("encode returned nil")
	}
	if _, err := decodeCompanyOpsOutcomeCursor(*token); err == nil {
		t.Fatal("expected whitespace command_id rejection")
	}
}

func TestCompanyOpsOutcomeFilterValues(t *testing.T) {
	var ws [16]byte
	ws[0] = 1
	workspaceID := pgtype.UUID{Bytes: ws, Valid: true}

	formalTrue := true
	req := CompanyOpsOutcomeListRequest{
		WorkspaceID:   workspaceID,
		Q:             "  search  ",
		AgentID:       workspaceID,
		ProjectID:     workspaceID,
		EmployeeID:    "EMP-1",
		Type:          "text/markdown",
		Status:        "approved",
		FormalVisible: &formalTrue,
	}
	qText, agentFilter, projectFilter, employeeFilter, typeFilter, statusFilter, formalVisibleFilter :=
		companyOpsOutcomeFilterValues(req)
	if !qText.Valid || qText.String != "search" {
		t.Fatalf("qText = %+v", qText)
	}
	if !agentFilter.Valid {
		t.Fatal("agentFilter should be valid")
	}
	if !projectFilter.Valid {
		t.Fatal("projectFilter should be valid")
	}
	if !employeeFilter.Valid || employeeFilter.String != "EMP-1" {
		t.Fatalf("employeeFilter = %+v", employeeFilter)
	}
	if !typeFilter.Valid || typeFilter.String != "text/markdown" {
		t.Fatalf("typeFilter = %+v", typeFilter)
	}
	if !statusFilter.Valid || statusFilter.String != "approved" {
		t.Fatalf("statusFilter = %+v", statusFilter)
	}
	if !formalVisibleFilter.Valid || !formalVisibleFilter.Bool {
		t.Fatalf("formalVisibleFilter = %+v", formalVisibleFilter)
	}

	// Empty request maps to all-invalid filters.
	eq, eAgent, eProject, eEmployee, eType, eStatus, eFormal :=
		companyOpsOutcomeFilterValues(CompanyOpsOutcomeListRequest{WorkspaceID: workspaceID})
	if eq.Valid || eAgent.Valid || eProject.Valid || eEmployee.Valid ||
		eType.Valid || eStatus.Valid || eFormal.Valid {
		t.Fatal("empty request should leave all filters invalid")
	}
}

func TestCompanyOpsOutcomePageNilService(t *testing.T) {
	var svc *CompanyOpsOutcomeCenterService
	_, err := svc.ListOutcomesPage(t.Context(), CompanyOpsOutcomeListRequest{})
	if err != ErrCompanyOpsArtifactUnavailable {
		t.Fatalf("nil service error = %v, want ErrCompanyOpsArtifactUnavailable", err)
	}
}
