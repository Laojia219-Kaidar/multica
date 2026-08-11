package companyops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestArtifactMaterializerRecordsIntentBeforeUploadAndUsesIndependentDigestKey(t *testing.T) {
	payload := []byte("reviewable artifact body")
	trace := []string{}
	source := &fakeArtifactMaterializationSource{
		snapshot: ArtifactSourceSnapshot{
			Bytes:       payload,
			Filename:    "review.md",
			ContentType: "text/markdown",
		},
	}
	store := newFakeArtifactObjectStore(&trace)
	repo := newFakeArtifactMaterializationRepository(&trace)
	materializer := NewArtifactMaterializer(source, store, repo)
	req := artifactMaterializationRequest()

	candidate, err := materializer.Materialize(context.Background(), req)
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}

	sum := sha256.Sum256(payload)
	hexDigest := hex.EncodeToString(sum[:])
	wantDigest := "sha256:" + hexDigest
	wantKey := "workspaces/workspace-1/artifact-candidates/candidate-1/" + hexDigest
	wantRef := store.ObjectURL(wantKey)
	if candidate.Digest != wantDigest {
		t.Fatalf("candidate digest = %q, want %q", candidate.Digest, wantDigest)
	}
	if candidate.DurableObjectRef != wantRef {
		t.Fatalf("candidate durable ref = %q, want %q", candidate.DurableObjectRef, wantRef)
	}
	if candidate.SourceAttachmentID != req.SourceAttachmentID || candidate.SourceCommentID != req.SourceCommentID {
		t.Fatalf("candidate provenance = (%q, %q), want (%q, %q)", candidate.SourceAttachmentID, candidate.SourceCommentID, req.SourceAttachmentID, req.SourceCommentID)
	}
	if store.lastUploadKey != wantKey {
		t.Fatalf("upload key = %q, want independent candidate key %q", store.lastUploadKey, wantKey)
	}
	if bytes.Equal([]byte(store.lastUploadKey), payload) || strings.Contains(store.lastUploadKey, req.SourceAttachmentID) {
		t.Fatalf("upload key %q reused source identity or content", store.lastUploadKey)
	}
	if got := store.objects[wantKey]; !bytes.Equal(got, payload) {
		t.Fatalf("stored bytes = %q, want %q", got, payload)
	}
	if !reflect.DeepEqual(trace, []string{"intent", "upload", "commit"}) {
		t.Fatalf("operation order = %v, want intent before upload then commit", trace)
	}
	if repo.lastIntent.StorageKey != wantKey || repo.lastIntent.DurableObjectRef != wantRef || repo.lastIntent.Digest != wantDigest {
		t.Fatalf("persisted intent does not bind key/ref/digest: %+v", repo.lastIntent)
	}
}

func TestArtifactMaterializerUploadFailureLeavesNoCandidateAndCleanupIntent(t *testing.T) {
	trace := []string{}
	uploadErr := errors.New("injected upload failure")
	source := &fakeArtifactMaterializationSource{snapshot: artifactSourceSnapshot()}
	store := newFakeArtifactObjectStore(&trace)
	store.uploadErr = uploadErr
	repo := newFakeArtifactMaterializationRepository(&trace)
	materializer := NewArtifactMaterializer(source, store, repo)

	_, err := materializer.Materialize(context.Background(), artifactMaterializationRequest())
	if !errors.Is(err, uploadErr) {
		t.Fatalf("Materialize() error = %v, want %v", err, uploadErr)
	}
	if repo.commitCalls != 0 || len(repo.candidatesByIdempotencyKey) != 0 {
		t.Fatalf("upload failure committed a candidate: commits=%d candidates=%v", repo.commitCalls, repo.candidatesByIdempotencyKey)
	}
	if len(repo.cleanupPending) != 1 {
		t.Fatalf("cleanup-pending intents = %d, want 1", len(repo.cleanupPending))
	}
	if repo.cleanupPending[0].StorageKey == "" {
		t.Fatal("cleanup-pending intent lost the independently addressable object key")
	}
	if !reflect.DeepEqual(trace, []string{"intent", "upload", "cleanup_pending"}) {
		t.Fatalf("operation order = %v, want durable intent, failed upload, cleanup-pending", trace)
	}
}

