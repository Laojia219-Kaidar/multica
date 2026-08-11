package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

const (
	formalClientPromotionID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c010"
	formalClientCandidateID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c011"
	formalClientApprovalID  = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c012"
)

func formalClientInput() HiveCosmFormalArtifactPromotionRequest {
	lookup := authorityClientLookup()
	return HiveCosmFormalArtifactPromotionRequest{
		PromotionID: formalClientPromotionID,
		Lookup:      lookup,
		WorkOrder: AuthoritySnapshot{
			Revision:      "sha256:" + repeatHex("a"),
			ContentDigest: "sha256:" + repeatHex("a"),
		},
		Employee: AuthoritySnapshot{
			Revision:      "sha256:" + repeatHex("b"),
			ContentDigest: "sha256:" + repeatHex("b"),
		},
		IdentityBinding: AuthoritySnapshot{
			Revision:      "sha256:" + repeatHex("c"),
			ContentDigest: "sha256:" + repeatHex("c"),
		},
		Candidate: HiveCosmFormalArtifactCandidate{
			ID:               formalClientCandidateID,
			Revision:         2,
			DurableObjectRef: "/uploads/hivecrew/outcome-r2.md",
			ContentDigest:    "sha256:" + repeatHex("d"),
			ApprovalEventID:  formalClientApprovalID,
		},
	}
}

func repeatHex(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}

func formalClientArtifactWire(input HiveCosmFormalArtifactPromotionRequest) formalArtifactAuthorityWire {
	artifactID := "FA-HCW-" + "01972F7E-7E8D-77EF-A13D-1B0CE3E9C010"
	return formalArtifactAuthorityWire{
		SchemaVersion:      HiveCosmFormalArtifactAuthorityV1,
		FormalArtifactRef:  input.Lookup.WorkOrderSourceRef + "/formal-artifact/" + artifactID,
		Revision:           "sha256:" + repeatHex("e"),
		ContentDigest:      "sha256:" + repeatHex("e"),
		Freshness:          "current",
		Status:             "formal",
		ProjectID:          "PRJ-HIVECREW-P2",
		WorkOrderID:        "WO-OWNER-JOURNEY-001",
		AssignmentID:       "ASG-HIVECREW-P2-001",
		EmployeeID:         input.Lookup.EmployeeID,
		AgentID:            input.Lookup.AgentID,
		IdentityBindingID:  input.Lookup.IdentityBindingID,
		ArtifactManifestID: artifactID,
		ContentObjectID:    "hivecrew:" + input.Candidate.ID + ":r2",
		ContentRef:         input.Candidate.DurableObjectRef,
		TemporaryArtifact: formalArtifactTemporaryWire{
			CandidateID:   input.Candidate.ID,
			Revision:      input.Candidate.Revision,
			ContentDigest: input.Candidate.ContentDigest,
		},
		OwnerReview: formalArtifactOwnerReviewWire{
			ReviewDecisionID: "REV-HCW-01972F7E-7E8D-77EF-A13D-1B0CE3E9C010",
			ReviewerID:       "HP-WILLIAM-001",
			Decision:         "accept",
			ApprovalEventID:  input.Candidate.ApprovalEventID,
		},
	}
}

func formalClientTransitionWire(input HiveCosmFormalArtifactPromotionRequest, formalArtifactRef string) *formalArtifactWorkOrderTransitionWire {
	return &formalArtifactWorkOrderTransitionWire{
		WorkOrderSourceRef: input.Lookup.WorkOrderSourceRef,
		PreviousAuthority: formalArtifactExpectedAuthorityWire{
			Revision:      input.WorkOrder.Revision,
			ContentDigest: input.WorkOrder.ContentDigest,
		},
		ResultingAuthority: formalArtifactExpectedAuthorityWire{
			Revision:      "sha256:" + repeatHex("1"),
			ContentDigest: "sha256:" + repeatHex("1"),
		},
		PromotionID:       input.PromotionID,
		CandidateID:       input.Candidate.ID,
		ApprovalEventID:   input.Candidate.ApprovalEventID,
		FormalArtifactRef: formalArtifactRef,
	}
}

