package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrInvalidArtifactReplicaLocation  = errors.New("invalid artifact replica location")
	ErrArtifactReplicaLocationConflict = errors.New("artifact replica location conflict")
	ErrArtifactReplicaLocationNotFound = errors.New("artifact replica location not found")
)

// ArtifactReplicaLocationState describes storage observation state only. It
// never replaces ArtifactLifecycleState or an Outcome authority event.
type ArtifactReplicaLocationState string

const (
	ArtifactReplicaLocationFixture    ArtifactReplicaLocationState = "fixture"
	ArtifactReplicaLocationRegistered ArtifactReplicaLocationState = "registered"
	ArtifactReplicaLocationPending    ArtifactReplicaLocationState = "pending"
	ArtifactReplicaLocationVerified   ArtifactReplicaLocationState = "verified"
	ArtifactReplicaLocationFailed     ArtifactReplicaLocationState = "failed"
)

// ArtifactReplicaLocation is a workspace-scoped placement ledger row. The
// OutcomeID is the existing assignment command/lineage identity; CandidateID
// identifies the immutable artifact candidate revision whose bytes the row
// describes. No field here confers acceptance or formal ownership.
type ArtifactReplicaLocation struct {
	ID                string
	WorkspaceID       string
	OutcomeID         string
	CandidateID       string
	CandidateRevision int
	Location          ArtifactStorageLocation
	State             ArtifactReplicaLocationState
	Digest            string
	MetadataDigest    string
	SizeBytes         int64
	Metadata          []byte
	CreatedAt         string
	UpdatedAt         string
}

// ArtifactReplicaLocationRepository persists storage placement observations
// through a caller-owned transaction. It deliberately has no Artifact or
// Outcome mutation methods: those authorities remain in their existing
// repositories/adapters.
type ArtifactReplicaLocationRepository struct {
	queries *db.Queries
}

func NewArtifactReplicaLocationRepository(tx pgx.Tx) *ArtifactReplicaLocationRepository {
	if tx == nil {
		return &ArtifactReplicaLocationRepository{}
	}
	return &ArtifactReplicaLocationRepository{queries: db.New(tx)}
}

