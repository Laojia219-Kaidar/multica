package companyops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"
)

var (
	ErrInvalidArtifactReplica      = errors.New("invalid artifact replica")
	ErrArtifactReplicaCopyFailed   = errors.New("artifact replica copy failed")
	ErrArtifactReplicaVerifyFailed = errors.New("artifact replica verification failed")
	ErrArtifactUploadSuspended     = errors.New("artifact upload suspended")
	ErrInvalidArtifactCapacity     = errors.New("invalid artifact capacity estimate")
)

// ArtifactStorageLocationClass identifies a physical placement without making
// that placement a second artifact authority.
type ArtifactStorageLocationClass string

const (
	ArtifactStorageLocationLocalCache   ArtifactStorageLocationClass = "local-cache"
	ArtifactStorageLocationNASPrimary   ArtifactStorageLocationClass = "nas-primary"
	ArtifactStorageLocationOfflineCopy  ArtifactStorageLocationClass = "offline-copy"
	ArtifactStorageLocationCloudReplica ArtifactStorageLocationClass = "cloud-replica"
)

// ArtifactReplicaState is deliberately independent from ArtifactLifecycle.
// A verified replica does not mean that the artifact has been accepted.
type ArtifactReplicaState string

const (
	ArtifactReplicaPending   ArtifactReplicaState = "pending"
	ArtifactReplicaVerified  ArtifactReplicaState = "verified"
	ArtifactReplicaFailed    ArtifactReplicaState = "failed"
	ArtifactReplicaSuspended ArtifactReplicaState = "suspended"
)

type ArtifactStorageLocation struct {
	ID            string
	Class         ArtifactStorageLocationClass
	StorageID     string
	ObjectRef     string
	RetentionHint string
}

type ArtifactReplica struct {
	ArtifactID     string
	Version        int
	Location       ArtifactStorageLocation
	State          ArtifactReplicaState
	Digest         string
	MetadataDigest string
	SizeBytes      int64
}

type ArtifactDurabilityAssessment struct {
	Durable                bool
	LocalPrimaryVerified   bool
	OfflineOrCloudVerified bool
	MetadataAndHashMatch   bool
	Reason                 string
}

// AssessArtifactDurability is a pure decision function. It requires a
// verified NAS primary and at least one verified offline or cloud replica,
// with both content and metadata fingerprints matching the expected values.
func AssessArtifactDurability(replicas []ArtifactReplica, expectedDigest, expectedMetadataDigest string) ArtifactDurabilityAssessment {
	assessment := ArtifactDurabilityAssessment{}
	if expectedDigest == "" {
		assessment.Reason = "expected digest is required"
		return assessment
	}
	if expectedMetadataDigest == "" {
		assessment.Reason = "expected metadata digest is required"
		return assessment
	}

	for _, replica := range replicas {
		if replica.State != ArtifactReplicaVerified {
			continue
		}
		if replica.Digest != expectedDigest || replica.MetadataDigest != expectedMetadataDigest {
			continue
		}
		switch replica.Location.Class {
		case ArtifactStorageLocationNASPrimary:
			assessment.LocalPrimaryVerified = true
		case ArtifactStorageLocationOfflineCopy, ArtifactStorageLocationCloudReplica:
			assessment.OfflineOrCloudVerified = true
		}
	}
	assessment.MetadataAndHashMatch = assessment.LocalPrimaryVerified && assessment.OfflineOrCloudVerified
	assessment.Durable = assessment.MetadataAndHashMatch
	if assessment.Durable {
		assessment.Reason = "verified NAS primary and verified offline or cloud replica match metadata and digest"
		return assessment
	}
	assessment.Reason = "requires matching verified NAS primary and offline or cloud replica"
	return assessment
}

type ArtifactCapacityEstimate struct {
	AvailableBytes    int64
	InputBytes        int64
	IntermediateBytes int64
	RenderBytes       int64
	ReplicaBytes      int64
	HeadroomBytes     int64
}

type ArtifactCapacityStatus string

const (
	ArtifactCapacityReady   ArtifactCapacityStatus = "READY"
	ArtifactCapacityBlocked ArtifactCapacityStatus = "CAPACITY_BLOCKED"
)

type ArtifactCapacityDecision struct {
	Status         ArtifactCapacityStatus
	RequiredBytes  int64
	AvailableBytes int64
	ShortfallBytes int64
}

// EvaluateArtifactCapacity never deletes content or mutates storage. It is a
// gate that callers can use before allocating a media pipeline.
func EvaluateArtifactCapacity(estimate ArtifactCapacityEstimate) (ArtifactCapacityDecision, error) {
	values := []int64{
		estimate.AvailableBytes,
		estimate.InputBytes,
		estimate.IntermediateBytes,
		estimate.RenderBytes,
		estimate.ReplicaBytes,
		estimate.HeadroomBytes,
	}
	for _, value := range values {
		if value < 0 {
			return ArtifactCapacityDecision{}, fmt.Errorf("%w: values cannot be negative", ErrInvalidArtifactCapacity)
		}
	}
	required := estimate.InputBytes + estimate.IntermediateBytes + estimate.RenderBytes + estimate.ReplicaBytes + estimate.HeadroomBytes
	decision := ArtifactCapacityDecision{
		Status:         ArtifactCapacityReady,
		RequiredBytes:  required,
		AvailableBytes: estimate.AvailableBytes,
	}
	if required > estimate.AvailableBytes {
		decision.Status = ArtifactCapacityBlocked
		decision.ShortfallBytes = required - estimate.AvailableBytes
	}
	return decision, nil
}

