package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func outcomeTestWorkspaceID() string {
	var wsID [16]byte
	wsID[6] = 0x40
	wsID[8] = 0x80
	wsID[15] = 0x11
	return util.UUIDToString(pgtype.UUID{Bytes: wsID, Valid: true})
}

func withOutcomeTestWorkspace(req *http.Request, workspaceID string) *http.Request {
	ctx := middleware.SetMemberContext(req.Context(), workspaceID, db.Member{})
	return req.WithContext(ctx)
}

func TestCompanyOpsOutcomeListResponseSchema(t *testing.T) {
	resp := companyOpsOutcomeListResponse{
		SchemaVersion: service.CompanyOpsOutcomeCenterSchemaVersion,
		Items:         []companyOpsOutcomeSummaryResponse{},
		Total:         0,
		Limit:         50,
		Offset:        0,
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"schema_version", "items", "total", "limit", "offset"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("response missing required key %q", key)
		}
	}
	if string(decoded["schema_version"]) != `"hivecrew.outcome-center.v1"` {
		t.Fatalf("schema_version = %s", decoded["schema_version"])
	}
}

func TestCompanyOpsOutcomeDetailResponseSchema(t *testing.T) {
	resp := companyOpsOutcomeDetailResponse{
		SchemaVersion: service.CompanyOpsOutcomeCenterSchemaVersion,
		Summary: companyOpsOutcomeSummaryResponse{
			ID: "11111111-1111-4111-8111-111111111111",
		},
		Versions: []companyOpsOutcomeVersionResponse{},
		Events:   []companyOpsOutcomeEventResponse{},
		Runs:     []companyOpsOutcomeRunResponse{},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"schema_version", "summary", "versions", "events", "runs"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("response missing required key %q", key)
		}
	}
}

func TestCompanyOpsOutcomeArtifactResponseOmitEmpty(t *testing.T) {
	cases := []struct {
		name     string
		artifact companyOpsOutcomeArtifactResponse
		wantRef  bool
	}{
		{
			name:     "empty formal_artifact_ref omitted",
			artifact: companyOpsOutcomeArtifactResponse{ID: "c1", Revision: 1, Status: "submitted"},
			wantRef:  false,
		},
		{
			name:     "non-empty formal_artifact_ref present",
			artifact: companyOpsOutcomeArtifactResponse{ID: "c1", Revision: 1, Status: "authority_readback_confirmed", FormalVisible: true, FormalArtifactRef: "hivecosm://formal/FA-1"},
			wantRef:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.artifact)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			_, present := decoded["formal_artifact_ref"]
			if present != tc.wantRef {
				t.Fatalf("formal_artifact_ref present = %v, want %v", present, tc.wantRef)
			}
		})
	}
}