func TestHiveCosmFormalArtifactClient_PromoteAndExactReadback(t *testing.T) {
	input := formalClientInput()
	artifact := formalClientArtifactWire(input)
	transition := formalClientTransitionWire(input, artifact.FormalArtifactRef)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == HiveCosmFormalArtifactPromotionEndpoint:
			var command formalArtifactPromotionCommandWire
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&command); err != nil {
				t.Fatalf("decode Promotion command: %v", err)
			}
			if command.SchemaVersion != HiveCosmFormalArtifactPromotionCommandV1 || command.PromotionID != input.PromotionID || command.Artifact.CandidateID != input.Candidate.ID {
				t.Fatalf("Promotion command = %+v", command)
			}
			_ = json.NewEncoder(w).Encode(formalArtifactPromotionEnvelope{
				SchemaVersion:  HiveCosmFormalArtifactPromotionReceiptV1,
				PromotionID:    input.PromotionID,
				WritePerformed: true,
				Artifact:       artifact,
			})
		case r.Method == http.MethodGet && r.URL.Path == HiveCosmFormalArtifactReadEndpointPrefix+artifact.ArtifactManifestID:
			if r.URL.Query().Get("employee_id") != input.Lookup.EmployeeID || len(r.URL.Query()) != 4 {
				t.Fatalf("readback query = %v", r.URL.Query())
			}
			_ = json.NewEncoder(w).Encode(formalArtifactReadbackEnvelope{
				SchemaVersion:       HiveCosmFormalArtifactAuthorityV1,
				LookupMode:          "exact",
				Complete:            true,
				OK:                  true,
				Request:             formalArtifactLookupWire(input.Lookup),
				Artifact:            artifact,
				WorkOrderTransition: transition,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewHiveCosmAuthorityClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("NewHiveCosmAuthorityClient: %v", err)
	}
	receipt, err := client.PromoteFormalArtifact(context.Background(), input)
	if err != nil {
		t.Fatalf("PromoteFormalArtifact: %v", err)
	}
	if !receipt.WritePerformed || receipt.PromotionID != input.PromotionID || receipt.Artifact.FormalArtifactRef != artifact.FormalArtifactRef {
		t.Fatalf("Promotion receipt = %+v", receipt)
	}
	readback, err := client.ReadFormalArtifact(context.Background(), input.Lookup, input.Candidate, artifact.ArtifactManifestID)
	if err != nil {
		t.Fatalf("ReadFormalArtifact: %v", err)
	}
	if !reflect.DeepEqual(readback, readbackArtifactToPublic(artifact, transition)) {
		t.Fatalf("readback = %+v, receipt = %+v", readback, receipt.Artifact)
	}
}

func readbackArtifactToPublic(wire formalArtifactAuthorityWire, transitionWire *formalArtifactWorkOrderTransitionWire) HiveCosmFormalArtifact {
	artifact, err := validateFormalArtifactAuthority(wire, formalClientInput().Lookup, formalClientInput().Candidate, wire.ArtifactManifestID)
	if err != nil {
		panic(err)
	}
	transition, err := validateFormalArtifactWorkOrderTransition(transitionWire, formalClientInput().Lookup, formalClientInput().Candidate, wire.FormalArtifactRef)
	if err != nil {
		panic(err)
	}
	artifact.WorkOrderTransition = &transition
	return artifact
}

func TestHiveCosmFormalArtifactClient_ReadbackRejectsApprovedCandidateTamper(t *testing.T) {
	input := formalClientInput()
	baseArtifact := formalClientArtifactWire(input)
	transition := formalClientTransitionWire(input, baseArtifact.FormalArtifactRef)
	tests := []struct {
		name   string
		tamper func(*formalArtifactAuthorityWire)
	}{
		{name: "candidate id", tamper: func(wire *formalArtifactAuthorityWire) {
			wire.TemporaryArtifact.CandidateID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c099"
		}},
		{name: "candidate revision", tamper: func(wire *formalArtifactAuthorityWire) { wire.TemporaryArtifact.Revision++ }},
		{name: "candidate digest", tamper: func(wire *formalArtifactAuthorityWire) {
			wire.TemporaryArtifact.ContentDigest = "sha256:" + repeatHex("f")
		}},
		{name: "content ref", tamper: func(wire *formalArtifactAuthorityWire) { wire.ContentRef = "/uploads/hivecrew/other.md" }},
		{name: "approval event", tamper: func(wire *formalArtifactAuthorityWire) {
			wire.OwnerReview.ApprovalEventID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c098"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := baseArtifact
			test.tamper(&artifact)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(formalArtifactReadbackEnvelope{
					SchemaVersion:       HiveCosmFormalArtifactAuthorityV1,
					LookupMode:          "exact",
					Complete:            true,
					OK:                  true,
					Request:             formalArtifactLookupWire(input.Lookup),
					Artifact:            artifact,
					WorkOrderTransition: transition,
				})
			}))
			defer server.Close()
			client, err := NewHiveCosmAuthorityClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ReadFormalArtifact(context.Background(), input.Lookup, input.Candidate, baseArtifact.ArtifactManifestID)
			var authorityErr *HiveCosmAuthorityError
			if !errors.As(err, &authorityErr) || authorityErr.Kind != HiveCosmAuthorityInvalid {
				t.Fatalf("tampered readback error = %v, want invalid", err)
			}
		})
	}
}

