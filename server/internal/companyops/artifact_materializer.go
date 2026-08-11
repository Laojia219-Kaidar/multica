package companyops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
)

var (
	ErrInvalidArtifactMaterialization = errors.New("invalid artifact materialization")
	ErrArtifactStorageKeyMismatch     = errors.New("artifact storage key mismatch")
)

type ArtifactMaterializationRequest struct {
	WorkspaceID        string
	CandidateID        string
	LineageID          string
	Revision           int
	SupersedesID       string
	SourceAttachmentID string
	SourceCommentID    string
	IdempotencyKey     string
}

type ArtifactSourceSnapshot struct {
	Bytes       []byte
	Filename    string
	ContentType string
}

// ArtifactMaterializationIntent is the durable object-write intent recorded
// before Upload. It is operational recovery state, not a second artifact value.
type ArtifactMaterializationIntent struct {
	WorkspaceID        string
	CandidateID        string
	LineageID          string
	StorageKey         string
	DurableObjectRef   string
	Digest             string
	Filename           string
	ContentType        string
	SizeBytes          int64
	SourceAttachmentID string
	SourceCommentID    string
	IdempotencyKey     string
}

type ArtifactMaterializationSource interface {
	ReadArtifactSource(ctx context.Context, request ArtifactMaterializationRequest) (ArtifactSourceSnapshot, error)
}

type ArtifactMaterializationObjectStore interface {
	Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error)
	DeleteObject(ctx context.Context, key string) error
	ObjectURL(key string) string
	KeyFromURL(ref string) string
}

type ArtifactMaterializationRepository interface {
	FindArtifactCandidateByIdempotencyKey(ctx context.Context, workspaceID string, idempotencyKey string) (ArtifactCandidate, bool, error)
	RecordArtifactMaterializationIntent(ctx context.Context, intent ArtifactMaterializationIntent) (ArtifactMaterializationIntent, error)
	CommitArtifactCandidate(ctx context.Context, intent ArtifactMaterializationIntent, candidate ArtifactCandidate) (ArtifactCandidate, error)
	MarkArtifactMaterializationCleanupPending(ctx context.Context, intent ArtifactMaterializationIntent, cause error) error
}

type ArtifactMaterializer struct {
	source ArtifactMaterializationSource
	store  ArtifactMaterializationObjectStore
	repo   ArtifactMaterializationRepository
}

func NewArtifactMaterializer(source ArtifactMaterializationSource, store ArtifactMaterializationObjectStore, repo ArtifactMaterializationRepository) *ArtifactMaterializer {
	return &ArtifactMaterializer{source: source, store: store, repo: repo}
}

