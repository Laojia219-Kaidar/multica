package companyops

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type archiveTestSource struct {
	objects map[string][]byte
	err     error
}

func (s archiveTestSource) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	if s.err != nil {
		return nil, s.err
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type archiveTestLedger struct {
	rows []ArtifactReplicaLocation
}

func (l *archiveTestLedger) Record(_ context.Context, location ArtifactReplicaLocation) (ArtifactReplicaLocation, error) {
	for _, row := range l.rows {
		if row.ID == location.ID || (row.OutcomeID == location.OutcomeID && row.CandidateID == location.CandidateID &&
			row.Location.Class == location.Location.Class && row.Location.ID == location.Location.ID) {
			if !artifactReplicaLocationRegistrationMatches(row, location) {
				return ArtifactReplicaLocation{}, ErrArtifactReplicaLocationConflict
			}
			return row, nil
		}
	}
	l.rows = append(l.rows, location)
	return location, nil
}

func (l *archiveTestLedger) ListByCandidate(_ context.Context, _, _ string) ([]ArtifactReplicaLocation, error) {
	return append([]ArtifactReplicaLocation(nil), l.rows...), nil
}

func (l *archiveTestLedger) UpdateState(_ context.Context, _ string, id string, state ArtifactReplicaLocationState, digest, metadataDigest string, sizeBytes int64) (ArtifactReplicaLocation, error) {
	for i := range l.rows {
		if l.rows[i].ID == id {
			l.rows[i].State = state
			l.rows[i].Digest = digest
			l.rows[i].MetadataDigest = metadataDigest
			l.rows[i].SizeBytes = sizeBytes
			return l.rows[i], nil
		}
	}
	return ArtifactReplicaLocation{}, ErrArtifactReplicaLocationNotFound
}

func archiveTestCandidate(t *testing.T, data []byte) (ArtifactArchiveCandidate, string) {
	t.Helper()
	key := "workspaces/ws-1/artifact-candidates/cand-1/" + strings.TrimPrefix(digestBytes(data), "sha256:")
	return ArtifactArchiveCandidate{
		WorkspaceID: "11111111-1111-1111-1111-111111111111",
		LineageID:   "22222222-2222-2222-2222-222222222222",
		CandidateID: "33333333-3333-3333-3333-333333333333",
		Revision:    1,
		StorageKey:  key,
		Digest:      digestBytes(data),
		Filename:    "outcome.md",
		ContentType: "text/markdown",
		SizeBytes:   int64(len(data)),
	}, key
}

func TestArchiveCandidateWritesVerifiesAndRecordsLedger(t *testing.T) {
	data := []byte("# accepted outcome\n\nbytes under test\n")
	candidate, key := archiveTestCandidate(t, data)
	source := archiveTestSource{objects: map[string][]byte{key: data}}
	store := &NASArtifactArchiveStore{Root: t.TempDir(), StorageID_: "nas-test", LocationID_: "nas-archive-01"}
	ledger := &archiveTestLedger{}
	archiver := &ArtifactArchiver{Store: store, Now: func() time.Time { return time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC) }}

	result, err := archiver.ArchiveCandidate(context.Background(), source, ledger, candidate)
	if err != nil {
		t.Fatalf("ArchiveCandidate: %v", err)
	}
	if !result.Verified {
		t.Fatalf("result.Verified = false, want true")
	}
	wantRef := "nas-archive://" + key
	if result.ObjectRef != wantRef {
		t.Errorf("ObjectRef = %q, want %q", result.ObjectRef, wantRef)
	}
	// Archive bytes on disk are exactly the source bytes.
	onDisk, err := os.ReadFile(filepath.Join(store.Root, filepath.FromSlash(key)))
	if err != nil {
		t.Fatalf("read archived object: %v", err)
	}
	if !bytes.Equal(onDisk, data) {
		t.Errorf("archived bytes differ from source (%d vs %d bytes)", len(onDisk), len(data))
	}
	if len(ledger.rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(ledger.rows))
	}
	row := ledger.rows[0]
	if row.State != ArtifactReplicaLocationVerified {
		t.Errorf("ledger state = %q, want verified", row.State)
	}
	if row.Location.Class != ArtifactStorageLocationNASPrimary || row.Location.ID != "nas-archive-01" || row.Location.StorageID != "nas-test" {
		t.Errorf("ledger location = %+v, want nas-primary/nas-archive-01/nas-test", row.Location)
	}
	if row.Digest != candidate.Digest {
		t.Errorf("ledger digest = %q, want %q", row.Digest, candidate.Digest)
	}
	if row.SizeBytes != int64(len(data)) {
		t.Errorf("ledger size = %d, want %d", row.SizeBytes, len(data))
	}
	if !strings.Contains(string(row.Metadata), `"archive_kind":"nas-primary"`) {
		t.Errorf("ledger metadata = %s, want archive_kind nas-primary", row.Metadata)
	}
}

