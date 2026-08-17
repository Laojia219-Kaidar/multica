package companyops

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ArtifactPersistenceTxStarter is the narrow database boundary needed to make
// the object-write intent durable before ArtifactMaterializer uploads bytes.
type ArtifactPersistenceTxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// DurableArtifactMaterializationRepository gives every materialization step
// its own committed database transaction. In particular, RecordIntent commits
// before Upload starts, so an ambiguous object-store outcome remains
// discoverable by the cleanup reconciler.
type DurableArtifactMaterializationRepository struct {
	txStarter ArtifactPersistenceTxStarter
}

func NewDurableArtifactMaterializationRepository(
	txStarter ArtifactPersistenceTxStarter,
) (*DurableArtifactMaterializationRepository, error) {
	if txStarter == nil {
		return nil, fmt.Errorf("artifact materialization transaction starter is required")
	}
	return &DurableArtifactMaterializationRepository{txStarter: txStarter}, nil
}

func artifactRepositoryTransaction[T any](
	ctx context.Context,
	txStarter ArtifactPersistenceTxStarter,
	operation func(*ArtifactPersistenceRepository) (T, error),
) (T, error) {
	var zero T
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin artifact persistence transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	value, err := operation(NewArtifactPersistenceRepository(tx))
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit artifact persistence transaction: %w", err)
	}
	committed = true
	return value, nil
}

func (r *DurableArtifactMaterializationRepository) FindArtifactCandidateByIdempotencyKey(
	ctx context.Context,
	workspaceID string,
	idempotencyKey string,
) (ArtifactCandidate, bool, error) {
	type result struct {
		candidate ArtifactCandidate
		found     bool
	}
	value, err := artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (result, error) {
		candidate, found, err := repo.FindArtifactCandidateByIdempotencyKey(ctx, workspaceID, idempotencyKey)
		return result{candidate: candidate, found: found}, err
	})
	return value.candidate, value.found, err
}

func (r *DurableArtifactMaterializationRepository) RecordArtifactMaterializationIntent(
	ctx context.Context,
	intent ArtifactMaterializationIntent,
) (ArtifactMaterializationIntent, error) {
	return artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (ArtifactMaterializationIntent, error) {
		return repo.RecordArtifactMaterializationIntent(ctx, intent)
	})
}

func (r *DurableArtifactMaterializationRepository) CommitArtifactCandidate(
	ctx context.Context,
	intent ArtifactMaterializationIntent,
	candidate ArtifactCandidate,
) (ArtifactCandidate, error) {
	return artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (ArtifactCandidate, error) {
		return repo.CommitArtifactCandidate(ctx, intent, candidate)
	})
}

func (r *DurableArtifactMaterializationRepository) MarkArtifactMaterializationCleanupPending(
	ctx context.Context,
	intent ArtifactMaterializationIntent,
	cause error,
) error {
	_, err := artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (struct{}, error) {
		return struct{}{}, repo.MarkArtifactMaterializationCleanupPending(ctx, intent, cause)
	})
	return err
}

func (r *DurableArtifactMaterializationRepository) GetArtifactCandidate(
	ctx context.Context,
	workspaceID string,
	candidateID string,
) (ArtifactCandidate, error) {
	return artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (ArtifactCandidate, error) {
		return repo.GetArtifactCandidate(ctx, workspaceID, candidateID)
	})
}

func (r *DurableArtifactMaterializationRepository) GetArtifactLifecycleProjection(
	ctx context.Context,
	workspaceID string,
	lineageID string,
) (ArtifactLifecycleProjection, error) {
	return artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (ArtifactLifecycleProjection, error) {
		return repo.GetArtifactLifecycleProjection(ctx, workspaceID, lineageID)
	})
}

func (r *DurableArtifactMaterializationRepository) AppendArtifactEvent(
	ctx context.Context,
	workspaceID string,
	lineageID string,
	input ArtifactEventInput,
) (ArtifactEvent, error) {
	return artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (ArtifactEvent, error) {
		return repo.AppendArtifactEvent(ctx, workspaceID, lineageID, input)
	})
}

func (r *DurableArtifactMaterializationRepository) ListArtifactEvents(
	ctx context.Context,
	workspaceID string,
	lineageID string,
) ([]ArtifactEvent, error) {
	return artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) ([]ArtifactEvent, error) {
		return repo.ListArtifactEvents(ctx, workspaceID, lineageID)
	})
}

