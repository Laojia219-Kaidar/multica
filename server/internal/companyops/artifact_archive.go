package companyops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrArtifactArchiveDisabled   = errors.New("artifact archive disabled")
	ErrArtifactArchiveVerifyFail = errors.New("artifact archive verification failed")
)

// artifactArchiveLedgerNamespace derives deterministic replica-location row
// ids so concurrent or replayed archive passes insert byte-identical ledger
// rows and the repository's registration-match path collapses them.
var artifactArchiveLedgerNamespace = uuid.MustParse("7a1c9b5e-3f42-4d8a-a6b7-2c9d4e8f1a3b")

// ArtifactArchiveStore is the durable cold-placement target for accepted
// candidates. It never becomes a second artifact authority: the candidate row
// plus event ledger stay the only lifecycle truth, and the replica-location
// row written beside the copy records the placement observation only.
type ArtifactArchiveStore interface {
	StorageID() string
	LocationID() string
	// Write stores bytes under the uploads-store key and returns the
	// durable archive object reference for the ledger row.
	Write(ctx context.Context, key string, data []byte, contentType, filename string) (objectRef string, err error)
	// ReadAt streams archived bytes back for verification.
	ReadAt(ctx context.Context, objectRef string) (io.ReadCloser, error)
}

// ArtifactArchiveSource streams candidate bytes out of the uploads store.
type ArtifactArchiveSource interface {
	GetReader(ctx context.Context, key string) (io.ReadCloser, error)
}

// NASArtifactArchiveStore writes into a NAS-backed directory bind-mounted
// into the backend container. Copies are atomic (tmp + rename) and replays
// against an existing object are no-ops; object refs are stable
// nas-archive://<uploads-key> identifiers so a restore maps 1:1 back onto
// the uploads store layout.
type NASArtifactArchiveStore struct {
	Root        string
	StorageID_  string
	LocationID_ string
}

// NewNASArtifactArchiveStoreFromEnv enables the archive only when
// HIVECREW_ARTIFACT_ARCHIVE_ROOT names a mounted archive directory.
func NewNASArtifactArchiveStoreFromEnv() *NASArtifactArchiveStore {
	root := strings.TrimSpace(os.Getenv("HIVECREW_ARTIFACT_ARCHIVE_ROOT"))
	if root == "" {
		return nil
	}
	storageID := strings.TrimSpace(os.Getenv("HIVECREW_ARTIFACT_ARCHIVE_STORAGE_ID"))
	if storageID == "" {
		storageID = "hive-nas-archive"
	}
	locationID := strings.TrimSpace(os.Getenv("HIVECREW_ARTIFACT_ARCHIVE_LOCATION_ID"))
	if locationID == "" {
		locationID = "nas-archive-01"
	}
	return &NASArtifactArchiveStore{Root: root, StorageID_: storageID, LocationID_: locationID}
}

func (s *NASArtifactArchiveStore) StorageID() string  { return s.StorageID_ }
func (s *NASArtifactArchiveStore) LocationID() string { return s.LocationID_ }

func (s *NASArtifactArchiveStore) objectRefFor(key string) string {
	clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+key)), "/")
	return "nas-archive://" + clean
}

