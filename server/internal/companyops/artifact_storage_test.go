package companyops

import (
	"context"
	"errors"
	"testing"
)

func TestAssessArtifactDurabilityRequiresMatchingLocalAndOffsiteReplicas(t *testing.T) {
	const digest = "sha256:content"
	const metadata = "sha256:metadata"
	base := []ArtifactReplica{
		{Location: ArtifactStorageLocation{ID: "nas-1", Class: ArtifactStorageLocationNASPrimary}, State: ArtifactReplicaVerified, Digest: digest, MetadataDigest: metadata},
	}
	assessment := AssessArtifactDurability(base, digest, metadata)
	if assessment.Durable || !assessment.LocalPrimaryVerified || assessment.OfflineOrCloudVerified {
		t.Fatalf("local-only assessment = %+v, want non-durable", assessment)
	}
	base = append(base, ArtifactReplica{Location: ArtifactStorageLocation{ID: "offline-1", Class: ArtifactStorageLocationOfflineCopy}, State: ArtifactReplicaVerified, Digest: digest, MetadataDigest: metadata})
	assessment = AssessArtifactDurability(base, digest, metadata)
	if !assessment.Durable || !assessment.MetadataAndHashMatch {
		t.Fatalf("matching replicas assessment = %+v, want durable", assessment)
	}
	base[1].Digest = "sha256:mismatch"
	assessment = AssessArtifactDurability(base, digest, metadata)
	if assessment.Durable {
		t.Fatalf("mismatched replica assessment = %+v, want non-durable", assessment)
	}
}

func TestEvaluateArtifactCapacityReturnsBlockedWithoutMutation(t *testing.T) {
	decision, err := EvaluateArtifactCapacity(ArtifactCapacityEstimate{
		AvailableBytes:    100,
		InputBytes:        20,
		IntermediateBytes: 30,
		RenderBytes:       25,
		ReplicaBytes:      20,
		HeadroomBytes:     10,
	})
	if err != nil {
		t.Fatalf("EvaluateArtifactCapacity() error = %v", err)
	}
	if decision.Status != ArtifactCapacityBlocked || decision.ShortfallBytes != 5 {
		t.Fatalf("capacity decision = %+v, want CAPACITY_BLOCKED with 5 byte shortfall", decision)
	}
}

func TestFixtureArtifactObjectStoreReplicationAndVerification(t *testing.T) {
	ctx := context.Background()
	store := NewFixtureArtifactObjectStore()
	data := []byte("fixture artifact")
	key := "workspaces/ws/artifacts/a1/source"
	if _, err := store.Upload(ctx, key, data, "text/plain", "source.txt"); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	metadata := "sha256:metadata"
	replica, err := store.Replicate(ctx, key, ArtifactStorageLocation{ID: "cloud-1", Class: ArtifactStorageLocationCloudReplica}, metadata)
	if err != nil {
		t.Fatalf("Replicate() error = %v", err)
	}
	if replica.State != ArtifactReplicaPending {
		t.Fatalf("replica state = %q, want pending before verification", replica.State)
	}
	verified, err := store.VerifyReplica(ctx, replica, SHA256Digest(data), metadata)
	if err != nil {
		t.Fatalf("VerifyReplica() error = %v", err)
	}
	if verified.State != ArtifactReplicaVerified {
		t.Fatalf("verified state = %q, want verified", verified.State)
	}
}

func TestFixtureArtifactObjectStoreReplicationFailureAndSuspendedRecovery(t *testing.T) {
	ctx := context.Background()
	store := NewFixtureArtifactObjectStore()
	key := "workspaces/ws/artifacts/a1/source"
	store.SuspendUpload(key)
	if _, err := store.Upload(ctx, key, []byte("data"), "text/plain", "source.txt"); !errors.Is(err, ErrArtifactUploadSuspended) {
		t.Fatalf("suspended Upload() error = %v, want %v", err, ErrArtifactUploadSuspended)
	}
	store.ResumeUpload(key)
	if _, err := store.Upload(ctx, key, []byte("data"), "text/plain", "source.txt"); err != nil {
		t.Fatalf("resumed Upload() error = %v", err)
	}
	store.SetReplicationError(errors.New("cloud unavailable"))
	_, err := store.Replicate(ctx, key, ArtifactStorageLocation{ID: "cloud-1", Class: ArtifactStorageLocationCloudReplica}, "sha256:metadata")
	if !errors.Is(err, ErrArtifactReplicaCopyFailed) {
		t.Fatalf("failed Replicate() error = %v, want wrapped copy failure", err)
	}
	store.SetReplicationError(nil)
	if _, err := store.Replicate(ctx, key, ArtifactStorageLocation{ID: "cloud-1", Class: ArtifactStorageLocationCloudReplica}, "sha256:metadata"); err != nil {
		t.Fatalf("recovered Replicate() error = %v", err)
	}
}

func TestFixtureArtifactObjectStoreChecksumMismatchFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := NewFixtureArtifactObjectStore()
	key := "workspaces/ws/artifacts/a1/source"
	data := []byte("fixture artifact")
	if _, err := store.Upload(ctx, key, data, "text/plain", "source.txt"); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	replica, err := store.Replicate(ctx, key, ArtifactStorageLocation{ID: "offline-1", Class: ArtifactStorageLocationOfflineCopy}, "sha256:metadata")
	if err != nil {
		t.Fatalf("Replicate() error = %v", err)
	}
	if _, err := store.VerifyReplica(ctx, replica, "sha256:wrong", "sha256:metadata"); !errors.Is(err, ErrArtifactReplicaVerifyFailed) {
		t.Fatalf("mismatch VerifyReplica() error = %v, want wrapped verification failure", err)
	}
}
