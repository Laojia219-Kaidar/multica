package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestCompanyOpsFormalArtifactPromotionResponseSerialization(t *testing.T) {
	cases := []struct {
		name       string
		response   companyOpsFormalArtifactPromotionResponse
		wantRef    string
		refPresent bool
	}{
		{
			name: "empty formal_artifact_ref omitted",
			response: companyOpsFormalArtifactPromotionResponse{
				SchemaVersion:   "hivecrew.formal-artifact-promotion.v1",
				PromotionID:     "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				CandidateID:     "66666666-6666-4666-8666-666666666666",
				LifecycleStatus: "promotion_requested",
				FormalVisible:   false,
				WritePerformed:  false,
				EventID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
				Sequence:        3,
			},
			wantRef:    "",
			refPresent: false,
		},
		{
			name: "non-empty formal_artifact_ref present",
			response: companyOpsFormalArtifactPromotionResponse{
				SchemaVersion:     "hivecrew.formal-artifact-promotion.v1",
				PromotionID:       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				CandidateID:       "66666666-6666-4666-8666-666666666666",
				LifecycleStatus:   "authority_readback_confirmed",
				FormalArtifactRef: "hivecosm://formal-artifacts/FA-001",
				FormalVisible:     true,
				WritePerformed:    true,
				EventID:           "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
				Sequence:          5,
			},
			wantRef:    "hivecosm://formal-artifacts/FA-001",
			refPresent: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.response)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal to map: %v", err)
			}
			_, present := decoded["formal_artifact_ref"]
			if present != tc.refPresent {
				t.Fatalf("formal_artifact_ref present = %v, want %v", present, tc.refPresent)
			}
			if tc.refPresent {
				var ref string
				if err := json.Unmarshal(decoded["formal_artifact_ref"], &ref); err != nil {
					t.Fatalf("unmarshal formal_artifact_ref: %v", err)
				}
				if ref != tc.wantRef {
					t.Fatalf("formal_artifact_ref = %q, want %q", ref, tc.wantRef)
				}
			}
			if string(decoded["schema_version"]) != `"hivecrew.formal-artifact-promotion.v1"` {
				t.Fatalf("schema_version = %s", decoded["schema_version"])
			}
			if string(decoded["lifecycle_status"]) != `"`+tc.response.LifecycleStatus+`"` {
				t.Fatalf("lifecycle_status = %s", decoded["lifecycle_status"])
			}
		})
	}
}

func TestCompanyOpsSelectorsFromQueryRequiresExactStableSelectors(t *testing.T) {
	const valid = "/api/company-ops/work-context?work_order_source_ref=hive%3A%2F%2Fhivecosm%2Fdelivery%2Fproject%2FPRJ-HIVECREW-P2%2Fwork-order%2FWO-P2-001&employee_id=EMP-P2-001&identity_binding_id=BIND-P2-001&agent_id=22222222-2222-4222-8222-222222222222&session_id=33333333-3333-4333-8333-333333333333"
	request := httptest.NewRequest("GET", valid, nil)
	selectors, err := companyOpsSelectorsFromQuery(request)
	if err != nil {
		t.Fatalf("companyOpsSelectorsFromQuery: %v", err)
	}
	if selectors.WorkOrderSourceRef != "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001" ||
		selectors.AgentID != "22222222-2222-4222-8222-222222222222" ||
		selectors.SessionID != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("selectors = %+v", selectors)
	}

	for name, target := range map[string]string{
		"missing":     strings.Replace(valid, "&employee_id=EMP-P2-001", "", 1),
		"duplicate":   valid + "&agent_id=44444444-4444-4444-8444-444444444444",
		"unknown key": valid + "&debug=true",
		"whitespace": strings.Replace(
			valid,
			"employee_id=EMP-P2-001",
			"employee_id=%20EMP-P2-001",
			1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := companyOpsSelectorsFromQuery(httptest.NewRequest("GET", target, nil)); err == nil {
				t.Fatal("companyOpsSelectorsFromQuery error = nil")
			}
		})
	}
}