func TestCompanyOpsOutcomeSummaryResponseOmitEmpty(t *testing.T) {
	resp := companyOpsOutcomeSummaryResponse{
		ID:             "11111111-1111-4111-8111-111111111111",
		ExecutionState: "awaiting_claim",
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["active_artifact"]; ok {
		t.Fatal("active_artifact should be omitted when nil")
	}
	if _, ok := decoded["latest_event_at"]; ok {
		t.Fatal("latest_event_at should be omitted when empty")
	}
}

func TestCompanyOpsOutcomeSummaryWireFullMapping(t *testing.T) {
	eventAt := "2026-08-11T12:00:00Z"
	summary := service.CompanyOpsOutcomeSummary{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Issue: service.CompanyOpsOutcomeIssue{
			ID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
			Number:     42,
			Identifier: "HIV-42",
			Title:      "Test outcome",
			Status:     "in_progress",
			ProjectID:  "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		},
		WorkOrder: service.CompanyOpsOutcomeWorkOrder{
			SourceRef: "hive://work-order/WO-1",
			Revision:  "rev-1",
			Digest:    "sha256:abc",
		},
		Employee: service.CompanyOpsOutcomeEntity{
			SourceRef: "hivecosm://employees/EMP-1",
			ID:        "hivecosm://employees/EMP-1",
		},
		IdentityBinding: service.CompanyOpsOutcomeEntity{
			SourceRef: "hivecosm://identity-bindings/BIND-1",
			ID:        "hivecosm://identity-bindings/BIND-1",
		},
		ExecutionTarget: service.CompanyOpsOutcomeExecTarget{
			LocalAgentID:  "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
			AgentRef:      "/api/agents/dddddddd-dddd-4ddd-8ddd-dddddddddddd",
			AgentRevision: "agent-rev-1",
			AgentDigest:   "sha256:def",
		},
		CurrentAgentDisplay: service.CompanyOpsOutcomeAgentDisplay{
			Name:   "Test Agent",
			Model:  "gpt-4",
			Status: "online",
		},
		InitialTaskID:  "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		CurrentTaskID:  "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		ExecutionState: "completed",
		ActiveArtifact: &service.CompanyOpsOutcomeArtifact{
			ID:                "ffffffff-ffff-4fff-8fff-ffffffffffff",
			Revision:          2,
			DurableObjectRef:  "s3://bucket/key",
			Digest:            "sha256:ghi",
			ContentType:       "text/markdown",
			Status:            "authority_readback_confirmed",
			FormalVisible:     true,
			FormalArtifactRef: "hive://formal/FA-1",
		},
		VersionCount:  2,
		LatestEventAt: &eventAt,
	}

	wire := companyOpsOutcomeSummaryWire(summary)
	if wire.ID != summary.ID ||
		wire.Issue.Identifier != "HIV-42" ||
		wire.ActiveArtifact.Status != "authority_readback_confirmed" ||
		!wire.ActiveArtifact.FormalVisible ||
		wire.LatestEventAt != eventAt {
		t.Fatalf("wire mapping incomplete: %+v", wire)
	}
}

func TestCompanyOpsOutcomeWireSuppressesPrematureFormalReferences(t *testing.T) {
	summary := service.CompanyOpsOutcomeSummary{
		ActiveArtifact: &service.CompanyOpsOutcomeArtifact{
			ID:                "ffffffff-ffff-4fff-8fff-ffffffffffff",
			Revision:          2,
			DurableObjectRef:  "/uploads/workspaces/ws/artifact-candidates/candidate/digest",
			Digest:            "sha256:digest",
			Status:            "promotion_succeeded",
			FormalVisible:     true,
			FormalArtifactRef: "hive://formal/FA-PREMATURE",
		},
	}
	wire := companyOpsOutcomeSummaryWire(summary)
	if wire.ActiveArtifact == nil {
		t.Fatal("active artifact missing")
	}
	if wire.ActiveArtifact.FormalVisible || wire.ActiveArtifact.FormalArtifactRef != "" {
		t.Fatalf("promotion_succeeded wire exposed formal fields: %+v", wire.ActiveArtifact)
	}

	event := companyOpsOutcomeEventWire(service.CompanyOpsOutcomeEvent{
		Type:              "promotion_succeeded",
		FormalArtifactRef: "hive://formal/FA-PREMATURE",
	})
	if event.FormalArtifactRef != "" {
		t.Fatalf("promotion_succeeded event wire exposed formal_artifact_ref %q", event.FormalArtifactRef)
	}
}

func TestCompanyOpsOutcomeEventWireKeepsConfirmedFormalReference(t *testing.T) {
	event := companyOpsOutcomeEventWire(service.CompanyOpsOutcomeEvent{
		Type:              "authority_readback_confirmed",
		FormalArtifactRef: "hive://formal/FA-1",
	})
	if event.FormalArtifactRef != "hive://formal/FA-1" {
		t.Fatalf("confirmed formal_artifact_ref = %q", event.FormalArtifactRef)
	}
}

func TestGetCompanyOpsOutcome_InvalidUUID(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes/not-a-uuid", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	req = withURLParams(req, "commandId", "not-a-uuid")
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcome(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body companyOpsErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.ReasonCode != "invalid_request" {
		t.Fatalf("reason_code = %q, want invalid_request", body.ReasonCode)
	}
}

func TestGetCompanyOpsOutcomes_RejectsUnknownQueryParams(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes?badparam=true", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestGetCompanyOpsOutcomes_NilServiceReturns503(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestGetCompanyOpsOutcome_NilServiceReturns503(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	validUUID := "11111111-1111-4111-8111-111111111111"
	h := &Handler{}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes/"+validUUID, nil)
	req = withOutcomeTestWorkspace(req, wsID)
	req = withURLParams(req, "commandId", validUUID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcome(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestGetCompanyOpsOutcomes_InvalidFormalVisible(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes?formal_visible=maybe", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetCompanyOpsOutcomes_InvalidAgentID(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes?agent_id=not-a-uuid", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetCompanyOpsOutcomes_InvalidLimit(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes?limit=abc", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetCompanyOpsOutcomes_NoWorkspaceReturns400(t *testing.T) {
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes", nil)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetCompanyOpsOutcomes_RejectsUnknownStatus(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes?status=nonsense", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var body companyOpsErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.ReasonCode != "invalid_request" {
		t.Fatalf("reason_code = %q, want invalid_request", body.ReasonCode)
	}
}

func TestGetCompanyOpsOutcomes_RejectsNonCanonicalEmployeeID(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	cases := map[string]string{
		"extra_slash":   "EMP/extra",
		"query_string":  "EMP?query=1",
		"fragment":      "EMP#fragment",
		"leading_space": " EMP",
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/company-ops/outcomes", nil)
			q := req.URL.Query()
			q.Set("employee_id", v)
			req.URL.RawQuery = q.Encode()
			req = withOutcomeTestWorkspace(req, wsID)
			rec := httptest.NewRecorder()
			h.GetCompanyOpsOutcomes(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("employee_id=%q status = %d, want %d", v, rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestGetCompanyOpsOutcomes_AcceptsCanonicalEmployeeID(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	// The service will be nil-queried — the handler must accept the param
	// and pass it through to the service, which returns 503 (nil queries).
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes?employee_id=EMP-001", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("canonical employee_id should not 400: %s", rec.Body.String())
	}
}

func TestGetCompanyOpsOutcomes_AcceptsTypeParam(t *testing.T) {
	wsID := outcomeTestWorkspaceID()
	h := &Handler{
		CompanyOpsOutcomeCenter: service.NewCompanyOpsOutcomeCenterService(nil),
	}
	req := httptest.NewRequest("GET", "/api/company-ops/outcomes?type=text/markdown", nil)
	req = withOutcomeTestWorkspace(req, wsID)
	rec := httptest.NewRecorder()
	h.GetCompanyOpsOutcomes(rec, req)
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("type param should not 400: %s", rec.Body.String())
	}
}