func TestArtifactMaterializerCommitFailureMarksCleanupPendingBeforeDelete(t *testing.T) {
	trace := []string{}
	commitErr := errors.New("injected candidate commit failure")
	source := &fakeArtifactMaterializationSource{snapshot: artifactSourceSnapshot()}
	store := newFakeArtifactObjectStore(&trace)
	repo := newFakeArtifactMaterializationRepository(&trace)
	repo.commitErr = commitErr
	materializer := NewArtifactMaterializer(source, store, repo)

	_, err := materializer.Materialize(context.Background(), artifactMaterializationRequest())
	if !errors.Is(err, commitErr) {
		t.Fatalf("Materialize() error = %v, want %v", err, commitErr)
	}
	if len(repo.cleanupPending) != 1 {
		t.Fatalf("cleanup-pending intents = %d, want 1", len(repo.cleanupPending))
	}
	if store.deleteCalls != 1 || store.lastDeletedKey != repo.cleanupPending[0].StorageKey {
		t.Fatalf("DeleteObject calls/key = %d/%q, want one attempt for %q", store.deleteCalls, store.lastDeletedKey, repo.cleanupPending[0].StorageKey)
	}
	if len(repo.candidatesByIdempotencyKey) != 0 {
		t.Fatalf("commit failure exposed a candidate: %v", repo.candidatesByIdempotencyKey)
	}
	if !reflect.DeepEqual(trace, []string{"intent", "upload", "commit", "cleanup_pending", "delete"}) {
		t.Fatalf("operation order = %v, want cleanup-pending durable before compensating delete", trace)
	}
}

func TestArtifactMaterializerIdempotentReplayReturnsSameCandidate(t *testing.T) {
	trace := []string{}
	source := &fakeArtifactMaterializationSource{snapshot: artifactSourceSnapshot()}
	store := newFakeArtifactObjectStore(&trace)
	repo := newFakeArtifactMaterializationRepository(&trace)
	materializer := NewArtifactMaterializer(source, store, repo)
	req := artifactMaterializationRequest()

	first, err := materializer.Materialize(context.Background(), req)
	if err != nil {
		t.Fatalf("first Materialize() error = %v", err)
	}
	source.deleted = true
	second, err := materializer.Materialize(context.Background(), req)
	if err != nil {
		t.Fatalf("idempotent Materialize() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("idempotent replay returned different candidate: first=%+v second=%+v", first, second)
	}
	if source.readCalls != 1 || store.uploadCalls != 1 || repo.intentCalls != 1 || repo.commitCalls != 1 {
		t.Fatalf("replay repeated side effects: source=%d upload=%d intent=%d commit=%d", source.readCalls, store.uploadCalls, repo.intentCalls, repo.commitCalls)
	}
}

func TestArtifactMaterializerSourceDeletionDoesNotAffectCandidateSnapshot(t *testing.T) {
	trace := []string{}
	payload := []byte("immutable candidate snapshot")
	source := &fakeArtifactMaterializationSource{
		snapshot: ArtifactSourceSnapshot{
			Bytes:       append([]byte(nil), payload...),
			Filename:    "snapshot.txt",
			ContentType: "text/plain",
		},
	}
	store := newFakeArtifactObjectStore(&trace)
	repo := newFakeArtifactMaterializationRepository(&trace)
	materializer := NewArtifactMaterializer(source, store, repo)

	candidate, err := materializer.Materialize(context.Background(), artifactMaterializationRequest())
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	wantCandidate := candidate
	storedKey := store.KeyFromURL(candidate.DurableObjectRef)

	for i := range source.snapshot.Bytes {
		source.snapshot.Bytes[i] = 'x'
	}
	source.snapshot.Bytes = nil
	source.deleted = true

	if !reflect.DeepEqual(candidate, wantCandidate) {
		t.Fatalf("source deletion mutated candidate: before=%+v after=%+v", wantCandidate, candidate)
	}
	if got := store.objects[storedKey]; !bytes.Equal(got, payload) {
		t.Fatalf("source deletion changed durable bytes: got=%q want=%q", got, payload)
	}
	sum := sha256.Sum256(payload)
	if candidate.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("candidate digest changed after source deletion: %q", candidate.Digest)
	}
}

