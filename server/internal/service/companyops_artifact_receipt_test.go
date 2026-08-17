package service

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/companyops"
)

func TestWrapArtifactLedgerRestoreConflictPreservesCause(t *testing.T) {
	cause := errors.New("stored candidate digest/object ref drift")
	err := wrapArtifactLedgerRestoreConflict(cause)
	if !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("wrapped error = %v, want ErrCompanyOpsArtifactConflict", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("wrapped error = %v, lost persisted ledger cause", err)
	}
}

func TestDecodeDurablePromotionResponseRequiresExplicitReceiptFields(t *testing.T) {
	promotionID := uuid.NewString()
	approvalEventID := uuid.NewString()
	candidate := companyops.ArtifactCandidate{
		ID:               uuid.NewString(),
		Revision:         1,
		Digest:           "sha256:candidate",
		DurableObjectRef: "hive://candidate/object",
	}
	manifestID, err := companyops.FormalArtifactManifestID(promotionID)
	if err != nil {
		t.Fatal(err)
	}
	artifact := companyops.HiveCosmFormalArtifact{
		FormalArtifactRef:  "hive://formal-artifact/" + manifestID,
		ArtifactManifestID: manifestID,
		ContentRef:         candidate.DurableObjectRef,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		ReviewerID:         "HP-WILLIAM-001",
		ApprovalEventID:    approvalEventID,
	}

	base := map[string]any{
		"PromotionID":    promotionID,
		"WritePerformed": false,
		"Artifact":       artifact,
	}
	tests := []struct {
		name string
		edit func(map[string]any)
		want bool
	}{
		{name: "false is present", want: true},
		{name: "true is present", edit: func(v map[string]any) { v["WritePerformed"] = true }, want: true},
		{name: "missing write performed", edit: func(v map[string]any) { delete(v, "WritePerformed") }},
		{name: "null write performed", edit: func(v map[string]any) { v["WritePerformed"] = nil }},
		{name: "missing promotion id", edit: func(v map[string]any) { delete(v, "PromotionID") }},
		{name: "missing artifact", edit: func(v map[string]any) { delete(v, "Artifact") }},
		{name: "unknown field", edit: func(v map[string]any) { v["Unexpected"] = true }},
		{name: "trailing value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := make(map[string]any, len(base))
			for key, value := range base {
				payload[key] = value
			}
			if tt.edit != nil {
				tt.edit(payload)
			}
			raw, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if tt.name == "trailing value" {
				raw = append(raw, []byte(` {"unexpected":true}`)...)
			}
			_, decodeErr := decodeDurablePromotionResponse(raw, promotionID, candidate, approvalEventID)
			if (decodeErr == nil) != tt.want {
				t.Fatalf("decode error = %v, want success=%v", decodeErr, tt.want)
			}
		})
	}
}

func TestValidatePromotionAuthorityArtifactRequiresExactWorkOrderFormalRef(t *testing.T) {
	promotionID := uuid.NewString()
	approvalEventID := uuid.NewString()
	candidate := companyops.ArtifactCandidate{ID: uuid.NewString(), Revision: 1, Digest: "sha256:candidate", DurableObjectRef: "hive://candidate/object"}
	manifestID, err := companyops.FormalArtifactManifestID(promotionID)
	if err != nil {
		t.Fatal(err)
	}
	workOrder := companyops.AuthoritySnapshot{SourceRef: "hive://work-order/WO-1", Revision: "r1", ContentDigest: "sha256:work-order"}
	artifact := companyops.HiveCosmFormalArtifact{
		FormalArtifactRef:  workOrder.SourceRef + "/formal-artifact/" + manifestID,
		ArtifactManifestID: manifestID,
		ContentRef:         candidate.DurableObjectRef,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		ReviewerID:         "HP-WILLIAM-001",
		ApprovalEventID:    approvalEventID,
	}
	if err := validatePromotionAuthorityArtifact(artifact, candidate, promotionID, approvalEventID, workOrder, false); err != nil {
		t.Fatalf("valid formal ref rejected: %v", err)
	}
	artifact.FormalArtifactRef = "hive://other-work-order/formal-artifact/" + manifestID
	if err := validatePromotionAuthorityArtifact(artifact, candidate, promotionID, approvalEventID, workOrder, false); err == nil {
		t.Fatal("formal ref from a different WorkOrder was accepted")
	}
}