func TestHiveCosmFormalArtifactClient_ReadbackRejectsWorkOrderTransitionTamper(t *testing.T) {
	input := formalClientInput()
	baseArtifact := formalClientArtifactWire(input)
	baseTransition := formalClientTransitionWire(input, baseArtifact.FormalArtifactRef)
	tests := []struct {
		name   string
		tamper func(**formalArtifactWorkOrderTransitionWire)
	}{
		{name: "missing proof", tamper: func(wire **formalArtifactWorkOrderTransitionWire) { *wire = nil }},
		{name: "source ref", tamper: func(wire **formalArtifactWorkOrderTransitionWire) { (*wire).WorkOrderSourceRef += "-other" }},
		{name: "previous revision", tamper: func(wire **formalArtifactWorkOrderTransitionWire) { (*wire).PreviousAuthority.Revision = " bad" }},
		{name: "previous digest", tamper: func(wire **formalArtifactWorkOrderTransitionWire) {
			(*wire).PreviousAuthority.ContentDigest = "sha256:BAD"
		}},
		{name: "resulting revision", tamper: func(wire **formalArtifactWorkOrderTransitionWire) { (*wire).ResultingAuthority.Revision = "" }},
		{name: "resulting digest", tamper: func(wire **formalArtifactWorkOrderTransitionWire) {
			(*wire).ResultingAuthority.ContentDigest = "sha256:BAD"
		}},
		{name: "promotion id", tamper: func(wire **formalArtifactWorkOrderTransitionWire) { (*wire).PromotionID = "not-a-uuid" }},
		{name: "candidate id", tamper: func(wire **formalArtifactWorkOrderTransitionWire) {
			(*wire).CandidateID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c099"
		}},
		{name: "approval id", tamper: func(wire **formalArtifactWorkOrderTransitionWire) {
			(*wire).ApprovalEventID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c098"
		}},
		{name: "formal ref", tamper: func(wire **formalArtifactWorkOrderTransitionWire) { (*wire).FormalArtifactRef += "-other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proof := *baseTransition
			transition := &proof
			test.tamper(&transition)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(formalArtifactReadbackEnvelope{
					SchemaVersion:       HiveCosmFormalArtifactAuthorityV1,
					LookupMode:          "exact",
					Complete:            true,
					OK:                  true,
					Request:             formalArtifactLookupWire(input.Lookup),
					Artifact:            baseArtifact,
					WorkOrderTransition: transition,
				})
			}))
			defer server.Close()
			client, err := NewHiveCosmAuthorityClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ReadFormalArtifact(context.Background(), input.Lookup, input.Candidate, baseArtifact.ArtifactManifestID)
			var authorityErr *HiveCosmAuthorityError
			if !errors.As(err, &authorityErr) || authorityErr.Kind != HiveCosmAuthorityInvalid {
				t.Fatalf("tampered transition error = %v, want invalid", err)
			}
		})
	}
}

func TestHiveCosmFormalArtifactClient_FailsClosedOnTamperAndConflict(t *testing.T) {
	input := formalClientInput()
	artifact := formalClientArtifactWire(input)
	artifact.EmployeeID = "KT-OTHER"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(formalArtifactPromotionEnvelope{
			SchemaVersion:  HiveCosmFormalArtifactPromotionReceiptV1,
			PromotionID:    input.PromotionID,
			WritePerformed: true,
			Artifact:       artifact,
		})
	}))
	defer server.Close()
	client, err := NewHiveCosmAuthorityClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.PromoteFormalArtifact(context.Background(), input)
	var authorityErr *HiveCosmAuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Kind != HiveCosmAuthorityInvalid {
		t.Fatalf("tampered receipt error = %v", err)
	}

	conflictServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(formalArtifactPromotionEnvelope{
			SchemaVersion: HiveCosmFormalArtifactPromotionReceiptV1,
			Error:         &hiveCosmAuthorityWireError{Code: "conflict", ObjectKind: "formal_artifact"},
		})
	}))
	defer conflictServer.Close()
	conflictClient, _ := NewHiveCosmAuthorityClient(conflictServer.URL, conflictServer.Client())
	_, err = conflictClient.PromoteFormalArtifact(context.Background(), input)
	if !errors.As(err, &authorityErr) || authorityErr.Kind != HiveCosmAuthorityConflict {
		t.Fatalf("conflict error = %v", err)
	}
}