func TestArtifactMaterializerIntentKeyReferenceAndDigestMismatchFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ArtifactMaterializationIntent)
		wantErr error
	}{
		{
			name: "storage key mismatch",
			mutate: func(intent *ArtifactMaterializationIntent) {
				intent.StorageKey += "-other"
			},
			wantErr: ErrArtifactStorageKeyMismatch,
		},
		{
			name: "durable object reference mismatch",
			mutate: func(intent *ArtifactMaterializationIntent) {
				intent.DurableObjectRef += "-other"
			},
			wantErr: ErrArtifactObjectRefMismatch,
		},
		{
			name: "digest mismatch",
			mutate: func(intent *ArtifactMaterializationIntent) {
				intent.Digest = "sha256:wrong"
			},
			wantErr: ErrArtifactDigestMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trace := []string{}
			source := &fakeArtifactMaterializationSource{snapshot: artifactSourceSnapshot()}
			store := newFakeArtifactObjectStore(&trace)
			repo := newFakeArtifactMaterializationRepository(&trace)
			repo.intentReadbackMutation = tt.mutate
			materializer := NewArtifactMaterializer(source, store, repo)

			_, err := materializer.Materialize(context.Background(), artifactMaterializationRequest())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Materialize() error = %v, want %v", err, tt.wantErr)
			}
			if store.uploadCalls != 0 || repo.commitCalls != 0 || len(repo.candidatesByIdempotencyKey) != 0 {
				t.Fatalf("mismatch escaped fail-closed gate: uploads=%d commits=%d candidates=%v", store.uploadCalls, repo.commitCalls, repo.candidatesByIdempotencyKey)
			}
			if !reflect.DeepEqual(trace, []string{"intent"}) {
				t.Fatalf("mismatch operation order = %v, want intent readback only", trace)
			}
		})
	}
}

func artifactMaterializationRequest() ArtifactMaterializationRequest {
	return ArtifactMaterializationRequest{
		WorkspaceID:        "workspace-1",
		CandidateID:        "candidate-1",
		LineageID:          "lineage-1",
		Revision:           1,
		SourceAttachmentID: "attachment-1",
		SourceCommentID:    "comment-1",
		IdempotencyKey:     "materialize-1",
	}
}

func artifactSourceSnapshot() ArtifactSourceSnapshot {
	return ArtifactSourceSnapshot{
		Bytes:       []byte("artifact payload"),
		Filename:    "artifact.md",
		ContentType: "text/markdown",
	}
}

type fakeArtifactMaterializationSource struct {
	snapshot  ArtifactSourceSnapshot
	err       error
	deleted   bool
	readCalls int
}

func (s *fakeArtifactMaterializationSource) ReadArtifactSource(context.Context, ArtifactMaterializationRequest) (ArtifactSourceSnapshot, error) {
	s.readCalls++
	if s.deleted {
		return ArtifactSourceSnapshot{}, errors.New("source deleted")
	}
	if s.err != nil {
		return ArtifactSourceSnapshot{}, s.err
	}
	snapshot := s.snapshot
	snapshot.Bytes = append([]byte(nil), s.snapshot.Bytes...)
	return snapshot, nil
}

