package companyops

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrArtifactCandidateImmutable          = errors.New("artifact candidate is immutable")
	ErrArtifactMaterializationIntentAbsent = errors.New("artifact materialization intent not found")
	ErrArtifactMaterializationConflict     = errors.New("artifact materialization intent conflict")
)

type ArtifactMaterializationState string

const (
	ArtifactMaterializationPending        ArtifactMaterializationState = "pending"
	ArtifactMaterializationCleanupPending ArtifactMaterializationState = "cleanup_pending"
	ArtifactMaterializationTombstoned     ArtifactMaterializationState = "tombstoned"
)

type ArtifactMaterializationCleanupDecision string

const (
	ArtifactMaterializationKeepObject   ArtifactMaterializationCleanupDecision = "keep_object"
	ArtifactMaterializationDeleteObject ArtifactMaterializationCleanupDecision = "delete_object"
)

// ArtifactMaterializationRecord is operational recovery state. Its embedded
// intent is a copy of the object-write fence, never a second artifact value.
type ArtifactMaterializationRecord struct {
	ArtifactMaterializationIntent
	State     ArtifactMaterializationState
	LastError string
}

// ArtifactPersistenceRepository persists candidate snapshots and append-only
// review events through a caller-owned transaction.
type ArtifactPersistenceRepository struct {
	tx      pgx.Tx
	queries *db.Queries
}

func NewArtifactPersistenceRepository(tx pgx.Tx) *ArtifactPersistenceRepository {
	return &ArtifactPersistenceRepository{tx: tx, queries: db.New(tx)}
}

func (r *ArtifactPersistenceRepository) FindArtifactCandidateByIdempotencyKey(
	ctx context.Context,
	workspaceID string,
	idempotencyKey string,
) (ArtifactCandidate, bool, error) {
	workspaceUUID, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return ArtifactCandidate{}, false, err
	}
	if idempotencyKey == "" {
		return ArtifactCandidate{}, false, ErrArtifactIdempotencyRequired
	}
	row, err := r.queries.GetArtifactCandidateByIdempotency(ctx, db.GetArtifactCandidateByIdempotencyParams{
		WorkspaceID:    workspaceUUID,
		IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactCandidate{}, false, nil
	}
	if err != nil {
		return ArtifactCandidate{}, false, fmt.Errorf("get artifact candidate by idempotency key: %w", err)
	}
	return artifactCandidateFromDB(row), true, nil
}

