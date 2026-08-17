package companyops

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidateC3b2PromotionClaimPayloadRequiresCompleteAuthorityBinding(t *testing.T) {
	workspaceID := uuid.NewString()
	promotionID := uuid.NewString()
	candidateID := uuid.NewString()
	lineageID := uuid.NewString()
	payload := PromotionClaimPayload{
		WorkspaceID:             workspaceID,
		PromotionID:             promotionID,
		IssueID:                 uuid.NewString(),
		AssignmentCommandID:     uuid.NewString(),
		AssignmentLineageID:     lineageID,
		AssignmentInitialTaskID: uuid.NewString(),
		LocalAgentID:            uuid.NewString(),
		CommandSchemaVersion:    HiveCosmFormalArtifactPromotionCommandV1,
		ActorUserID:             uuid.NewString(),
		LookupWorkOrderRef:      "hive://work-order/WO-1",
		LookupEmployeeID:        "EMP-1",
		LookupBindingID:         "BIND-1",
		LookupAgentID:           "AGENT-1",
		WorkOrderRef:            "hive://work-order/WO-1",
		WorkOrderRevision:       "r1",
		WorkOrderContentDigest:  "sha256:work-order",
		EmployeeRef:             "hive://employee/EMP-1",
		EmployeeRevision:        "r1",
		EmployeeContentDigest:   "sha256:employee",
		AgentRef:                "hive://agent/AGENT-1",
		AgentRevision:           "r1",
		AgentContentDigest:      "sha256:agent",
		BindingRef:              "hive://binding/BIND-1",
		BindingRevision:         "r1",
		BindingContentDigest:    "sha256:binding",
		CandidateRevision:       1,
		CandidateID:             candidateID,
		CandidateDigest:         "sha256:candidate",
		CandidateObjectRef:      "hive://candidate/object",
		CandidateContentType:    HiveCosmFormalArtifactContentTypeMarkdown,
		ApprovalActorUserID:     uuid.NewString(),
		ApprovalEventID:         uuid.NewString(),
		ApprovalEventSequence:   2,
		ApprovalEventType:       string(ArtifactEventApproved),
		ApprovalEventDigest:     "sha256:approval-event",
		SourceTaskID:            uuid.NewString(),
		WriterLeaseTargetDigest: "sha256:writer-lease",
		CompletionReceiptDigest: "sha256:completion-receipt",
	}
	if err := validateC3b2PromotionClaimPayload(payload, workspaceID, promotionID, candidateID, lineageID); err != nil {
		t.Fatalf("complete C3b2 payload rejected: %v", err)
	}

	missing := map[string]func(*PromotionClaimPayload){
		"schema":         func(p *PromotionClaimPayload) { p.CommandSchemaVersion = "" },
		"actor":          func(p *PromotionClaimPayload) { p.ActorUserID = "" },
		"lookup":         func(p *PromotionClaimPayload) { p.LookupAgentID = "" },
		"work order":     func(p *PromotionClaimPayload) { p.WorkOrderContentDigest = "" },
		"employee":       func(p *PromotionClaimPayload) { p.EmployeeRevision = "" },
		"binding":        func(p *PromotionClaimPayload) { p.BindingRef = "" },
		"candidate type": func(p *PromotionClaimPayload) { p.CandidateContentType = "" },
		"approval":       func(p *PromotionClaimPayload) { p.ApprovalEventDigest = "" },
		"source task":    func(p *PromotionClaimPayload) { p.SourceTaskID = "" },
		"lease digest":   func(p *PromotionClaimPayload) { p.WriterLeaseTargetDigest = "" },
		"receipt digest": func(p *PromotionClaimPayload) { p.CompletionReceiptDigest = "" },
	}
	for name, remove := range missing {
		t.Run(name, func(t *testing.T) {
			copy := payload
			remove(&copy)
			if err := validateC3b2PromotionClaimPayload(copy, workspaceID, promotionID, candidateID, lineageID); err == nil {
				t.Fatal("partial C3b2 payload was accepted")
			}
		})
	}
}

func TestValidateC3b2PromotionClaimPayloadKeepsEmptyLegacyPayloadCompatible(t *testing.T) {
	if err := validateC3b2PromotionClaimPayload(PromotionClaimPayload{}, uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()); err != nil {
		t.Fatalf("empty legacy payload rejected: %v", err)
	}
}