func (m *ArtifactMaterializer) Materialize(ctx context.Context, request ArtifactMaterializationRequest) (ArtifactCandidate, error) {
	if err := validateArtifactMaterializationRequest(request); err != nil {
		return ArtifactCandidate{}, err
	}
	if m == nil || m.source == nil || m.store == nil || m.repo == nil {
		return ArtifactCandidate{}, fmt.Errorf("%w: source, store, and repository are required", ErrInvalidArtifactMaterialization)
	}

	existing, found, err := m.repo.FindArtifactCandidateByIdempotencyKey(ctx, request.WorkspaceID, request.IdempotencyKey)
	if err != nil {
		return ArtifactCandidate{}, fmt.Errorf("find artifact candidate replay: %w", err)
	}
	if found {
		if !artifactCandidateMatchesMaterializationRequest(existing, request) {
			return ArtifactCandidate{}, ErrArtifactIdempotencyConflict
		}
		return existing, nil
	}

	source, err := m.source.ReadArtifactSource(ctx, request)
	if err != nil {
		return ArtifactCandidate{}, fmt.Errorf("read artifact source: %w", err)
	}
	// Own the bytes before any durable write. A source implementation may reuse
	// or clear its buffer as soon as this call returns.
	data := append([]byte(nil), source.Bytes...)
	sum := sha256.Sum256(data)
	hexDigest := hex.EncodeToString(sum[:])
	digest := "sha256:" + hexDigest
	storageKey := path.Join("workspaces", request.WorkspaceID, "artifact-candidates", request.CandidateID, hexDigest)
	durableObjectRef := m.store.ObjectURL(storageKey)

	intent := ArtifactMaterializationIntent{
		WorkspaceID:        request.WorkspaceID,
		CandidateID:        request.CandidateID,
		LineageID:          request.LineageID,
		StorageKey:         storageKey,
		DurableObjectRef:   durableObjectRef,
		Digest:             digest,
		Filename:           source.Filename,
		ContentType:        source.ContentType,
		SizeBytes:          int64(len(data)),
		SourceAttachmentID: request.SourceAttachmentID,
		SourceCommentID:    request.SourceCommentID,
		IdempotencyKey:     request.IdempotencyKey,
	}
	persistedIntent, err := m.repo.RecordArtifactMaterializationIntent(ctx, intent)
	if err != nil {
		return ArtifactCandidate{}, fmt.Errorf("record artifact materialization intent: %w", err)
	}
	if err := validateArtifactMaterializationIntentReadback(intent, persistedIntent); err != nil {
		return ArtifactCandidate{}, err
	}

	uploadedRef, err := m.store.Upload(ctx, storageKey, data, source.ContentType, source.Filename)
	if err != nil {
		cleanupErr := m.repo.MarkArtifactMaterializationCleanupPending(ctx, persistedIntent, err)
		return ArtifactCandidate{}, errors.Join(fmt.Errorf("upload artifact candidate: %w", err), cleanupErr)
	}
	if uploadedRef != durableObjectRef || m.store.KeyFromURL(uploadedRef) != storageKey {
		mismatch := fmt.Errorf("%w: uploaded reference does not resolve to recorded key", ErrArtifactObjectRefMismatch)
		cleanupErr := m.repo.MarkArtifactMaterializationCleanupPending(ctx, persistedIntent, mismatch)
		deleteErr := m.store.DeleteObject(ctx, storageKey)
		return ArtifactCandidate{}, errors.Join(mismatch, cleanupErr, deleteErr)
	}

	candidate := ArtifactCandidate{
		ID:                 request.CandidateID,
		LineageID:          request.LineageID,
		Revision:           request.Revision,
		SupersedesID:       request.SupersedesID,
		DurableObjectRef:   durableObjectRef,
		Digest:             digest,
		SourceAttachmentID: request.SourceAttachmentID,
		SourceCommentID:    request.SourceCommentID,
	}
	err = validateArtifactCandidate(candidate)
	if err != nil {
		cleanupErr := m.repo.MarkArtifactMaterializationCleanupPending(ctx, persistedIntent, err)
		deleteErr := m.store.DeleteObject(ctx, storageKey)
		return ArtifactCandidate{}, errors.Join(err, cleanupErr, deleteErr)
	}

	committed, err := m.repo.CommitArtifactCandidate(ctx, persistedIntent, candidate)
	if err != nil {
		// Keep recovery intent durable before attempting the compensating delete.
		// A failed delete is intentionally returned while the repository intent
		// remains available for a later reconciler retry.
		cleanupErr := m.repo.MarkArtifactMaterializationCleanupPending(ctx, persistedIntent, err)
		deleteErr := m.store.DeleteObject(ctx, storageKey)
		return ArtifactCandidate{}, errors.Join(fmt.Errorf("commit artifact candidate: %w", err), cleanupErr, deleteErr)
	}
	return committed, nil
}

func validateArtifactMaterializationRequest(request ArtifactMaterializationRequest) error {
	requiredSegments := []struct {
		name  string
		value string
	}{
		{name: "workspace id", value: request.WorkspaceID},
		{name: "candidate id", value: request.CandidateID},
		{name: "lineage id", value: request.LineageID},
	}
	for _, field := range requiredSegments {
		if field.value == "" || field.value == "." || field.value == ".." || strings.ContainsAny(field.value, `/\\`) {
			return fmt.Errorf("%w: %s must be a non-empty path segment", ErrInvalidArtifactMaterialization, field.name)
		}
	}
	if request.IdempotencyKey == "" {
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidArtifactMaterialization)
	}
	if request.Revision < 1 {
		return fmt.Errorf("%w: revision must be positive", ErrInvalidArtifactMaterialization)
	}
	if request.Revision == 1 && request.SupersedesID != "" {
		return fmt.Errorf("%w: initial revision cannot supersede another candidate", ErrInvalidArtifactMaterialization)
	}
	if request.Revision > 1 && request.SupersedesID == "" {
		return fmt.Errorf("%w: later revision requires its predecessor", ErrInvalidArtifactMaterialization)
	}
	return nil
}

func validateArtifactMaterializationIntentReadback(want ArtifactMaterializationIntent, got ArtifactMaterializationIntent) error {
	if got.StorageKey != want.StorageKey {
		return fmt.Errorf("%w: recorded %q, want %q", ErrArtifactStorageKeyMismatch, got.StorageKey, want.StorageKey)
	}
	if got.DurableObjectRef != want.DurableObjectRef {
		return fmt.Errorf("%w: recorded %q, want %q", ErrArtifactObjectRefMismatch, got.DurableObjectRef, want.DurableObjectRef)
	}
	if got.Digest != want.Digest {
		return fmt.Errorf("%w: recorded %q, want %q", ErrArtifactDigestMismatch, got.Digest, want.Digest)
	}
	return nil
}

func artifactCandidateMatchesMaterializationRequest(candidate ArtifactCandidate, request ArtifactMaterializationRequest) bool {
	return candidate.ID == request.CandidateID &&
		candidate.LineageID == request.LineageID &&
		candidate.Revision == request.Revision &&
		candidate.SupersedesID == request.SupersedesID &&
		candidate.SourceAttachmentID == request.SourceAttachmentID &&
		candidate.SourceCommentID == request.SourceCommentID
}