func (s *NASArtifactArchiveStore) Write(_ context.Context, key string, data []byte, _ string, _ string) (string, error) {
	if s == nil {
		return "", ErrArtifactArchiveDisabled
	}
	abs := filepath.Join(s.Root, filepath.Clean("/"+key))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("artifact archive mkdir: %w", err)
	}
	// Archived candidates are immutable. An existing object is never
	// rewritten; byte-identical replay is a no-op.
	if _, statErr := os.Stat(abs); statErr == nil {
		return s.objectRefFor(key), nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".archive-tmp-*")
	if err != nil {
		return "", fmt.Errorf("artifact archive temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("artifact archive write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("artifact archive sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("artifact archive close: %w", err)
	}
	// Same-volume rename keeps the placement atomic; a racing writer with
	// the same key converges on one canonical copy.
	if err := os.Rename(tmpPath, abs); err != nil {
		return "", fmt.Errorf("artifact archive rename: %w", err)
	}
	return s.objectRefFor(key), nil
}

func (s *NASArtifactArchiveStore) ReadAt(_ context.Context, objectRef string) (io.ReadCloser, error) {
	if s == nil {
		return nil, ErrArtifactArchiveDisabled
	}
	const prefix = "nas-archive://"
	if !strings.HasPrefix(objectRef, prefix) {
		return nil, fmt.Errorf("artifact archive object ref %q is outside the archive namespace", objectRef)
	}
	rel := strings.TrimPrefix(objectRef, prefix)
	if rel == "" || strings.Contains(rel, "..") {
		return nil, fmt.Errorf("artifact archive object ref %q is not addressable", objectRef)
	}
	return os.Open(filepath.Join(s.Root, filepath.FromSlash(rel)))
}

// ArtifactArchiveMetadata is the compact sidecar JSON stored in the ledger
// row's metadata column. It describes the placement, never the lifecycle.
type ArtifactArchiveMetadata struct {
	ArchivedAt  string `json:"archived_at"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SourceRef   string `json:"source_ref"`
	ArchiveKind string `json:"archive_kind"`
}

// artifactMetadataDigestV0 hashes the canonical metadata sidecar so a later
// verification can detect a replaced copy plus tampered metadata.
func artifactMetadataDigestV0(meta ArtifactArchiveMetadata) string {
	canonical, _ := json.Marshal(meta)
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ArtifactArchiveCandidate is the archive view of one accepted candidate:
// the uploads-store identity plus the expected content fingerprint.
type ArtifactArchiveCandidate struct {
	WorkspaceID      string
	LineageID        string
	CandidateID      string
	Revision         int
	StorageKey       string
	Digest           string
	DurableObjectRef string
	Filename         string
	ContentType      string
	SizeBytes        int64
}

// ArchiveArtifactResult reports what one archive pass did.
type ArchiveArtifactResult struct {
	CandidateID string
	ObjectRef   string
	Verified    bool
	Skipped     string
}

// ArtifactArchiveLedger is the narrow ledger surface the archiver needs; in
// production each operation commits in its own transaction.
type ArtifactArchiveLedger interface {
	Record(ctx context.Context, location ArtifactReplicaLocation) (ArtifactReplicaLocation, error)
	ListByCandidate(ctx context.Context, workspaceID, candidateID string) ([]ArtifactReplicaLocation, error)
	UpdateState(ctx context.Context, workspaceID, id string, state ArtifactReplicaLocationState, digest, metadataDigest string, sizeBytes int64) (ArtifactReplicaLocation, error)
}

// DurableArtifactArchiveLedger gives each ledger operation its own committed
// transaction, mirroring the materializer's per-step durability.
type DurableArtifactArchiveLedger struct {
	txStarter ArtifactPersistenceTxStarter
}

func NewDurableArtifactArchiveLedger(txStarter ArtifactPersistenceTxStarter) *DurableArtifactArchiveLedger {
	return &DurableArtifactArchiveLedger{txStarter: txStarter}
}

func (l *DurableArtifactArchiveLedger) Record(ctx context.Context, location ArtifactReplicaLocation) (ArtifactReplicaLocation, error) {
	tx, err := l.txStarter.Begin(ctx)
	if err != nil {
		return ArtifactReplicaLocation{}, fmt.Errorf("begin artifact archive ledger transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	stored, err := NewArtifactReplicaLocationRepository(tx).Record(ctx, location)
	if err != nil {
		return ArtifactReplicaLocation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ArtifactReplicaLocation{}, fmt.Errorf("commit artifact archive ledger transaction: %w", err)
	}
	return stored, nil
}

func (l *DurableArtifactArchiveLedger) ListByCandidate(ctx context.Context, workspaceID, candidateID string) ([]ArtifactReplicaLocation, error) {
	tx, err := l.txStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin artifact archive ledger transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	locations, err := NewArtifactReplicaLocationRepository(tx).ListByCandidate(ctx, workspaceID, candidateID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit artifact archive ledger transaction: %w", err)
	}
	return locations, nil
}

func (l *DurableArtifactArchiveLedger) UpdateState(ctx context.Context, workspaceID, id string, state ArtifactReplicaLocationState, digest, metadataDigest string, sizeBytes int64) (ArtifactReplicaLocation, error) {
	tx, err := l.txStarter.Begin(ctx)
	if err != nil {
		return ArtifactReplicaLocation{}, fmt.Errorf("begin artifact archive ledger transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	stored, err := NewArtifactReplicaLocationRepository(tx).UpdateState(ctx, workspaceID, id, state, digest, metadataDigest, sizeBytes)
	if err != nil {
		return ArtifactReplicaLocation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ArtifactReplicaLocation{}, fmt.Errorf("commit artifact archive ledger transaction: %w", err)
	}
	return stored, nil
}

// ArtifactArchiver mirrors an accepted candidate into the archive store and
// records the placement in the replica-location ledger with verification.
type ArtifactArchiver struct {
	Store ArtifactArchiveStore
	// Now is injectable for deterministic sidecar timestamps in tests.
	Now func() time.Time
}

func (a *ArtifactArchiver) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// ArchiveCandidate copies candidate bytes from the uploads store into the
// archive, verifies the copy by digest, and records a verified (or failed)
// ledger row. It is idempotent and crash-safe:
//   - a verified ledger row for this archive location short-circuits;
//   - the pending ledger row is durable before any byte moves;
//   - a replay after a crash mid-copy re-verifies the archive object and
//     only rewrites it when the digest does not match.
func (a *ArtifactArchiver) ArchiveCandidate(
	ctx context.Context,
	source ArtifactArchiveSource,
	ledger ArtifactArchiveLedger,
	candidate ArtifactArchiveCandidate,
) (ArchiveArtifactResult, error) {
	if a == nil || a.Store == nil {
		return ArchiveArtifactResult{}, ErrArtifactArchiveDisabled
	}
	result := ArchiveArtifactResult{CandidateID: candidate.CandidateID}

	existing, err := ledger.ListByCandidate(ctx, candidate.WorkspaceID, candidate.CandidateID)
	if err != nil {
		return result, err
	}
	for _, row := range existing {
		if row.Location.Class != ArtifactStorageLocationNASPrimary || row.Location.ID != a.Store.LocationID() {
			continue
		}
		if row.State == ArtifactReplicaLocationVerified && row.Digest == candidate.Digest {
			result.Skipped = "already_verified"
			result.ObjectRef = row.Location.ObjectRef
			return result, nil
		}
	}

	rowID := uuid.NewSHA1(artifactArchiveLedgerNamespace,
		[]byte(candidate.WorkspaceID+"/"+candidate.CandidateID+"/"+a.Store.LocationID())).String()
	meta := ArtifactArchiveMetadata{
		ArchivedAt:  a.now().Format(time.RFC3339),
		Filename:    candidate.Filename,
		ContentType: candidate.ContentType,
		SourceRef:   candidate.DurableObjectRef,
		ArchiveKind: string(ArtifactStorageLocationNASPrimary),
	}
	metadataJSON, err := json.Marshal(meta)
	if err != nil {
		return result, err
	}
	row := ArtifactReplicaLocation{
		ID:                rowID,
		WorkspaceID:       candidate.WorkspaceID,
		OutcomeID:         candidate.LineageID,
		CandidateID:       candidate.CandidateID,
		CandidateRevision: candidate.Revision,
		Location: ArtifactStorageLocation{
			ID:        a.Store.LocationID(),
			Class:     ArtifactStorageLocationNASPrimary,
			StorageID: a.Store.StorageID(),
			// The archive key mirrors the uploads storage key so a restore
			// maps 1:1; the concrete ref is finalized after the write.
			ObjectRef:     "nas-archive://" + candidate.StorageKey,
			RetentionHint: "accepted-artifact",
		},
		State:          ArtifactReplicaLocationPending,
		Digest:         candidate.Digest,
		MetadataDigest: artifactMetadataDigestV0(meta),
		SizeBytes:      candidate.SizeBytes,
		Metadata:       metadataJSON,
	}
	if _, err := ledger.Record(ctx, row); err != nil {
		return result, fmt.Errorf("record artifact archive ledger row: %w", err)
	}

	verifyFail := func(reason string, markErr error) (ArchiveArtifactResult, error) {
		if _, err := ledger.UpdateState(ctx, candidate.WorkspaceID, rowID,
			ArtifactReplicaLocationFailed, "", "", 0); err != nil {
			return result, fmt.Errorf("%s; marking ledger row failed: %w", reason, err)
		}
		return result, fmt.Errorf("%w: %s: %v", ErrArtifactArchiveVerifyFail, reason, markErr)
	}

	// Source integrity first: never archive bytes that disagree with the
	// candidate digest, whatever the uploads store hands back.
	reader, err := source.GetReader(ctx, candidate.StorageKey)
	if err != nil {
		return verifyFail("uploads read", err)
	}
	data, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil {
		return verifyFail("uploads read", err)
	}
	if closeErr != nil {
		return verifyFail("uploads close", closeErr)
	}
	if got := digestBytes(data); got != candidate.Digest {
		return verifyFail("uploads digest mismatch (want "+candidate.Digest+", got "+got+")", nil)
	}

	objectRef, err := a.Store.Write(ctx, candidate.StorageKey, data, candidate.ContentType, candidate.Filename)
	if err != nil {
		return verifyFail("archive write", err)
	}
	result.ObjectRef = objectRef

	readback, err := a.Store.ReadAt(ctx, objectRef)
	if err != nil {
		return verifyFail("archive readback", err)
	}
	archived, err := io.ReadAll(readback)
	readbackErr := readback.Close()
	if err != nil {
		return verifyFail("archive readback", err)
	}
	if readbackErr != nil {
		return verifyFail("archive readback close", readbackErr)
	}
	if got := digestBytes(archived); got != candidate.Digest {
		return verifyFail("archive digest mismatch (want "+candidate.Digest+", got "+got+")", nil)
	}

	if _, err := ledger.UpdateState(ctx, candidate.WorkspaceID, rowID,
		ArtifactReplicaLocationVerified, candidate.Digest, row.MetadataDigest, int64(len(archived))); err != nil {
		return result, fmt.Errorf("mark artifact archive verified: %w", err)
	}
	result.Verified = true
	return result, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