func (r *ArtifactPersistenceRepository) RecordArtifactMaterializationIntent(
	ctx context.Context,
	intent ArtifactMaterializationIntent,
) (ArtifactMaterializationIntent, error) {
	params, err := artifactMaterializationIntentParams(intent)
	if err != nil {
		return ArtifactMaterializationIntent{}, err
	}
	row, err := r.queries.InsertArtifactMaterializationIntent(ctx, params)
	if err == nil {
		return artifactMaterializationIntentFromDB(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ArtifactMaterializationIntent{}, fmt.Errorf("insert artifact materialization intent: %w", err)
	}

	existing, err := r.queries.GetArtifactMaterializationIntent(ctx, db.GetArtifactMaterializationIntentParams{
		WorkspaceID: params.WorkspaceID,
		StorageKey:  intent.StorageKey,
	})
	if err != nil {
		return ArtifactMaterializationIntent{}, fmt.Errorf("reload artifact materialization intent: %w", err)
	}
	stored := artifactMaterializationIntentFromDB(existing)
	if stored != intent || existing.State != string(ArtifactMaterializationPending) {
		return ArtifactMaterializationIntent{}, ErrArtifactMaterializationConflict
	}
	return stored, nil
}

func (r *ArtifactPersistenceRepository) CommitArtifactCandidate(
	ctx context.Context,
	intent ArtifactMaterializationIntent,
	candidate ArtifactCandidate,
) (ArtifactCandidate, error) {
	if err := validateArtifactCandidate(candidate); err != nil {
		return ArtifactCandidate{}, err
	}
	if err := artifactCandidateMatchesIntent(candidate, intent); err != nil {
		return ArtifactCandidate{}, err
	}
	params, err := artifactMaterializationIntentParams(intent)
	if err != nil {
		return ArtifactCandidate{}, err
	}

	child, err := r.tx.Begin(ctx)
	if err != nil {
		return ArtifactCandidate{}, fmt.Errorf("begin artifact candidate commit: %w", err)
	}
	defer func() { _ = child.Rollback(ctx) }()
	queries := db.New(child)

	if err := queries.LockArtifactLineage(ctx, artifactLineageLockKey(intent.WorkspaceID, candidate.LineageID)); err != nil {
		return ArtifactCandidate{}, fmt.Errorf("lock artifact lineage: %w", err)
	}
	if existingRow, found, err := findArtifactCandidateRowByIdempotency(ctx, queries, params.WorkspaceID, intent.IdempotencyKey); err != nil {
		return ArtifactCandidate{}, err
	} else if found {
		existing := artifactCandidateFromDB(existingRow)
		if existing != candidate {
			return ArtifactCandidate{}, ErrArtifactIdempotencyConflict
		}
		if err := artifactCandidateRowMatchesIntent(existingRow, intent); err != nil {
			return ArtifactCandidate{}, err
		}
		if err := child.Commit(ctx); err != nil {
			return ArtifactCandidate{}, fmt.Errorf("commit artifact candidate replay: %w", err)
		}
		return existing, nil
	}

	candidateUUID, err := artifactPersistenceUUID(candidate.ID, "candidate id")
	if err != nil {
		return ArtifactCandidate{}, err
	}
	if _, err := queries.GetArtifactCandidate(ctx, db.GetArtifactCandidateParams{
		WorkspaceID: params.WorkspaceID,
		ID:          candidateUUID,
	}); err == nil {
		return ArtifactCandidate{}, ErrArtifactCandidateImmutable
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ArtifactCandidate{}, fmt.Errorf("check artifact candidate identity: %w", err)
	}

	lifecycle, err := loadArtifactLifecycle(ctx, queries, params.WorkspaceID, params.LineageID)
	if err != nil {
		return ArtifactCandidate{}, err
	}
	if lifecycle == nil {
		lifecycle, err = NewArtifactLifecycle(candidate)
	} else {
		err = lifecycle.AddRevision(candidate)
	}
	if err != nil {
		return ArtifactCandidate{}, err
	}
	submittedInput := ArtifactEventInput{
		Type:               ArtifactEventSubmitted,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		IdempotencyKey:     intent.IdempotencyKey,
	}
	if _, err := lifecycle.Append(submittedInput); err != nil {
		return ArtifactCandidate{}, err
	}

	candidateParams, err := artifactCandidateInsertParams(params, candidate)
	if err != nil {
		return ArtifactCandidate{}, err
	}
	inserted, err := queries.InsertArtifactCandidate(ctx, candidateParams)
	if errors.Is(err, pgx.ErrNoRows) {
		return resolveArtifactCandidateConflict(ctx, queries, params.WorkspaceID, candidate, intent)
	}
	if err != nil {
		return ArtifactCandidate{}, fmt.Errorf("insert artifact candidate: %w", err)
	}

	sequence, err := queries.NextArtifactEventSequence(ctx, db.NextArtifactEventSequenceParams{
		WorkspaceID: params.WorkspaceID,
		LineageID:   params.LineageID,
	})
	if err != nil {
		return ArtifactCandidate{}, fmt.Errorf("next submitted artifact event sequence: %w", err)
	}
	if _, err := queries.InsertArtifactEvent(ctx, artifactEventInsertParams(
		params.WorkspaceID,
		params.LineageID,
		sequence,
		submittedInput,
	)); err != nil {
		return ArtifactCandidate{}, fmt.Errorf("insert submitted artifact event: %w", err)
	}
	deleted, err := queries.DeleteCommittedArtifactMaterializationIntent(ctx, db.DeleteCommittedArtifactMaterializationIntentParams{
		WorkspaceID:      params.WorkspaceID,
		StorageKey:       intent.StorageKey,
		CandidateID:      params.CandidateID,
		LineageID:        params.LineageID,
		DurableObjectRef: intent.DurableObjectRef,
		Digest:           intent.Digest,
	})
	if err != nil {
		return ArtifactCandidate{}, fmt.Errorf("delete committed artifact materialization intent: %w", err)
	}
	if deleted != 1 {
		return ArtifactCandidate{}, ErrArtifactMaterializationConflict
	}
	if err := child.Commit(ctx); err != nil {
		return ArtifactCandidate{}, fmt.Errorf("commit artifact candidate transaction: %w", err)
	}
	return artifactCandidateFromDB(inserted), nil
}

func (r *ArtifactPersistenceRepository) GetArtifactCandidate(
	ctx context.Context,
	workspaceID string,
	candidateID string,
) (ArtifactCandidate, error) {
	workspaceUUID, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return ArtifactCandidate{}, err
	}
	candidateUUID, err := artifactPersistenceUUID(candidateID, "candidate id")
	if err != nil {
		return ArtifactCandidate{}, err
	}
	row, err := r.queries.GetArtifactCandidate(ctx, db.GetArtifactCandidateParams{
		WorkspaceID: workspaceUUID,
		ID:          candidateUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactCandidate{}, ErrArtifactCandidateNotFound
	}
	if err != nil {
		return ArtifactCandidate{}, fmt.Errorf("get artifact candidate: %w", err)
	}
	return artifactCandidateFromDB(row), nil
}

func (r *ArtifactPersistenceRepository) AppendArtifactEvent(
	ctx context.Context,
	workspaceID string,
	lineageID string,
	input ArtifactEventInput,
) (ArtifactEvent, error) {
	workspaceUUID, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return ArtifactEvent{}, err
	}
	lineageUUID, err := artifactPersistenceUUID(lineageID, "lineage id")
	if err != nil {
		return ArtifactEvent{}, err
	}
	if input.IdempotencyKey == "" {
		return ArtifactEvent{}, ErrArtifactIdempotencyRequired
	}
	if err := r.queries.LockArtifactLineage(ctx, artifactLineageLockKey(workspaceID, lineageID)); err != nil {
		return ArtifactEvent{}, fmt.Errorf("lock artifact lineage: %w", err)
	}
	if existing, found, err := getArtifactEventByIdempotency(ctx, r.queries, workspaceUUID, input.IdempotencyKey); err != nil {
		return ArtifactEvent{}, err
	} else if found {
		if util.UUIDToString(existing.LineageID) != lineageID || !artifactEventMatchesInput(artifactEventFromDB(existing), input) {
			return ArtifactEvent{}, ErrArtifactIdempotencyConflict
		}
		return artifactEventFromDB(existing), nil
	}

	lifecycle, err := loadArtifactLifecycle(ctx, r.queries, workspaceUUID, lineageUUID)
	if err != nil {
		return ArtifactEvent{}, err
	}
	if lifecycle == nil {
		return ArtifactEvent{}, ErrArtifactCandidateNotFound
	}
	if _, err := lifecycle.Append(input); err != nil {
		return ArtifactEvent{}, err
	}
	sequence, err := r.queries.NextArtifactEventSequence(ctx, db.NextArtifactEventSequenceParams{
		WorkspaceID: workspaceUUID,
		LineageID:   lineageUUID,
	})
	if err != nil {
		return ArtifactEvent{}, fmt.Errorf("next artifact event sequence: %w", err)
	}
	row, err := r.queries.InsertArtifactEvent(ctx, artifactEventInsertParams(workspaceUUID, lineageUUID, sequence, input))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, reloadErr := getArtifactEventByIdempotency(ctx, r.queries, workspaceUUID, input.IdempotencyKey)
		if reloadErr != nil {
			return ArtifactEvent{}, reloadErr
		}
		if found && util.UUIDToString(existing.LineageID) == lineageID && artifactEventMatchesInput(artifactEventFromDB(existing), input) {
			return artifactEventFromDB(existing), nil
		}
		return ArtifactEvent{}, ErrArtifactIdempotencyConflict
	}
	if err != nil {
		return ArtifactEvent{}, fmt.Errorf("insert artifact event: %w", err)
	}
	return artifactEventFromDB(row), nil
}