type fakeArtifactObjectStore struct {
	trace          *[]string
	objects        map[string][]byte
	uploadErr      error
	deleteErr      error
	uploadCalls    int
	deleteCalls    int
	lastUploadKey  string
	lastDeletedKey string
}

func newFakeArtifactObjectStore(trace *[]string) *fakeArtifactObjectStore {
	return &fakeArtifactObjectStore{trace: trace, objects: make(map[string][]byte)}
}

func (s *fakeArtifactObjectStore) Upload(_ context.Context, key string, data []byte, _ string, _ string) (string, error) {
	*s.trace = append(*s.trace, "upload")
	s.uploadCalls++
	s.lastUploadKey = key
	if s.uploadErr != nil {
		return "", s.uploadErr
	}
	s.objects[key] = append([]byte(nil), data...)
	return s.ObjectURL(key), nil
}

func (s *fakeArtifactObjectStore) DeleteObject(_ context.Context, key string) error {
	*s.trace = append(*s.trace, "delete")
	s.deleteCalls++
	s.lastDeletedKey = key
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.objects, key)
	return nil
}

func (*fakeArtifactObjectStore) ObjectURL(key string) string {
	return "object://hivecrew/" + key
}

func (*fakeArtifactObjectStore) KeyFromURL(ref string) string {
	return strings.TrimPrefix(ref, "object://hivecrew/")
}

type fakeArtifactMaterializationRepository struct {
	trace                      *[]string
	candidatesByIdempotencyKey map[string]ArtifactCandidate
	lastIntent                 ArtifactMaterializationIntent
	cleanupPending             []ArtifactMaterializationIntent
	intentReadbackMutation     func(*ArtifactMaterializationIntent)
	lookupErr                  error
	intentErr                  error
	commitErr                  error
	cleanupErr                 error
	lookupCalls                int
	intentCalls                int
	commitCalls                int
	cleanupCalls               int
}

func newFakeArtifactMaterializationRepository(trace *[]string) *fakeArtifactMaterializationRepository {
	return &fakeArtifactMaterializationRepository{
		trace:                      trace,
		candidatesByIdempotencyKey: make(map[string]ArtifactCandidate),
	}
}

func (r *fakeArtifactMaterializationRepository) FindArtifactCandidateByIdempotencyKey(_ context.Context, _ string, key string) (ArtifactCandidate, bool, error) {
	r.lookupCalls++
	if r.lookupErr != nil {
		return ArtifactCandidate{}, false, r.lookupErr
	}
	candidate, ok := r.candidatesByIdempotencyKey[key]
	return candidate, ok, nil
}

func (r *fakeArtifactMaterializationRepository) RecordArtifactMaterializationIntent(_ context.Context, intent ArtifactMaterializationIntent) (ArtifactMaterializationIntent, error) {
	*r.trace = append(*r.trace, "intent")
	r.intentCalls++
	if r.intentErr != nil {
		return ArtifactMaterializationIntent{}, r.intentErr
	}
	r.lastIntent = intent
	readback := intent
	if r.intentReadbackMutation != nil {
		r.intentReadbackMutation(&readback)
	}
	return readback, nil
}

func (r *fakeArtifactMaterializationRepository) CommitArtifactCandidate(_ context.Context, intent ArtifactMaterializationIntent, candidate ArtifactCandidate) (ArtifactCandidate, error) {
	*r.trace = append(*r.trace, "commit")
	r.commitCalls++
	if r.commitErr != nil {
		return ArtifactCandidate{}, r.commitErr
	}
	r.candidatesByIdempotencyKey[intent.IdempotencyKey] = candidate
	return candidate, nil
}

func (r *fakeArtifactMaterializationRepository) MarkArtifactMaterializationCleanupPending(_ context.Context, intent ArtifactMaterializationIntent, _ error) error {
	*r.trace = append(*r.trace, "cleanup_pending")
	r.cleanupCalls++
	r.cleanupPending = append(r.cleanupPending, intent)
	return r.cleanupErr
}
