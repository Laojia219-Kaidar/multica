package companyops

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestValidateArtifactReplicaLocationRequiresRegisteredStorageShape(t *testing.T) {
	base := ArtifactReplicaLocation{
		ID:                uuid.NewString(),
		WorkspaceID:       uuid.NewString(),
		OutcomeID:         uuid.NewString(),
		CandidateID:       uuid.NewString(),
		CandidateRevision: 1,
		Location: ArtifactStorageLocation{
			ID:        "nas-primary-1",
			Class:     ArtifactStorageLocationNASPrimary,
			StorageID: "synology-fixture",
			ObjectRef: "fixture://artifact/nas-primary-1/object",
		},
		State:    ArtifactReplicaLocationRegistered,
		Metadata: []byte(`{"role":"primary"}`),
	}
	if err := validateArtifactReplicaLocation(base); err != nil {
		t.Fatalf("valid location rejected: %v", err)
	}

	for name, mutate := range map[string]func(*ArtifactReplicaLocation){
		"unknown state": func(value *ArtifactReplicaLocation) {
			value.State = "mounted"
		},
		"unknown class": func(value *ArtifactReplicaLocation) {
			value.Location.Class = "real-nas"
		},
		"negative size": func(value *ArtifactReplicaLocation) {
			value.SizeBytes = -1
		},
		"metadata array": func(value *ArtifactReplicaLocation) {
			value.Metadata = []byte(`[]`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := validateArtifactReplicaLocation(value); !errors.Is(err, ErrInvalidArtifactReplicaLocation) {
				t.Fatalf("validation error = %v, want %v", err, ErrInvalidArtifactReplicaLocation)
			}
		})
	}
}

func TestArtifactReplicaLocationLedgerIsWorkspaceScopedAndStateOnly(t *testing.T) {
	fixture := newArtifactPersistenceFixture(t)
	candidate := fixture.createCandidate(t, "replica-location")
	repo := NewArtifactReplicaLocationRepository(fixture.tx)
	location := ArtifactReplicaLocation{
		ID:                uuid.NewString(),
		WorkspaceID:       fixture.workspaceID,
		OutcomeID:         candidate.LineageID,
		CandidateID:       candidate.ID,
		CandidateRevision: candidate.Revision,
		Location: ArtifactStorageLocation{
			ID:            "nas-primary-1",
			Class:         ArtifactStorageLocationNASPrimary,
			StorageID:     "synology-fixture",
			ObjectRef:     "fixture://artifact/nas-primary-1/object",
			RetentionHint: "primary",
		},
		State:          ArtifactReplicaLocationFixture,
		Digest:         candidate.Digest,
		MetadataDigest: "sha256:metadata-fixture",
		SizeBytes:      42,
		Metadata:       []byte(`{"source":"fixture"}`),
	}
	stored, err := repo.Record(fixture.ctx, location)
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if stored.State != ArtifactReplicaLocationFixture || stored.Location.ObjectRef != location.Location.ObjectRef {
		t.Fatalf("stored fixture location = %+v", stored)
	}

	replayed, err := repo.Record(fixture.ctx, location)
	if err != nil {
		t.Fatalf("Record() replay error = %v", err)
	}
	if replayed.ID != location.ID || replayed.State != ArtifactReplicaLocationFixture {
		t.Fatalf("replay changed identity/state: %+v", replayed)
	}
	mutatedIdentity := location
	mutatedIdentity.ID = uuid.NewString()
	mutatedIdentity.Location.ObjectRef += "/different"
	if _, err := repo.Record(fixture.ctx, mutatedIdentity); !errors.Is(err, ErrArtifactReplicaLocationConflict) {
		t.Fatalf("mutated identity replay error = %v, want %v", err, ErrArtifactReplicaLocationConflict)
	}

	verified, err := repo.UpdateState(
		fixture.ctx,
		fixture.workspaceID,
		location.ID,
		ArtifactReplicaLocationVerified,
		candidate.Digest,
		"sha256:metadata-verified",
		42,
	)
	if err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	if verified.State != ArtifactReplicaLocationVerified || verified.MetadataDigest != "sha256:metadata-verified" {
		t.Fatalf("verified location = %+v", verified)
	}

	byOutcome, err := repo.ListByOutcome(fixture.ctx, fixture.workspaceID, candidate.LineageID)
	if err != nil || len(byOutcome) != 1 || byOutcome[0].State != ArtifactReplicaLocationVerified {
		t.Fatalf("ListByOutcome() = (%+v, %v), want one verified row", byOutcome, err)
	}
	byCandidate, err := repo.ListByCandidate(fixture.ctx, fixture.workspaceID, candidate.ID)
	if err != nil || len(byCandidate) != 1 {
		t.Fatalf("ListByCandidate() = (%+v, %v), want one row", byCandidate, err)
	}
	otherWorkspace := uuid.NewString()
	if rows, err := repo.ListByCandidate(fixture.ctx, otherWorkspace, candidate.ID); err != nil || len(rows) != 0 {
		t.Fatalf("cross-workspace candidate read = (%+v, %v), want empty", rows, err)
	}
}
