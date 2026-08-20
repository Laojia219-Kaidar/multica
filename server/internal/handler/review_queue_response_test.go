package handler

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

func TestReviewQueueResponseCapabilitiesFailClosed(t *testing.T) {
	tests := []struct {
		name          string
		handler       Handler
		wantAuthority bool
		wantOutcome   bool
	}{
		{name: "no providers"},
		{
			name:          "authority evidence only",
			handler:       Handler{ReviewAuthorityEvidenceReady: true},
			wantAuthority: true,
		},
		{
			name:        "outcome center only",
			handler:     Handler{CompanyOpsOutcomeCenter: &service.CompanyOpsOutcomeCenterService{}},
			wantOutcome: true,
		},
		{
			name: "both providers",
			handler: Handler{
				ReviewAuthorityEvidenceReady: true,
				CompanyOpsOutcomeCenter:      &service.CompanyOpsOutcomeCenterService{},
			},
			wantAuthority: true,
			wantOutcome:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := test.handler.buildReviewQueueResponse([]reviewQueueItemResponse{})
			body, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			var wire struct {
				AuthorityReady     bool `json:"authority_ready"`
				OutcomeCenterReady bool `json:"outcome_center_ready"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if wire.AuthorityReady != test.wantAuthority || wire.OutcomeCenterReady != test.wantOutcome {
				t.Fatalf("readiness = authority:%v outcome:%v, want authority:%v outcome:%v", wire.AuthorityReady, wire.OutcomeCenterReady, test.wantAuthority, test.wantOutcome)
			}
		})
	}
}
