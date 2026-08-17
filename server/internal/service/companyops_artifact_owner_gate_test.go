package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
)

// ownerGateAtomicAuthority is deliberately safe for concurrent calls. The
// owner-gate tests must prove that a rejected service call never reaches the
// Authority boundary, even if callers race.
type ownerGateAtomicAuthority struct {
	promoteCount atomic.Int64
	readCount    atomic.Int64
}

func (a *ownerGateAtomicAuthority) PromoteFormalArtifact(
	_ context.Context,
	input companyops.HiveCosmFormalArtifactPromotionRequest,
) (companyops.HiveCosmFormalArtifactPromotionReceipt, error) {
	a.promoteCount.Add(1)
	return companyops.HiveCosmFormalArtifactPromotionReceipt{
		PromotionID:    input.PromotionID,
		WritePerformed: true,
		Artifact: companyops.HiveCosmFormalArtifact{
			FormalArtifactRef: promotionTestFormalArtifactRef,
		},
	}, nil
}

func (a *ownerGateAtomicAuthority) ReadFormalArtifact(
	_ context.Context,
	_ companyops.HiveCosmAuthorityLookup,
	_ companyops.HiveCosmFormalArtifactCandidate,
	_ string,
) (companyops.HiveCosmFormalArtifact, error) {
	a.readCount.Add(1)
	return companyops.HiveCosmFormalArtifact{FormalArtifactRef: promotionTestFormalArtifactRef}, nil
}

func newOwnerGateArtifactService(
	t *testing.T,
	fixture companyOpsExecutionTestFixture,
	authority *ownerGateAtomicAuthority,
) *CompanyOpsArtifactService {
	t.Helper()
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	store := storage.NewLocalStorageFromEnv()
	artifactService, err := NewCompanyOpsArtifactService(fixture.queries, fixture.pool, store, fixture.service, authority)
	if err != nil {
		t.Fatalf("NewCompanyOpsArtifactService: %v", err)
	}
	return artifactService
}

func TestCompanyOpsArtifactOwnerGateRejectsNonOwnersBeforeAuthority(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	authority := &ownerGateAtomicAuthority{}
	artifactService := newOwnerGateArtifactService(t, fixture, authority)

	defer func() {
		if _, err := fixture.pool.Exec(ctx, `UPDATE member SET role = 'owner' WHERE workspace_id = $1 AND user_id = $2`, fixture.company.workspaceID, fixture.company.userID); err != nil {
			t.Errorf("restore fixture Owner role: %v", err)
		}
	}()

	for _, role := range []string{"admin", "member"} {
		t.Run(role, func(t *testing.T) {
			if _, err := fixture.pool.Exec(ctx, `UPDATE member SET role = $1 WHERE workspace_id = $2 AND user_id = $3`, role, fixture.company.workspaceID, fixture.company.userID); err != nil {
				t.Fatalf("set fixture role %q: %v", role, err)
			}

			if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, CompanyOpsArtifactReview{
				CandidateID:   uuid.NewString(),
				Decision:      companyops.ArtifactEventApproved,
				IdempotencyID: uuid.NewString(),
				ActorUserID:   fixture.company.userID,
			}); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
				t.Fatalf("ReviewArtifact(%s) error = %v, want Owner conflict", role, err)
			}

			promotion := companyOpsArtifactPromotionRequest(fixture, uuid.NewString(), uuid.NewString())
			promotion.ActorUserID = fixture.company.userID
			if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
				t.Fatalf("PromoteArtifact(%s) error = %v, want Owner conflict", role, err)
			}
		})
	}

	absent := companyOpsArtifactPromotionRequest(fixture, uuid.NewString(), uuid.NewString())
	absent.ActorUserID = util.MustParseUUID(uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, absent); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("PromoteArtifact(absent actor) error = %v, want Owner conflict", err)
	}
	if authority.promoteCount.Load() != 0 || authority.readCount.Load() != 0 {
		t.Fatalf("non-Owner service calls reached Authority: promote=%d read=%d", authority.promoteCount.Load(), authority.readCount.Load())
	}
}