func TestArchiveCandidateIsIdempotent(t *testing.T) {
	data := []byte("accepted twice")
	candidate, key := archiveTestCandidate(t, data)
	source := archiveTestSource{objects: map[string][]byte{key: data}}
	store := &NASArtifactArchiveStore{Root: t.TempDir(), StorageID_: "nas-test", LocationID_: "nas-archive-01"}
	ledger := &archiveTestLedger{}
	archiver := &ArtifactArchiver{Store: store}

	if _, err := archiver.ArchiveCandidate(context.Background(), source, ledger, candidate); err != nil {
		t.Fatalf("first ArchiveCandidate: %v", err)
	}
	second, err := archiver.ArchiveCandidate(context.Background(), source, ledger, candidate)
	if err != nil {
		t.Fatalf("second ArchiveCandidate: %v", err)
	}
	if second.Skipped != "already_verified" {
		t.Errorf("second pass Skipped = %q, want already_verified", second.Skipped)
	}
	// The ledger row id is deterministic per (workspace, candidate, location),
	// so a replay can never mint a second row.
	if len(ledger.rows) != 1 {
		t.Errorf("ledger rows = %d, want 1 after replay", len(ledger.rows))
	}
}

func TestArchiveCandidateRejectsSourceDigestMismatch(t *testing.T) {
	candidate, key := archiveTestCandidate(t, []byte("original"))
	source := archiveTestSource{objects: map[string][]byte{key: []byte("tampered")}}
	store := &NASArtifactArchiveStore{Root: t.TempDir(), StorageID_: "nas-test", LocationID_: "nas-archive-01"}
	ledger := &archiveTestLedger{}
	archiver := &ArtifactArchiver{Store: store}

	_, err := archiver.ArchiveCandidate(context.Background(), source, ledger, candidate)
	if !errors.Is(err, ErrArtifactArchiveVerifyFail) {
		t.Fatalf("error = %v, want ErrArtifactArchiveVerifyFail", err)
	}
	// Nothing was archived and the ledger row is failed with cleared digest.
	if _, statErr := os.Stat(filepath.Join(store.Root, filepath.FromSlash(key))); statErr == nil {
		t.Errorf("tampered bytes were archived; want no archive object")
	}
	if len(ledger.rows) != 1 || ledger.rows[0].State != ArtifactReplicaLocationFailed {
		t.Fatalf("ledger rows = %+v, want one failed row", ledger.rows)
	}
	if ledger.rows[0].Digest != "" {
		t.Errorf("failed row digest = %q, want cleared", ledger.rows[0].Digest)
	}
}

func TestNASArtifactArchiveStoreWriteIsAtomicAndReplaySafe(t *testing.T) {
	store := &NASArtifactArchiveStore{Root: t.TempDir(), StorageID_: "nas-test", LocationID_: "nas-archive-01"}
	ctx := context.Background()

	ref, err := store.Write(ctx, "workspaces/ws/a/b.md", []byte("first"), "text/markdown", "b.md")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if ref != "nas-archive://workspaces/ws/a/b.md" {
		t.Errorf("ref = %q, want nas-archive://workspaces/ws/a/b.md", ref)
	}
	// Second write of the same key with different bytes is a no-op: archived
	// candidates are immutable.
	ref2, err := store.Write(ctx, "workspaces/ws/a/b.md", []byte("second"), "text/markdown", "b.md")
	if err != nil {
		t.Fatalf("replay Write: %v", err)
	}
	if ref2 != ref {
		t.Errorf("replay ref = %q, want %q", ref2, ref)
	}
	onDisk, _ := os.ReadFile(filepath.Join(store.Root, "workspaces", "ws", "a", "b.md"))
	if string(onDisk) != "first" {
		t.Errorf("replay rewrote bytes to %q, want first", onDisk)
	}
	// No temp-file residue.
	entries, _ := os.ReadDir(filepath.Join(store.Root, "workspaces", "ws", "a"))
	if len(entries) != 1 {
		t.Errorf("archive dir entries = %d, want 1 (no tmp residue)", len(entries))
	}
	// ReadAt refuses refs outside the archive namespace.
	if _, err := store.ReadAt(ctx, "file:///etc/passwd"); err == nil {
		t.Errorf("ReadAt accepted non-archive ref")
	}
	if _, err := store.ReadAt(ctx, "nas-archive://../escape"); err == nil {
		t.Errorf("ReadAt accepted traversal ref")
	}
}