// ArtifactReplicaObjectStore is an opt-in extension of the existing
// ArtifactMaterializationObjectStore. Integrators can detect this capability
// without changing the existing materializer contract or creating a second
// artifact store authority.
type ArtifactReplicaObjectStore interface {
	ArtifactMaterializationObjectStore
	Replicate(ctx context.Context, sourceKey string, location ArtifactStorageLocation, metadataDigest string) (ArtifactReplica, error)
	VerifyReplica(ctx context.Context, replica ArtifactReplica, expectedDigest, expectedMetadataDigest string) (ArtifactReplica, error)
}

// FixtureArtifactObjectStore is an in-memory, fixture://-only store. It is
// intentionally unsuitable for NAS, S3, or production use.
type FixtureArtifactObjectStore struct {
	mu             sync.RWMutex
	objects        map[string][]byte
	metadata       map[string]string
	uploadErr      error
	replicationErr error
	suspendedKeys  map[string]bool
}

func NewFixtureArtifactObjectStore() *FixtureArtifactObjectStore {
	return &FixtureArtifactObjectStore{
		objects:       make(map[string][]byte),
		metadata:      make(map[string]string),
		suspendedKeys: make(map[string]bool),
	}
}

func (s *FixtureArtifactObjectStore) Upload(_ context.Context, key string, data []byte, _ string, _ string) (string, error) {
	if s == nil {
		return "", ErrInvalidArtifactMaterialization
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.uploadErr; err != nil {
		return "", err
	}
	if s.suspendedKeys[key] {
		return "", ErrArtifactUploadSuspended
	}
	s.objects[key] = append([]byte(nil), data...)
	return s.ObjectURL(key), nil
}

func (s *FixtureArtifactObjectStore) DeleteObject(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	delete(s.metadata, key)
	return nil
}

func (*FixtureArtifactObjectStore) ObjectURL(key string) string {
	return "fixture://artifact/" + strings.TrimPrefix(path.Clean("/"+key), "/")
}

func (*FixtureArtifactObjectStore) KeyFromURL(ref string) string {
	parsed, err := url.Parse(ref)
	if err != nil || parsed.Scheme != "fixture" || parsed.Host != "artifact" {
		return ""
	}
	return strings.TrimPrefix(path.Clean(parsed.Path), "/")
}

func (s *FixtureArtifactObjectStore) SetUploadError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploadErr = err
}

func (s *FixtureArtifactObjectStore) SetReplicationError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replicationErr = err
}

func (s *FixtureArtifactObjectStore) SuspendUpload(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suspendedKeys[key] = true
}

func (s *FixtureArtifactObjectStore) ResumeUpload(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.suspendedKeys, key)
}

func (s *FixtureArtifactObjectStore) Replicate(_ context.Context, sourceKey string, location ArtifactStorageLocation, metadataDigest string) (ArtifactReplica, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.replicationErr; err != nil {
		return ArtifactReplica{Location: location, State: ArtifactReplicaFailed}, fmt.Errorf("%w: %v", ErrArtifactReplicaCopyFailed, err)
	}
	source, ok := s.objects[sourceKey]
	if !ok {
		return ArtifactReplica{Location: location, State: ArtifactReplicaFailed}, fmt.Errorf("%w: source key %q not found", ErrArtifactReplicaCopyFailed, sourceKey)
	}
	digest := SHA256Digest(source)
	targetKey := path.Join("replicas", string(location.Class), location.ID, path.Base(sourceKey))
	s.objects[targetKey] = append([]byte(nil), source...)
	s.metadata[targetKey] = metadataDigest
	location.ObjectRef = s.ObjectURL(targetKey)
	return ArtifactReplica{
		Location:       location,
		State:          ArtifactReplicaPending,
		Digest:         digest,
		MetadataDigest: metadataDigest,
		SizeBytes:      int64(len(source)),
	}, nil
}

func (s *FixtureArtifactObjectStore) VerifyReplica(_ context.Context, replica ArtifactReplica, expectedDigest, expectedMetadataDigest string) (ArtifactReplica, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := s.KeyFromURL(replica.Location.ObjectRef)
	data, ok := s.objects[key]
	if !ok {
		replica.State = ArtifactReplicaFailed
		return replica, fmt.Errorf("%w: object %q not found", ErrArtifactReplicaVerifyFailed, key)
	}
	digest := SHA256Digest(data)
	metadataDigest := s.metadata[key]
	replica.Digest = digest
	replica.MetadataDigest = metadataDigest
	if digest != expectedDigest || metadataDigest != expectedMetadataDigest {
		replica.State = ArtifactReplicaFailed
		return replica, fmt.Errorf("%w: digest or metadata mismatch", ErrArtifactReplicaVerifyFailed)
	}
	replica.State = ArtifactReplicaVerified
	return replica, nil
}

func SHA256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