func TestCompanyOpsArtifactPromotionRejectsApprovalActorAndCandidateDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(productionCompanyOpsFixture, *CompanyOpsArtifactOutcome, *syntheticArtifactEvent)
	}{
		{
			name: "approval actor mismatch",
			mutate: func(_ productionCompanyOpsFixture, _ *CompanyOpsArtifactOutcome, event *syntheticArtifactEvent) {
				event.actorUserID = uuid.NewString()
			},
		},
		{
			name: "candidate revision drift",
			mutate: func(_ productionCompanyOpsFixture, outcome *CompanyOpsArtifactOutcome, event *syntheticArtifactEvent) {
				event.candidateRevision = outcome.Candidate.Revision + 1
			},
		},
		{
			name: "candidate digest drift",
			mutate: func(_ productionCompanyOpsFixture, _ *CompanyOpsArtifactOutcome, event *syntheticArtifactEvent) {
				event.candidateDigest = "sha256:approval-drift"
			},
		},
		{
			name: "candidate object ref drift",
			mutate: func(_ productionCompanyOpsFixture, _ *CompanyOpsArtifactOutcome, event *syntheticArtifactEvent) {
				event.candidateObjectRef += "/drift"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, fixture := newCompanyOpsExecutionTestFixture(t)
			authority := &ownerGateAtomicAuthority{}
			artifactService := newOwnerGateArtifactService(t, fixture, authority)
			outcome := materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)
			event := syntheticArtifactEvent{
				typeName:           string(companyops.ArtifactEventApproved),
				candidateID:        outcome.Candidate.ID,
				candidateRevision:  outcome.Candidate.Revision,
				candidateDigest:    outcome.Candidate.Digest,
				candidateObjectRef: outcome.Candidate.DurableObjectRef,
				actorUserID:        util.UUIDToString(fixture.company.userID),
				idempotencyKey:     "owner-review:" + uuid.NewString(),
			}
			test.mutate(fixture.company, outcome, &event)
			insertSyntheticArtifactEvent(t, ctx, fixture, outcome, event)

			promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
			if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
				t.Fatalf("PromoteArtifact(%s) error = %v, want conflict", test.name, err)
			}
			if authority.promoteCount.Load() != 0 || authority.readCount.Load() != 0 {
				t.Fatalf("approval drift reached Authority: promote=%d read=%d", authority.promoteCount.Load(), authority.readCount.Load())
			}
		})
	}
}

func TestCompanyOpsArtifactPromotionRejectsLegacyApprovalWithoutActor(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	authority := &ownerGateAtomicAuthority{}
	artifactService := newOwnerGateArtifactService(t, fixture, authority)
	outcome := materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)
	insertSyntheticArtifactEvent(t, ctx, fixture, outcome, syntheticArtifactEvent{
		typeName:           string(companyops.ArtifactEventApproved),
		candidateID:        outcome.Candidate.ID,
		candidateRevision:  outcome.Candidate.Revision,
		candidateDigest:    outcome.Candidate.Digest,
		candidateObjectRef: outcome.Candidate.DurableObjectRef,
		idempotencyKey:     "legacy-owner-review:" + uuid.NewString(),
	})

	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("legacy approval without actor error = %v, want conflict", err)
	}
	if authority.promoteCount.Load() != 0 || authority.readCount.Load() != 0 {
		t.Fatalf("legacy approval reached Authority: promote=%d read=%d", authority.promoteCount.Load(), authority.readCount.Load())
	}
}

func TestCompanyOpsArtifactPromotionRejectsDuplicateAndSupersedingApprovalStates(t *testing.T) {
	for _, eventType := range []string{
		string(companyops.ArtifactEventApproved),
		string(companyops.ArtifactEventChangesRequested),
		"rejected",
	} {
		t.Run(eventType, func(t *testing.T) {
			ctx, fixture := newCompanyOpsExecutionTestFixture(t)
			authority := &ownerGateAtomicAuthority{}
			artifactService := newOwnerGateArtifactService(t, fixture, authority)
			outcome := materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)
			approved := syntheticArtifactEvent{
				typeName:           string(companyops.ArtifactEventApproved),
				candidateID:        outcome.Candidate.ID,
				candidateRevision:  outcome.Candidate.Revision,
				candidateDigest:    outcome.Candidate.Digest,
				candidateObjectRef: outcome.Candidate.DurableObjectRef,
				actorUserID:        util.UUIDToString(fixture.company.userID),
				idempotencyKey:     "owner-review:" + uuid.NewString(),
			}
			insertSyntheticArtifactEvent(t, ctx, fixture, outcome, approved)
			if eventType == string(companyops.ArtifactEventApproved) {
				approved.idempotencyKey = "owner-review:" + uuid.NewString()
			}
			approved.typeName = eventType
			approved.idempotencyKey = "synthetic:" + eventType + ":" + uuid.NewString()
			insertSyntheticArtifactEvent(t, ctx, fixture, outcome, approved)

			promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
			if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err == nil {
				t.Fatalf("PromoteArtifact with %s state unexpectedly succeeded", eventType)
			}
			if authority.promoteCount.Load() != 0 || authority.readCount.Load() != 0 {
				t.Fatalf("%s state reached Authority: promote=%d read=%d", eventType, authority.promoteCount.Load(), authority.readCount.Load())
			}
		})
	}
}