// Record inserts a location row, or returns the existing row for an exact
// location identity. State changes must use UpdateState so a caller cannot
// silently replace the immutable identity or object reference.
func (r *ArtifactReplicaLocationRepository) Record(
	ctx context.Context,
	location ArtifactReplicaLocation,
) (ArtifactReplicaLocation, error) {
	if r == nil || r.queries == nil {
		return ArtifactReplicaLocation{}, ErrArtifactReplicaLocationNotFound
	}
	if err := validateArtifactReplicaLocation(location); err != nil {
		return ArtifactReplicaLocation{}, err
	}
	params, err := artifactReplicaLocationParams(location)
	if err != nil {
		return ArtifactReplicaLocation{}, err
	}
	row, err := r.queries.InsertArtifactReplicaLocation(ctx, params)
	if err == nil {
		return artifactReplicaLocationFromDB(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ArtifactReplicaLocation{}, fmt.Errorf("insert artifact replica location: %w", err)
	}

	existing, err := r.queries.GetArtifactReplicaLocationByIdentity(ctx, db.GetArtifactReplicaLocationByIdentityParams{
		WorkspaceID:   params.WorkspaceID,
		OutcomeID:     params.OutcomeID,
		CandidateID:   params.CandidateID,
		LocationClass: params.LocationClass,
		LocationID:    params.LocationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactReplicaLocation{}, ErrArtifactReplicaLocationConflict
	}
	if err != nil {
		return ArtifactReplicaLocation{}, fmt.Errorf("reload artifact replica location: %w", err)
	}
	stored := artifactReplicaLocationFromDB(existing)
	if !artifactReplicaLocationRegistrationMatches(stored, location) {
		return ArtifactReplicaLocation{}, ErrArtifactReplicaLocationConflict
	}
	return stored, nil
}

func (r *ArtifactReplicaLocationRepository) Get(
	ctx context.Context,
	workspaceID string,
	id string,
) (ArtifactReplicaLocation, error) {
	if r == nil || r.queries == nil {
		return ArtifactReplicaLocation{}, ErrArtifactReplicaLocationNotFound
	}
	workspaceUUID, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return ArtifactReplicaLocation{}, err
	}
	idUUID, err := artifactPersistenceUUID(id, "replica location id")
	if err != nil {
		return ArtifactReplicaLocation{}, err
	}
	row, err := r.queries.GetArtifactReplicaLocation(ctx, db.GetArtifactReplicaLocationParams{
		WorkspaceID: workspaceUUID,
		ID:          idUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactReplicaLocation{}, ErrArtifactReplicaLocationNotFound
	}
	if err != nil {
		return ArtifactReplicaLocation{}, fmt.Errorf("get artifact replica location: %w", err)
	}
	return artifactReplicaLocationFromDB(row), nil
}

// UpdateState updates only storage observation fields. It cannot alter the
// workspace, Outcome, candidate, location identity, or object reference.
func (r *ArtifactReplicaLocationRepository) UpdateState(
	ctx context.Context,
	workspaceID string,
	id string,
	state ArtifactReplicaLocationState,
	digest string,
	metadataDigest string,
	sizeBytes int64,
) (ArtifactReplicaLocation, error) {
	if r == nil || r.queries == nil {
		return ArtifactReplicaLocation{}, ErrArtifactReplicaLocationNotFound
	}
	location := ArtifactReplicaLocation{
		WorkspaceID:    workspaceID,
		ID:             id,
		State:          state,
		Digest:         digest,
		MetadataDigest: metadataDigest,
		SizeBytes:      sizeBytes,
	}
	if err := validateArtifactReplicaLocationState(location); err != nil {
		return ArtifactReplicaLocation{}, err
	}
	workspaceUUID, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return ArtifactReplicaLocation{}, err
	}
	idUUID, err := artifactPersistenceUUID(id, "replica location id")
	if err != nil {
		return ArtifactReplicaLocation{}, err
	}
	row, err := r.queries.UpdateArtifactReplicaLocationState(ctx, db.UpdateArtifactReplicaLocationStateParams{
		State:          string(state),
		Digest:         digest,
		MetadataDigest: metadataDigest,
		SizeBytes:      sizeBytes,
		WorkspaceID:    workspaceUUID,
		ID:             idUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactReplicaLocation{}, ErrArtifactReplicaLocationNotFound
	}
	if err != nil {
		return ArtifactReplicaLocation{}, fmt.Errorf("update artifact replica location state: %w", err)
	}
	return artifactReplicaLocationFromDB(row), nil
}

func (r *ArtifactReplicaLocationRepository) ListByOutcome(
	ctx context.Context,
	workspaceID string,
	outcomeID string,
) ([]ArtifactReplicaLocation, error) {
	if r == nil || r.queries == nil {
		return nil, ErrArtifactReplicaLocationNotFound
	}
	workspaceUUID, outcomeUUID, err := artifactWorkspaceLineageUUIDs(workspaceID, outcomeID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListArtifactReplicaLocationsByOutcome(ctx, db.ListArtifactReplicaLocationsByOutcomeParams{
		WorkspaceID: workspaceUUID,
		OutcomeID:   outcomeUUID,
	})
	if err != nil {
		return nil, fmt.Errorf("list artifact replica locations by outcome: %w", err)
	}
	return artifactReplicaLocationsFromDB(rows), nil
}

func (r *ArtifactReplicaLocationRepository) ListByCandidate(
	ctx context.Context,
	workspaceID string,
	candidateID string,
) ([]ArtifactReplicaLocation, error) {
	if r == nil || r.queries == nil {
		return nil, ErrArtifactReplicaLocationNotFound
	}
	workspaceUUID, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return nil, err
	}
	candidateUUID, err := artifactPersistenceUUID(candidateID, "candidate id")
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListArtifactReplicaLocationsByCandidate(ctx, db.ListArtifactReplicaLocationsByCandidateParams{
		WorkspaceID: workspaceUUID,
		CandidateID: candidateUUID,
	})
	if err != nil {
		return nil, fmt.Errorf("list artifact replica locations by candidate: %w", err)
	}
	return artifactReplicaLocationsFromDB(rows), nil
}

func validateArtifactReplicaLocation(location ArtifactReplicaLocation) error {
	if location.ID == "" || location.WorkspaceID == "" || location.OutcomeID == "" || location.CandidateID == "" {
		return fmt.Errorf("%w: id, workspace, outcome, and candidate are required", ErrInvalidArtifactReplicaLocation)
	}
	if location.CandidateRevision < 1 {
		return fmt.Errorf("%w: candidate revision must be positive", ErrInvalidArtifactReplicaLocation)
	}
	if _, err := artifactPersistenceUUID(location.ID, "replica location id"); err != nil {
		return err
	}
	if _, err := artifactPersistenceUUID(location.WorkspaceID, "workspace id"); err != nil {
		return err
	}
	if _, err := artifactPersistenceUUID(location.OutcomeID, "outcome id"); err != nil {
		return err
	}
	if _, err := artifactPersistenceUUID(location.CandidateID, "candidate id"); err != nil {
		return err
	}
	if location.Location.ID == "" || location.Location.StorageID == "" || location.Location.ObjectRef == "" {
		return fmt.Errorf("%w: location id, storage id, and object ref are required", ErrInvalidArtifactReplicaLocation)
	}
	if !artifactStorageLocationClassAllowed(location.Location.Class) {
		return fmt.Errorf("%w: unknown location class %q", ErrInvalidArtifactReplicaLocation, location.Location.Class)
	}
	if err := validateArtifactReplicaLocationState(location); err != nil {
		return err
	}
	if location.SizeBytes < 0 {
		return fmt.Errorf("%w: size cannot be negative", ErrInvalidArtifactReplicaLocation)
	}
	if len(location.Metadata) > 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(location.Metadata, &object); err != nil || object == nil {
			return fmt.Errorf("%w: metadata must be a JSON object", ErrInvalidArtifactReplicaLocation)
		}
	}
	return nil
}

func validateArtifactReplicaLocationState(location ArtifactReplicaLocation) error {
	switch location.State {
	case ArtifactReplicaLocationFixture,
		ArtifactReplicaLocationRegistered,
		ArtifactReplicaLocationPending,
		ArtifactReplicaLocationVerified,
		ArtifactReplicaLocationFailed:
	default:
		return fmt.Errorf("%w: unknown state %q", ErrInvalidArtifactReplicaLocation, location.State)
	}
	if location.SizeBytes < 0 {
		return fmt.Errorf("%w: size cannot be negative", ErrInvalidArtifactReplicaLocation)
	}
	return nil
}

func artifactStorageLocationClassAllowed(class ArtifactStorageLocationClass) bool {
	switch class {
	case ArtifactStorageLocationLocalCache,
		ArtifactStorageLocationNASPrimary,
		ArtifactStorageLocationOfflineCopy,
		ArtifactStorageLocationCloudReplica:
		return true
	default:
		return false
	}
}

func artifactReplicaLocationParams(location ArtifactReplicaLocation) (db.InsertArtifactReplicaLocationParams, error) {
	workspaceUUID, err := artifactPersistenceUUID(location.WorkspaceID, "workspace id")
	if err != nil {
		return db.InsertArtifactReplicaLocationParams{}, err
	}
	outcomeUUID, err := artifactPersistenceUUID(location.OutcomeID, "outcome id")
	if err != nil {
		return db.InsertArtifactReplicaLocationParams{}, err
	}
	candidateUUID, err := artifactPersistenceUUID(location.CandidateID, "candidate id")
	if err != nil {
		return db.InsertArtifactReplicaLocationParams{}, err
	}
	idUUID, err := artifactPersistenceUUID(location.ID, "replica location id")
	if err != nil {
		return db.InsertArtifactReplicaLocationParams{}, err
	}
	metadata := location.Metadata
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}
	return db.InsertArtifactReplicaLocationParams{
		ID:                idUUID,
		WorkspaceID:       workspaceUUID,
		OutcomeID:         outcomeUUID,
		CandidateID:       candidateUUID,
		CandidateRevision: int32(location.CandidateRevision),
		LocationClass:     string(location.Location.Class),
		LocationID:        location.Location.ID,
		StorageID:         location.Location.StorageID,
		ObjectRef:         location.Location.ObjectRef,
		State:             string(location.State),
		Digest:            location.Digest,
		MetadataDigest:    location.MetadataDigest,
		SizeBytes:         location.SizeBytes,
		RetentionHint:     location.Location.RetentionHint,
		Metadata:          metadata,
	}, nil
}