func TestCompanyOpsWorkOrderLinkMatchesRequiresExactRevisionAndDigest(t *testing.T) {
	workOrder := companyops.AuthoritySnapshot{
		Revision:      "sha256:" + strings.Repeat("a", 64),
		ContentDigest: "sha256:" + strings.Repeat("b", 64),
	}
	if !companyOpsWorkOrderLinkMatches(workOrder.Revision, workOrder.ContentDigest, workOrder) {
		t.Fatal("exact WorkOrder link was treated as stale")
	}
	for name, link := range map[string][2]string{
		"revision drift": {"sha256:" + strings.Repeat("c", 64), workOrder.ContentDigest},
		"digest drift":   {workOrder.Revision, "sha256:" + strings.Repeat("d", 64)},
		"both drift":     {"sha256:" + strings.Repeat("c", 64), "sha256:" + strings.Repeat("d", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			if companyOpsWorkOrderLinkMatches(link[0], link[1], workOrder) {
				t.Fatal("drifted WorkOrder link was treated as exact")
			}
		})
	}
}

func TestDecodeCompanyOpsAssignmentRequestStrictJSON(t *testing.T) {
	valid := `{
		"command_id":"11111111-1111-4111-8111-111111111111",
		"work_order_source_ref":"hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001",
		"employee_id":"EMP-P2-001",
		"identity_binding_id":"BIND-P2-001",
		"agent_id":"22222222-2222-4222-8222-222222222222",
		"session_id":"33333333-3333-4333-8333-333333333333",
		"project_id":"44444444-4444-4444-8444-444444444444",
		"handoff_note":"Produce one exact receipt."
	}`
	request := httptest.NewRequest("POST", "/api/company-ops/assignments", strings.NewReader(valid))
	decoded, err := decodeCompanyOpsAssignmentRequest(request)
	if err != nil {
		t.Fatalf("decodeCompanyOpsAssignmentRequest: %v", err)
	}
	if decoded.HandoffNote != "Produce one exact receipt." {
		t.Fatalf("handoff_note = %q", decoded.HandoffNote)
	}
	if decoded.ProjectID != "44444444-4444-4444-8444-444444444444" {
		t.Fatalf("project_id = %q", decoded.ProjectID)
	}

	for name, body := range map[string]string{
		"unknown field":         strings.Replace(valid, "\n\t}", ",\n\t\t\"revision\":\"browser-forged\"\n\t}", 1),
		"trailing JSON":         valid + `{}`,
		"missing target":        strings.Replace(valid, `"employee_id":"EMP-P2-001",`, `"employee_id":"",`, 1),
		"blank handoff":         strings.Replace(valid, `"handoff_note":"Produce one exact receipt."`, `"handoff_note":"   "`, 1),
		"non-canonical project": strings.Replace(valid, `"project_id":"44444444-4444-4444-8444-444444444444"`, `"project_id":"44444444-4444-4444-8444-44444444444"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/company-ops/assignments", strings.NewReader(body))
			if _, err := decodeCompanyOpsAssignmentRequest(request); err == nil {
				t.Fatal("decodeCompanyOpsAssignmentRequest error = nil")
			}
		})
	}
}

func TestWriteCompanyOpsServiceError_ArtifactAndPromotionMappings(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{"idempotency required", companyops.ErrArtifactIdempotencyRequired, http.StatusBadRequest, "invalid_request"},
		{"project not found", service.ErrProjectNotFound, http.StatusBadRequest, "invalid_request"},
		{"promotion in progress", companyops.ErrArtifactPromotionInProgress, http.StatusConflict, "artifact_promotion_in_progress"},
		{"invalid transition", companyops.ErrInvalidArtifactTransition, http.StatusConflict, "artifact_conflict"},
		{"idempotency conflict", companyops.ErrArtifactIdempotencyConflict, http.StatusConflict, "artifact_conflict"},
		{"promotion claim conflict", companyops.ErrArtifactPromotionConflict, http.StatusConflict, "artifact_conflict"},
		{"formal ref mismatch", companyops.ErrFormalArtifactRefMismatch, http.StatusConflict, "artifact_conflict"},
		{"invalid candidate", companyops.ErrInvalidArtifactCandidate, http.StatusConflict, "artifact_conflict"},
		{"revision mismatch", companyops.ErrArtifactRevisionMismatch, http.StatusConflict, "artifact_conflict"},
		{"digest mismatch", companyops.ErrArtifactDigestMismatch, http.StatusConflict, "artifact_conflict"},
		{"object ref mismatch", companyops.ErrArtifactObjectRefMismatch, http.StatusConflict, "artifact_conflict"},
		{"candidate not found", companyops.ErrArtifactCandidateNotFound, http.StatusNotFound, "artifact_not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeCompanyOpsServiceError(rec, tc.err)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var body companyOpsErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if body.ReasonCode != tc.wantReason {
				t.Fatalf("reason_code = %q, want %q", body.ReasonCode, tc.wantReason)
			}
		})
	}
}