func (r *DurableArtifactMaterializationRepository) ClaimPromotion(
	ctx context.Context,
	workspaceID string,
	promotionID string,
	candidateID string,
	lineageID string,
	payload PromotionClaimPayload,
) error {
	_, err := artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (struct{}, error) {
		return struct{}{}, repo.ClaimPromotion(ctx, workspaceID, promotionID, candidateID, lineageID, payload)
	})
	return err
}

func (r *DurableArtifactMaterializationRepository) VerifyPromotion(
	ctx context.Context,
	workspaceID string,
	promotionID string,
	candidateID string,
	lineageID string,
	payload PromotionClaimPayload,
) error {
	_, err := artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (struct{}, error) {
		return struct{}{}, repo.VerifyPromotion(ctx, workspaceID, promotionID, candidateID, lineageID, payload)
	})
	return err
}

func (r *DurableArtifactMaterializationRepository) EnsureArtifactPromotionDelivery(ctx context.Context, workspaceID, promotionID, candidateID, lineageID string, payload PromotionClaimPayload, requestPayload []byte) (db.ArtifactPromotionDelivery, error) {
	return artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (db.ArtifactPromotionDelivery, error) {
		return repo.EnsureArtifactPromotionDelivery(ctx, workspaceID, promotionID, candidateID, lineageID, payload, requestPayload)
	})
}

func (r *DurableArtifactMaterializationRepository) GetArtifactPromotionDelivery(ctx context.Context, workspaceID, promotionID string) (db.ArtifactPromotionDelivery, error) {
	return artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (db.ArtifactPromotionDelivery, error) {
		workspace, err := artifactPersistenceUUID(workspaceID, "workspace id")
		if err != nil {
			return db.ArtifactPromotionDelivery{}, err
		}
		return repo.queries.GetArtifactPromotionDelivery(ctx, db.GetArtifactPromotionDeliveryParams{WorkspaceID: workspace, PromotionID: promotionID})
	})
}

func (r *DurableArtifactMaterializationRepository) ClaimArtifactPromotionDelivery(ctx context.Context, workspaceID, promotionID, payloadDigest string) (db.ArtifactPromotionDelivery, error) {
	return artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (db.ArtifactPromotionDelivery, error) {
		return repo.ClaimArtifactPromotionDelivery(ctx, workspaceID, promotionID, payloadDigest)
	})
}

func (r *DurableArtifactMaterializationRepository) MarkArtifactPromotionDeliverySucceeded(ctx context.Context, row db.ArtifactPromotionDelivery, response []byte) error {
	_, err := artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (struct{}, error) {
		return struct{}{}, repo.MarkArtifactPromotionDeliverySucceeded(ctx, row, response)
	})
	return err
}

func (r *DurableArtifactMaterializationRepository) MarkArtifactPromotionDeliveryFailed(ctx context.Context, row db.ArtifactPromotionDelivery, message string) error {
	_, err := artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (struct{}, error) {
		return struct{}{}, repo.MarkArtifactPromotionDeliveryFailed(ctx, row, message)
	})
	return err
}

func (r *DurableArtifactMaterializationRepository) MarkArtifactPromotionDeliveryDefiniteAbsent(ctx context.Context, row db.ArtifactPromotionDelivery, message string) error {
	_, err := artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (struct{}, error) {
		return struct{}{}, repo.MarkArtifactPromotionDeliveryDefiniteAbsent(ctx, row, message)
	})
	return err
}

func (r *DurableArtifactMaterializationRepository) MarkArtifactPromotionDeliveryReadbackConfirmed(ctx context.Context, row db.ArtifactPromotionDelivery, readback []byte) error {
	_, err := artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (struct{}, error) {
		return struct{}{}, repo.MarkArtifactPromotionDeliveryReadbackConfirmed(ctx, row, readback)
	})
	return err
}

func (r *DurableArtifactMaterializationRepository) RecoverArtifactPromotionDeliveryFromReadback(ctx context.Context, row db.ArtifactPromotionDelivery, response, readback []byte) error {
	_, err := artifactRepositoryTransaction(ctx, r.txStarter, func(repo *ArtifactPersistenceRepository) (struct{}, error) {
		return struct{}{}, repo.RecoverArtifactPromotionDeliveryFromReadback(ctx, row, response, readback)
	})
	return err
}

var _ ArtifactMaterializationRepository = (*DurableArtifactMaterializationRepository)(nil)
