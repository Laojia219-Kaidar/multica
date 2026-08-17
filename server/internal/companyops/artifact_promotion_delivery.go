package companyops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (r *ArtifactPersistenceRepository) EnsureArtifactPromotionDelivery(
	ctx context.Context,
	workspaceID, promotionID, candidateID, lineageID string,
	payload PromotionClaimPayload, requestPayload []byte,
) (db.ArtifactPromotionDelivery, error) {
	workspace, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return db.ArtifactPromotionDelivery{}, err
	}
	candidate, err := artifactPersistenceUUID(candidateID, "candidate id")
	if err != nil {
		return db.ArtifactPromotionDelivery{}, err
	}
	lineage, err := artifactPersistenceUUID(lineageID, "lineage id")
	if err != nil {
		return db.ArtifactPromotionDelivery{}, err
	}
	if parsed, parseErr := util.ParseUUID(promotionID); parseErr != nil || util.UUIDToString(parsed) != promotionID {
		return db.ArtifactPromotionDelivery{}, fmt.Errorf("%w: promotion_id must be a canonical UUID", ErrArtifactPromotionConflict)
	}
	sourceTaskID, sourceTaskErr := artifactOptionalUUID(payload.SourceTaskID, "source task id")
	if sourceTaskErr != nil {
		return db.ArtifactPromotionDelivery{}, sourceTaskErr
	}
	row, err := r.queries.InsertArtifactPromotionDelivery(ctx, db.InsertArtifactPromotionDeliveryParams{
		WorkspaceID: workspace, PromotionID: promotionID, CandidateID: candidate, LineageID: lineage,
		SourceTaskID:            sourceTaskID,
		WriterLeaseTargetDigest: pgtype.Text{String: payload.WriterLeaseTargetDigest, Valid: payload.WriterLeaseTargetDigest != ""},
		CompletionReceiptDigest: pgtype.Text{String: payload.CompletionReceiptDigest, Valid: payload.CompletionReceiptDigest != ""},
		PayloadDigest:           payload.Digest(), RequestPayload: requestPayload,
	})
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.ArtifactPromotionDelivery{}, fmt.Errorf("insert artifact promotion delivery: %w", err)
	}
	existing, getErr := r.queries.GetArtifactPromotionDelivery(ctx, db.GetArtifactPromotionDeliveryParams{WorkspaceID: workspace, PromotionID: promotionID})
	if getErr != nil {
		return db.ArtifactPromotionDelivery{}, fmt.Errorf("read artifact promotion delivery replay: %w", getErr)
	}
	if existing.CandidateID != candidate || existing.LineageID != lineage || existing.PayloadDigest != payload.Digest() ||
		!canonicalJSONEqual(existing.RequestPayload, requestPayload) {
		return db.ArtifactPromotionDelivery{}, ErrArtifactPromotionConflict
	}
	return existing, nil
}

func canonicalJSONEqual(left, right []byte) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}
	lb, le := json.Marshal(l)
	rb, re := json.Marshal(r)
	return le == nil && re == nil && string(lb) == string(rb)
}

func (r *ArtifactPersistenceRepository) ClaimArtifactPromotionDelivery(ctx context.Context, workspaceID, promotionID, payloadDigest string) (db.ArtifactPromotionDelivery, error) {
	workspace, err := artifactPersistenceUUID(workspaceID, "workspace id")
	if err != nil {
		return db.ArtifactPromotionDelivery{}, err
	}
	row, err := r.queries.ClaimArtifactPromotionDelivery(ctx, db.ClaimArtifactPromotionDeliveryParams{WorkspaceID: workspace, PromotionID: promotionID, PayloadDigest: payloadDigest})
	if errors.Is(err, pgx.ErrNoRows) {
		if existing, getErr := r.queries.GetArtifactPromotionDelivery(ctx, db.GetArtifactPromotionDeliveryParams{WorkspaceID: workspace, PromotionID: promotionID}); getErr == nil && existing.State == "dispatching" {
			return db.ArtifactPromotionDelivery{}, ErrArtifactPromotionInProgress
		}
		return db.ArtifactPromotionDelivery{}, ErrArtifactPromotionConflict
	}
	if err != nil {
		return db.ArtifactPromotionDelivery{}, fmt.Errorf("claim artifact promotion delivery: %w", err)
	}
	return row, nil
}

func (r *ArtifactPersistenceRepository) MarkArtifactPromotionDeliverySucceeded(ctx context.Context, row db.ArtifactPromotionDelivery, response []byte) error {
	_, err := r.queries.MarkArtifactPromotionDeliverySucceeded(ctx, db.MarkArtifactPromotionDeliverySucceededParams{ResponseReceipt: response, WorkspaceID: row.WorkspaceID, PromotionID: row.PromotionID, PayloadDigest: row.PayloadDigest, DispatchToken: row.DispatchToken})
	return err
}

func (r *ArtifactPersistenceRepository) MarkArtifactPromotionDeliveryFailed(ctx context.Context, row db.ArtifactPromotionDelivery, message string) error {
	_, err := r.queries.MarkArtifactPromotionDeliveryFailed(ctx, db.MarkArtifactPromotionDeliveryFailedParams{LastError: pgtype.Text{String: message, Valid: message != ""}, WorkspaceID: row.WorkspaceID, PromotionID: row.PromotionID, PayloadDigest: row.PayloadDigest, DispatchToken: row.DispatchToken})
	return err
}

func (r *ArtifactPersistenceRepository) MarkArtifactPromotionDeliveryDefiniteAbsent(ctx context.Context, row db.ArtifactPromotionDelivery, message string) error {
	_, err := r.queries.MarkArtifactPromotionDeliveryDefiniteAbsent(ctx, db.MarkArtifactPromotionDeliveryDefiniteAbsentParams{LastError: pgtype.Text{String: message, Valid: true}, WorkspaceID: row.WorkspaceID, PromotionID: row.PromotionID, PayloadDigest: row.PayloadDigest, DispatchToken: row.DispatchToken})
	return err
}

func (r *ArtifactPersistenceRepository) MarkArtifactPromotionDeliveryReadbackConfirmed(ctx context.Context, row db.ArtifactPromotionDelivery, readback []byte) error {
	_, err := r.queries.MarkArtifactPromotionDeliveryReadbackConfirmed(ctx, db.MarkArtifactPromotionDeliveryReadbackConfirmedParams{ReadbackReceipt: readback, WorkspaceID: row.WorkspaceID, PromotionID: row.PromotionID, PayloadDigest: row.PayloadDigest})
	return err
}

func (r *ArtifactPersistenceRepository) RecoverArtifactPromotionDeliveryFromReadback(ctx context.Context, row db.ArtifactPromotionDelivery, response, readback []byte) error {
	_, err := r.queries.RecoverArtifactPromotionDeliveryFromReadback(ctx, db.RecoverArtifactPromotionDeliveryFromReadbackParams{ResponseReceipt: response, ReadbackReceipt: readback, WorkspaceID: row.WorkspaceID, PromotionID: row.PromotionID, PayloadDigest: row.PayloadDigest, DispatchToken: row.DispatchToken})
	return err
}