func (r *ArtifactPersistenceRepository) ListArtifactEvents(
	ctx context.Context,
	workspaceID string,
	lineageID string,
) ([]ArtifactEvent, error) {
	workspaceUUID, lineageUUID, err := artifactWorkspaceLineageUUIDs(workspaceID, lineageID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListArtifactEventsByLineage(ctx, db.ListArtifactEventsByLineageParams{
		WorkspaceID: workspaceUUID,
		LineageID:   lineageUUID,
	})
	if err != nil {
		return nil, fmt.Errorf("list artifact events: %w", err)
	}
	events := make([]ArtifactEvent, len(rows))
	for i := range rows {
		events[i] = artifactEventFromDB(rows[i])
	}
	return events, nil
}

func (r *ArtifactPersistenceRepository) GetArtifactLifecycleProjection(
	ctx context.Context,
	workspaceID string,
	lineageID string,
) (ArtifactLifecycleProjection, error) {
	workspaceUUID, lineageUUID, err := artifactWorkspaceLineageUUIDs(workspaceID, lineageID)
	if err != nil {
		return ArtifactLifecycleProjection{}, err
	}
	lifecycle, err := loadArtifactLifecycle(ctx, r.queries, workspaceUUID, lineageUUID)
	if err != nil {
		return ArtifactLifecycleProjection{}, err
	}
	if lifecycle == nil {
		return ArtifactLifecycleProjection{}, ErrArtifactCandidateNotFound
	}
	return lifecycle.Projection(), nil
}

func (r *ArtifactPersistenceRepository) MarkArtifactMaterializationCleanupPending(
	ctx context.Context,
	intent ArtifactMaterializationIntent,
	cause error,
) error {
	params, err := artifactMaterializationIntentParams(intent)
	if err != nil {
		return err
	}
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
	}
	_, err = r.queries.MarkArtifactMaterializationIntentCleanupPending(ctx, db.MarkArtifactMaterializationIntentCleanupPendingParams{
		LastError:        artifactPersistenceText(lastError),
		WorkspaceID:      params.WorkspaceID,
		StorageKey:       intent.StorageKey,
		CandidateID:      params.CandidateID,
		LineageID:        params.LineageID,
		DurableObjectRef: intent.DurableObjectRef,
		Digest:           intent.Digest,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("mark artifact materialization cleanup pending: %w", err)
	}
	record, getErr := r.GetArtifactMaterializationRecord(ctx, intent.WorkspaceID, intent.StorageKey)
	if getErr != nil {
		return getErr
	}
	if record.ArtifactMaterializationIntent != intent {
		return ErrArtifactMaterializationConflict
	}
	if record.State == ArtifactMaterializationCleanupPending || record.State == ArtifactMaterializationTombstoned {
		return nil
	}
	return ErrArtifactMaterializationConflict
}

func (r *ArtifactPersistenceRepository) GetArtifactMaterializationRecord(
	ctx context.Context,
	workspaceID string,
	storageKey string,
) (ArtifactMaterializationRecord, error) {
	workspaceUUID, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return ArtifactMaterializationRecord{}, err
	}
	row, err := r.queries.GetArtifactMaterializationIntent(ctx, db.GetArtifactMaterializationIntentParams{
		WorkspaceID: workspaceUUID,
		StorageKey:  storageKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactMaterializationRecord{}, ErrArtifactMaterializationIntentAbsent
	}
	if err != nil {
		return ArtifactMaterializationRecord{}, fmt.Errorf("get artifact materialization intent: %w", err)
	}
	return artifactMaterializationRecordFromDB(row), nil
}

func (r *ArtifactPersistenceRepository) TombstoneArtifactMaterializationIntent(
	ctx context.Context,
	workspaceID string,
	storageKey string,
) error {
	workspaceUUID, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return err
	}
	_, err = r.queries.TombstoneArtifactMaterializationIntent(ctx, db.TombstoneArtifactMaterializationIntentParams{
		WorkspaceID: workspaceUUID,
		StorageKey:  storageKey,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("tombstone artifact materialization intent: %w", err)
	}
	record, getErr := r.GetArtifactMaterializationRecord(ctx, workspaceID, storageKey)
	if getErr != nil {
		return getErr
	}
	if record.State == ArtifactMaterializationTombstoned {
		return nil
	}
	return ErrArtifactMaterializationConflict
}

func (r *ArtifactPersistenceRepository) DecideArtifactMaterializationCleanup(
	ctx context.Context,
	record ArtifactMaterializationRecord,
) (ArtifactMaterializationCleanupDecision, error) {
	if record.State != ArtifactMaterializationCleanupPending {
		return "", ErrArtifactMaterializationConflict
	}
	params, err := artifactMaterializationIntentParams(record.ArtifactMaterializationIntent)
	if err != nil {
		return "", err
	}
	exact, err := r.queries.IsArtifactMaterializationExactlyReferenced(ctx, db.IsArtifactMaterializationExactlyReferencedParams{
		WorkspaceID:      params.WorkspaceID,
		CandidateID:      params.CandidateID,
		LineageID:        params.LineageID,
		StorageKey:       record.StorageKey,
		DurableObjectRef: record.DurableObjectRef,
		Digest:           record.Digest,
	})
	if err != nil {
		return "", fmt.Errorf("decide artifact materialization cleanup: %w", err)
	}
	if exact {
		return ArtifactMaterializationKeepObject, nil
	}
	return ArtifactMaterializationDeleteObject, nil
}

func loadArtifactLifecycle(
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	lineageID pgtype.UUID,
) (*ArtifactLifecycle, error) {
	candidateRows, err := queries.ListArtifactCandidatesByLineage(ctx, db.ListArtifactCandidatesByLineageParams{
		WorkspaceID: workspaceID,
		LineageID:   lineageID,
	})
	if err != nil {
		return nil, fmt.Errorf("list artifact lineage candidates: %w", err)
	}
	if len(candidateRows) == 0 {
		return nil, nil
	}
	eventRows, err := queries.ListArtifactEventsByLineage(ctx, db.ListArtifactEventsByLineageParams{
		WorkspaceID: workspaceID,
		LineageID:   lineageID,
	})
	if err != nil {
		return nil, fmt.Errorf("list artifact lineage events: %w", err)
	}

	candidates := make([]ArtifactCandidate, len(candidateRows))
	for i := range candidateRows {
		candidates[i] = artifactCandidateFromDB(candidateRows[i])
	}
	lifecycle, err := NewArtifactLifecycle(candidates[0])
	if err != nil {
		return nil, fmt.Errorf("restore artifact lifecycle: %w", err)
	}
	nextRevision := 1
	for _, row := range eventRows {
		event := artifactEventFromDB(row)
		for nextRevision < len(candidates) && event.CandidateID == candidates[nextRevision].ID {
			if err := lifecycle.AddRevision(candidates[nextRevision]); err != nil {
				return nil, fmt.Errorf("restore artifact revision %d: %w", candidates[nextRevision].Revision, err)
			}
			nextRevision++
		}
		stored, err := lifecycle.Append(artifactEventInputFromEvent(event))
		if err != nil {
			return nil, fmt.Errorf("restore artifact event sequence %d: %w", event.Sequence, err)
		}
		if stored.Sequence != event.Sequence {
			return nil, fmt.Errorf("restore artifact event sequence: stored=%d database=%d", stored.Sequence, event.Sequence)
		}
	}
	if nextRevision != len(candidates) {
		return nil, fmt.Errorf("artifact lifecycle contains a candidate without its submitted event")
	}
	return lifecycle, nil
}

func findArtifactCandidateRowByIdempotency(
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	idempotencyKey string,
) (db.ArtifactCandidate, bool, error) {
	row, err := queries.GetArtifactCandidateByIdempotency(ctx, db.GetArtifactCandidateByIdempotencyParams{
		WorkspaceID:    workspaceID,
		IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ArtifactCandidate{}, false, nil
	}
	if err != nil {
		return db.ArtifactCandidate{}, false, fmt.Errorf("get artifact candidate idempotency replay: %w", err)
	}
	return row, true, nil
}

func resolveArtifactCandidateConflict(
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	candidate ArtifactCandidate,
	intent ArtifactMaterializationIntent,
) (ArtifactCandidate, error) {
	if existingRow, found, err := findArtifactCandidateRowByIdempotency(ctx, queries, workspaceID, intent.IdempotencyKey); err != nil {
		return ArtifactCandidate{}, err
	} else if found {
		existing := artifactCandidateFromDB(existingRow)
		if existing == candidate {
			if err := artifactCandidateRowMatchesIntent(existingRow, intent); err != nil {
				return ArtifactCandidate{}, err
			}
			return existing, nil
		}
		return ArtifactCandidate{}, ErrArtifactIdempotencyConflict
	}
	candidateUUID, err := artifactPersistenceUUID(candidate.ID, "candidate id")
	if err != nil {
		return ArtifactCandidate{}, err
	}
	if _, err := queries.GetArtifactCandidate(ctx, db.GetArtifactCandidateParams{WorkspaceID: workspaceID, ID: candidateUUID}); err == nil {
		return ArtifactCandidate{}, ErrArtifactCandidateImmutable
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ArtifactCandidate{}, fmt.Errorf("reload conflicting artifact candidate: %w", err)
	}
	return ArtifactCandidate{}, ErrArtifactCandidateImmutable
}

func getArtifactEventByIdempotency(
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	idempotencyKey string,
) (db.ArtifactEvent, bool, error) {
	row, err := queries.GetArtifactEventByIdempotency(ctx, db.GetArtifactEventByIdempotencyParams{
		WorkspaceID:    workspaceID,
		IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ArtifactEvent{}, false, nil
	}
	if err != nil {
		return db.ArtifactEvent{}, false, fmt.Errorf("get artifact event idempotency replay: %w", err)
	}
	return row, true, nil
}

func artifactCandidateInsertParams(
	intent db.InsertArtifactMaterializationIntentParams,
	candidate ArtifactCandidate,
) (db.InsertArtifactCandidateParams, error) {
	supersedesID, err := artifactPersistenceOptionalUUID(candidate.SupersedesID, "supersedes id")
	if err != nil {
		return db.InsertArtifactCandidateParams{}, err
	}
	return db.InsertArtifactCandidateParams{
		ID:                 intent.CandidateID,
		WorkspaceID:        intent.WorkspaceID,
		LineageID:          intent.LineageID,
		Revision:           int32(candidate.Revision),
		SupersedesID:       supersedesID,
		StorageKey:         intent.StorageKey,
		DurableObjectRef:   intent.DurableObjectRef,
		Digest:             intent.Digest,
		Filename:           intent.Filename,
		ContentType:        intent.ContentType,
		SizeBytes:          intent.SizeBytes,
		SourceAttachmentID: intent.SourceAttachmentID,
		SourceCommentID:    intent.SourceCommentID,
		IdempotencyKey:     intent.IdempotencyKey,
	}, nil
}

func artifactEventInsertParams(
	workspaceID pgtype.UUID,
	lineageID pgtype.UUID,
	sequence int32,
	input ArtifactEventInput,
) db.InsertArtifactEventParams {
	return db.InsertArtifactEventParams{
		ID:                 util.MustParseUUID(uuid.NewString()),
		WorkspaceID:        workspaceID,
		LineageID:          lineageID,
		Sequence:           sequence,
		EventType:          string(input.Type),
		CandidateID:        util.MustParseUUID(input.CandidateID),
		CandidateRevision:  int32(input.CandidateRevision),
		CandidateDigest:    input.CandidateDigest,
		CandidateObjectRef: input.CandidateObjectRef,
		FormalArtifactRef:  artifactPersistenceText(input.FormalArtifactRef),
		IdempotencyKey:     input.IdempotencyKey,
	}
}

func artifactMaterializationIntentParams(intent ArtifactMaterializationIntent) (db.InsertArtifactMaterializationIntentParams, error) {
	workspaceID, err := artifactPersistenceUUID(intent.WorkspaceID, "workspace id")
	if err != nil {
		return db.InsertArtifactMaterializationIntentParams{}, err
	}
	candidateID, err := artifactPersistenceUUID(intent.CandidateID, "candidate id")
	if err != nil {
		return db.InsertArtifactMaterializationIntentParams{}, err
	}
	lineageID, err := artifactPersistenceUUID(intent.LineageID, "lineage id")
	if err != nil {
		return db.InsertArtifactMaterializationIntentParams{}, err
	}
	if intent.StorageKey == "" || intent.DurableObjectRef == "" || intent.Digest == "" || intent.IdempotencyKey == "" {
		return db.InsertArtifactMaterializationIntentParams{}, ErrInvalidArtifactMaterialization
	}
	sourceAttachmentID, err := artifactPersistenceOptionalUUID(intent.SourceAttachmentID, "source attachment id")
	if err != nil {
		return db.InsertArtifactMaterializationIntentParams{}, err
	}
	sourceCommentID, err := artifactPersistenceOptionalUUID(intent.SourceCommentID, "source comment id")
	if err != nil {
		return db.InsertArtifactMaterializationIntentParams{}, err
	}
	return db.InsertArtifactMaterializationIntentParams{
		WorkspaceID:        workspaceID,
		CandidateID:        candidateID,
		LineageID:          lineageID,
		StorageKey:         intent.StorageKey,
		DurableObjectRef:   intent.DurableObjectRef,
		Digest:             intent.Digest,
		Filename:           intent.Filename,
		ContentType:        intent.ContentType,
		SizeBytes:          intent.SizeBytes,
		SourceAttachmentID: sourceAttachmentID,
		SourceCommentID:    sourceCommentID,
		IdempotencyKey:     intent.IdempotencyKey,
	}, nil
}

func artifactCandidateMatchesIntent(candidate ArtifactCandidate, intent ArtifactMaterializationIntent) error {
	switch {
	case candidate.ID != intent.CandidateID || candidate.LineageID != intent.LineageID:
		return ErrArtifactMaterializationConflict
	case candidate.DurableObjectRef != intent.DurableObjectRef:
		return ErrArtifactObjectRefMismatch
	case candidate.Digest != intent.Digest:
		return ErrArtifactDigestMismatch
	case candidate.SourceAttachmentID != intent.SourceAttachmentID || candidate.SourceCommentID != intent.SourceCommentID:
		return ErrArtifactMaterializationConflict
	default:
		return nil
	}
}

func artifactCandidateRowMatchesIntent(row db.ArtifactCandidate, intent ArtifactMaterializationIntent) error {
	switch {
	case row.StorageKey != intent.StorageKey:
		return ErrArtifactStorageKeyMismatch
	case row.DurableObjectRef != intent.DurableObjectRef:
		return ErrArtifactObjectRefMismatch
	case row.Digest != intent.Digest:
		return ErrArtifactDigestMismatch
	case row.Filename != intent.Filename || row.ContentType != intent.ContentType || row.SizeBytes != intent.SizeBytes:
		return ErrArtifactMaterializationConflict
	case util.UUIDToString(row.SourceAttachmentID) != intent.SourceAttachmentID || util.UUIDToString(row.SourceCommentID) != intent.SourceCommentID:
		return ErrArtifactMaterializationConflict
	default:
		return nil
	}
}

func artifactCandidateFromDB(row db.ArtifactCandidate) ArtifactCandidate {
	return ArtifactCandidate{
		ID:                 util.UUIDToString(row.ID),
		LineageID:          util.UUIDToString(row.LineageID),
		Revision:           int(row.Revision),
		SupersedesID:       util.UUIDToString(row.SupersedesID),
		DurableObjectRef:   row.DurableObjectRef,
		Digest:             row.Digest,
		SourceAttachmentID: util.UUIDToString(row.SourceAttachmentID),
		SourceCommentID:    util.UUIDToString(row.SourceCommentID),
	}
}

func artifactEventFromDB(row db.ArtifactEvent) ArtifactEvent {
	return ArtifactEvent{
		ID:                 util.UUIDToString(row.ID),
		Sequence:           int(row.Sequence),
		Type:               ArtifactEventType(row.EventType),
		CandidateID:        util.UUIDToString(row.CandidateID),
		CandidateRevision:  int(row.CandidateRevision),
		CandidateDigest:    row.CandidateDigest,
		CandidateObjectRef: row.CandidateObjectRef,
		FormalArtifactRef:  artifactPersistenceTextValue(row.FormalArtifactRef),
		IdempotencyKey:     row.IdempotencyKey,
	}
}

func artifactEventInputFromEvent(event ArtifactEvent) ArtifactEventInput {
	return ArtifactEventInput{
		Type:               event.Type,
		CandidateID:        event.CandidateID,
		CandidateRevision:  event.CandidateRevision,
		CandidateDigest:    event.CandidateDigest,
		CandidateObjectRef: event.CandidateObjectRef,
		FormalArtifactRef:  event.FormalArtifactRef,
		IdempotencyKey:     event.IdempotencyKey,
	}
}

func artifactMaterializationIntentFromDB(row db.ArtifactMaterializationIntent) ArtifactMaterializationIntent {
	return ArtifactMaterializationIntent{
		WorkspaceID:        util.UUIDToString(row.WorkspaceID),
		CandidateID:        util.UUIDToString(row.CandidateID),
		LineageID:          util.UUIDToString(row.LineageID),
		StorageKey:         row.StorageKey,
		DurableObjectRef:   row.DurableObjectRef,
		Digest:             row.Digest,
		Filename:           row.Filename,
		ContentType:        row.ContentType,
		SizeBytes:          row.SizeBytes,
		SourceAttachmentID: util.UUIDToString(row.SourceAttachmentID),
		SourceCommentID:    util.UUIDToString(row.SourceCommentID),
		IdempotencyKey:     row.IdempotencyKey,
	}
}

func artifactMaterializationRecordFromDB(row db.ArtifactMaterializationIntent) ArtifactMaterializationRecord {
	return ArtifactMaterializationRecord{
		ArtifactMaterializationIntent: artifactMaterializationIntentFromDB(row),
		State:                         ArtifactMaterializationState(row.State),
		LastError:                     artifactPersistenceTextValue(row.LastError),
	}
}

func artifactWorkspaceLineageUUIDs(workspaceID string, lineageID string) (pgtype.UUID, pgtype.UUID, error) {
	workspaceUUID, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	lineageUUID, err := artifactPersistenceUUID(lineageID, "lineage id")
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return workspaceUUID, lineageUUID, nil
}

func artifactPersistenceUUID(value string, field string) (pgtype.UUID, error) {
	parsed, err := util.ParseUUID(value)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid artifact %s: %w", field, err)
	}
	return parsed, nil
}

func artifactPersistenceOptionalUUID(value string, field string) (pgtype.UUID, error) {
	if value == "" {
		return pgtype.UUID{}, nil
	}
	return artifactPersistenceUUID(value, field)
}

func artifactPersistenceText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func artifactPersistenceTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func artifactLineageLockKey(workspaceID string, lineageID string) string {
	return "companyops-artifact:" + workspaceID + ":" + lineageID
}
