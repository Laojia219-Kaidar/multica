package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestDecodeCompanyOpsAssignmentRequestStrictJSON(t *testing.T) {
	valid := `{
		"command_id":"11111111-1111-4111-8111-111111111111",
		"work_order_source_ref":"hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001",
		"employee_id":"EMP-P2-001",
		"identity_binding_id":"BIND-P2-001",
		"agent_id":"22222222-2222-4222-8222-222222222222",
		"session_id":"33333333-3333-4333-8333-333333333333",
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

	for name, body := range map[string]string{
		"unknown field":  strings.Replace(valid, "\n}", ",\n\"revision\":\"browser-forged\"\n}", 1),
		"trailing JSON":  valid + `{}`,
		"missing target": strings.Replace(valid, `"employee_id":"EMP-P2-001",`, `"employee_id":"",`, 1),
		"blank handoff":  strings.Replace(valid, `"handoff_note":"Produce one exact receipt."`, `"handoff_note":"   "`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/company-ops/assignments", strings.NewReader(body))
			if _, err := decodeCompanyOpsAssignmentRequest(request); err == nil {
				t.Fatal("decodeCompanyOpsAssignmentRequest error = nil")
			}
		})
	}
}