func artifactReplicaLocationFromDB(row db.ArtifactReplicaLocation) ArtifactReplicaLocation {
	metadata := append([]byte(nil), row.Metadata...)
	return ArtifactReplicaLocation{
		ID:                util.UUIDToString(row.ID),
		WorkspaceID:       util.UUIDToString(row.WorkspaceID),
		OutcomeID:         util.UUIDToString(row.OutcomeID),
		CandidateID:       util.UUIDToString(row.CandidateID),
		CandidateRevision: int(row.CandidateRevision),
		Location: ArtifactStorageLocation{
			ID:            row.LocationID,
			Class:         ArtifactStorageLocationClass(row.LocationClass),
			StorageID:     row.StorageID,
			ObjectRef:     row.ObjectRef,
			RetentionHint: row.RetentionHint,
		},
		State:          ArtifactReplicaLocationState(row.State),
		Digest:         row.Digest,
		MetadataDigest: row.MetadataDigest,
		SizeBytes:      row.SizeBytes,
		Metadata:       metadata,
		CreatedAt:      row.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05.999999999Z"),
		UpdatedAt:      row.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05.999999999Z"),
	}
}

func artifactReplicaLocationsFromDB(rows []db.ArtifactReplicaLocation) []ArtifactReplicaLocation {
	locations := make([]ArtifactReplicaLocation, 0, len(rows))
	for _, row := range rows {
		locations = append(locations, artifactReplicaLocationFromDB(row))
	}
	return locations
}

func artifactReplicaLocationRegistrationMatches(stored, requested ArtifactReplicaLocation) bool {
	return stored.ID == requested.ID &&
		stored.WorkspaceID == requested.WorkspaceID &&
		stored.OutcomeID == requested.OutcomeID &&
		stored.CandidateID == requested.CandidateID &&
		stored.CandidateRevision == requested.CandidateRevision &&
		stored.Location.ID == requested.Location.ID &&
		stored.Location.Class == requested.Location.Class &&
		stored.Location.StorageID == requested.Location.StorageID &&
		stored.Location.ObjectRef == requested.Location.ObjectRef &&
		stored.Location.RetentionHint == requested.Location.RetentionHint
}
