package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

const companyOpsArtifactReplicaLocationsSchemaVersion = "hivecrew.artifact-replica-locations.v1"

type companyOpsArtifactReplicaLocationResponse struct {
	ID                string `json:"id"`
	OutcomeID         string `json:"outcome_id"`
	CandidateID       string `json:"candidate_id"`
	CandidateRevision int    `json:"candidate_revision"`
	LocationClass     string `json:"location_class"`
	LocationID        string `json:"location_id"`
	StorageID         string `json:"storage_id"`
	ObjectRef         string `json:"object_ref"`
	RetentionHint     string `json:"retention_hint,omitempty"`
	State             string `json:"state"`
	Digest            string `json:"digest"`
	MetadataDigest    string `json:"metadata_digest"`
	SizeBytes         int64  `json:"size_bytes"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type companyOpsArtifactReplicaLocationsResponse struct {
	SchemaVersion string                                      `json:"schema_version"`
	WorkspaceID   string                                      `json:"workspace_id"`
	OutcomeID     string                                      `json:"outcome_id"`
	Items         []companyOpsArtifactReplicaLocationResponse `json:"items"`
}

// GetCompanyOpsArtifactReplicaLocations exposes storage placement observations
// for an existing Outcome. The Outcome Center remains the authority for
// existence, lifecycle and review; this ledger is strictly read-only here.
func (h *Handler) GetCompanyOpsArtifactReplicaLocations(w http.ResponseWriter, r *http.Request) {
	if h.CompanyOpsOutcomeCenter == nil {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "source_gap", "CompanyOps outcome center is not configured")
		return
	}
	workspaceID, err := util.ParseUUID(ctxWorkspaceID(r.Context()))
	if err != nil || !workspaceID.Valid {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "workspace_id is required")
		return
	}
	outcomeIDString := chi.URLParam(r, "commandId")
	outcomeID, err := util.ParseUUID(outcomeIDString)
	if err != nil || util.UUIDToString(outcomeID) != outcomeIDString {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "command_id must be a canonical UUID")
		return
	}

	// Validate the canonical Outcome first. A missing ledger row is an empty
	// location list only after this authority read succeeds.
	if _, err := h.CompanyOpsOutcomeCenter.GetOutcome(r.Context(), workspaceID, outcomeID); err != nil {
		if errors.Is(err, service.ErrCompanyOpsOutcomeNotFound) {
			writeCompanyOpsError(w, http.StatusNotFound, "not_found", "outcome not found in this workspace")
			return
		}
		writeCompanyOpsOutcomeServiceError(w, err)
		return
	}
	if h.TxStarter == nil {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "source_gap", "artifact location ledger is not configured")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "source_gap", "artifact location ledger is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	locations, err := companyops.NewArtifactReplicaLocationRepository(tx).ListByOutcome(
		r.Context(), util.UUIDToString(workspaceID), outcomeIDString,
	)
	if err != nil {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "source_gap", "artifact location ledger is unavailable")
		return
	}
	items := make([]companyOpsArtifactReplicaLocationResponse, 0, len(locations))
	for _, location := range locations {
		items = append(items, companyOpsArtifactReplicaLocationResponse{
			ID:                location.ID,
			OutcomeID:         location.OutcomeID,
			CandidateID:       location.CandidateID,
			CandidateRevision: location.CandidateRevision,
			LocationClass:     string(location.Location.Class),
			LocationID:        location.Location.ID,
			StorageID:         location.Location.StorageID,
			ObjectRef:         location.Location.ObjectRef,
			RetentionHint:     location.Location.RetentionHint,
			State:             string(location.State),
			Digest:            location.Digest,
			MetadataDigest:    location.MetadataDigest,
			SizeBytes:         location.SizeBytes,
			CreatedAt:         location.CreatedAt,
			UpdatedAt:         location.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, companyOpsArtifactReplicaLocationsResponse{
		SchemaVersion: companyOpsArtifactReplicaLocationsSchemaVersion,
		WorkspaceID:   util.UUIDToString(workspaceID),
		OutcomeID:     outcomeIDString,
		Items:         items,
	})
}