func TestArtifactLifecycleIdempotencyBindsApprovalActor(t *testing.T) {
	candidate, err := companyops.NewArtifactCandidate(companyops.ArtifactCandidateInput{
		ID:               "candidate-owner-actor",
		LineageID:        "lineage-owner-actor",
		Revision:         1,
		DurableObjectRef: "object://owner-actor/candidate",
		Digest:           "sha256:owner-actor",
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := companyops.NewArtifactLifecycle(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Append(companyops.ArtifactEventInput{
		Type:               companyops.ArtifactEventSubmitted,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		IdempotencyKey:     "submit-owner-actor",
	}); err != nil {
		t.Fatal(err)
	}
	approved := companyops.ArtifactEventInput{
		Type:               companyops.ArtifactEventApproved,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		IdempotencyKey:     "approve-owner-actor",
		ActorUserID:        "owner-a",
	}
	if _, err := lifecycle.Append(approved); err != nil {
		t.Fatal(err)
	}
	approved.ActorUserID = "owner-b"
	if _, err := lifecycle.Append(approved); !errors.Is(err, companyops.ErrArtifactIdempotencyConflict) {
		t.Fatalf("different actor idempotency replay error = %v, want conflict", err)
	}
}

func TestPromotionClaimPayloadBindsWriterAndApprovalEvidenceDigests(t *testing.T) {
	base := companyops.PromotionClaimPayload{
		CommandSchemaVersion:    companyops.HiveCosmFormalArtifactPromotionCommandV1,
		ActorUserID:             uuid.NewString(),
		LookupWorkOrderRef:      "hive://work-order/1",
		LookupEmployeeID:        "EMP-1",
		LookupBindingID:         "BIND-1",
		LookupAgentID:           uuid.NewString(),
		WorkOrderRef:            "hive://work-order/1",
		WorkOrderRevision:       "r1",
		WorkOrderContentDigest:  "sha256:work-order",
		EmployeeRef:             "hive://employee/1",
		EmployeeRevision:        "r1",
		EmployeeContentDigest:   "sha256:employee",
		BindingRef:              "hive://binding/1",
		BindingRevision:         "r1",
		BindingContentDigest:    "sha256:binding",
		CandidateRevision:       1,
		CandidateDigest:         "sha256:candidate",
		CandidateObjectRef:      "object://candidate/1",
		CandidateContentType:    companyops.HiveCosmFormalArtifactContentTypeMarkdown,
		ApprovalEventID:         uuid.NewString(),
		SourceTaskID:            uuid.NewString(),
		WriterLeaseTargetDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		CompletionReceiptDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	baseDigest := base.Digest()
	mutations := []struct {
		name   string
		mutate func(*companyops.PromotionClaimPayload)
	}{
		{"actor", func(p *companyops.PromotionClaimPayload) { p.ActorUserID = uuid.NewString() }},
		{"agent", func(p *companyops.PromotionClaimPayload) { p.LookupAgentID = uuid.NewString() }},
		{"candidate", func(p *companyops.PromotionClaimPayload) { p.CandidateDigest = "sha256:candidate-drift" }},
		{"approval event", func(p *companyops.PromotionClaimPayload) { p.ApprovalEventID = uuid.NewString() }},
		{"source task", func(p *companyops.PromotionClaimPayload) { p.SourceTaskID = uuid.NewString() }},
		{"migration-406 target", func(p *companyops.PromotionClaimPayload) {
			p.WriterLeaseTargetDigest = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
		}},
		{"migration-407 receipt", func(p *companyops.PromotionClaimPayload) {
			p.CompletionReceiptDigest = "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := base
			mutation.mutate(&changed)
			if changed.Digest() == baseDigest {
				t.Fatalf("payload digest did not change after %s drift", mutation.name)
			}
		})
	}
}

type syntheticArtifactEvent struct {
	typeName           string
	candidateID        string
	candidateRevision  int
	candidateDigest    string
	candidateObjectRef string
	actorUserID        string
	idempotencyKey     string
}

func insertSyntheticArtifactEvent(t *testing.T, ctx context.Context, fixture companyOpsExecutionTestFixture, outcome *CompanyOpsArtifactOutcome, event syntheticArtifactEvent) {
	t.Helper()
	var sequence int
	if err := fixture.pool.QueryRow(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM artifact_event WHERE workspace_id = $1 AND lineage_id = $2`, fixture.company.workspaceID, outcome.CommandID).Scan(&sequence); err != nil {
		t.Fatalf("next synthetic artifact event sequence: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO artifact_event (
			id, workspace_id, lineage_id, sequence, event_type,
			candidate_id, candidate_revision, candidate_digest,
			candidate_object_ref, idempotency_key, actor_user_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		uuid.New(), fixture.company.workspaceID, outcome.CommandID, sequence, event.typeName,
		util.MustParseUUID(event.candidateID), event.candidateRevision, event.candidateDigest,
		event.candidateObjectRef, event.idempotencyKey, syntheticActorUUID(event.actorUserID)); err != nil {
		t.Fatalf("insert synthetic artifact event %s: %v", event.typeName, err)
	}
}

func syntheticActorUUID(actor string) any {
	if actor == "" {
		return nil
	}
	return util.MustParseUUID(actor)
}
