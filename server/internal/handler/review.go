package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// reviewQueueItemResponse is the wire shape of one Review Queue card
// (C11). Internal task/agent UUIDs are intentional here: the queue is a
// backend/API surface; the frontend renders names/short SHAs and must never
// leak cross-workspace ids (browser matrix §显示约定).
type reviewQueueItemResponse struct {
	IssueID            string  `json:"issue_id"`
	Identifier         string  `json:"identifier"`
	Title              string  `json:"title"`
	ReviewState        *string `json:"review_state"`
	ReviewStateReason  *string `json:"review_state_reason,omitempty"`
	ReviewerAgentID    *string `json:"reviewer_agent_id,omitempty"`
	ReviewerName       *string `json:"reviewer_name,omitempty"`
	ReviewTargetTaskID *string `json:"review_target_task_id,omitempty"`
	ReviewTaskStatus   *string `json:"review_task_status,omitempty"`
	IssueUpdatedAt     string  `json:"issue_updated_at"`
}

// reviewVerdictRequest is the verdict write body.
type reviewVerdictRequest struct {
	Verdict            string   `json:"verdict"` // "pass" | "revise"
	Notes              string   `json:"notes"`
	RepairRequirements []string `json:"repair_requirements,omitempty"`
}

// ListReviewQueue returns the open review queue for the current workspace
// (GET /api/issues/review-queue).
func (h *Handler) ListReviewQueue(w http.ResponseWriter, r *http.Request) {
	if h.ReviewPipelineService == nil {
		writeError(w, http.StatusServiceUnavailable, "review pipeline is not enabled")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	rows, err := h.ReviewPipelineService.ListReviewQueue(r.Context(), wsUUID)
	if err != nil {
		slog.Warn("list review queue failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list review queue")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	items := make([]reviewQueueItemResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, reviewQueueItem(row, prefix))
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": items})
}

// WriteReviewVerdict applies a reviewer/coordinator verdict
// (POST /api/issues/{id}/review-verdict).
func (h *Handler) WriteReviewVerdict(w http.ResponseWriter, r *http.Request) {
	if h.ReviewPipelineService == nil {
		writeError(w, http.StatusServiceUnavailable, "review pipeline is not enabled")
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req reviewVerdictRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Verdict != "pass" && req.Verdict != "revise" {
		writeError(w, http.StatusBadRequest, "verdict must be 'pass' or 'revise'")
		return
	}

	actor, ok := h.reviewActor(r, uuidToString(issue.WorkspaceID))
	if !ok {
		writeError(w, http.StatusForbidden, "verdict requires an agent reviewer or a member owner")
		return
	}

	result, err := h.ReviewPipelineService.WriteVerdict(r.Context(), issue.ID, actor, service.VerdictInput{
		Verdict:            req.Verdict,
		Notes:              req.Notes,
		RepairRequirements: req.RepairRequirements,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNoOpenReviewTask):
			writeError(w, http.StatusConflict, "no open review task for this issue")
		case errors.Is(err, service.ErrReviewStateClosed):
			writeError(w, http.StatusConflict, "review state is not open for a verdict")
		case errors.Is(err, service.ErrNotAssignedReviewer),
			errors.Is(err, service.ErrReviewerIsImplementer),
			errors.Is(err, service.ErrNotCoordinator):
			writeError(w, http.StatusForbidden, err.Error())
		default:
			slog.Warn("write review verdict failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to write review verdict")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issue_id":       uuidToString(result.IssueID),
		"review_state":   result.ReviewState,
		"review_task_id": uuidToString(result.ReviewTaskID),
	})
}

// RequeueReview re-runs candidate lineage for an owner_decision issue
// (POST /api/issues/{id}/review-requeue; coordinator agent or member owner).
func (h *Handler) RequeueReview(w http.ResponseWriter, r *http.Request) {
	if h.ReviewPipelineService == nil {
		writeError(w, http.StatusServiceUnavailable, "review pipeline is not enabled")
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	actor, ok := h.reviewActor(r, uuidToString(issue.WorkspaceID))
	if !ok {
		writeError(w, http.StatusForbidden, "requeue requires the coordinator agent or a member owner")
		return
	}
	result, err := h.ReviewPipelineService.Requeue(r.Context(), issue.ID, actor)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNotCoordinator):
			writeError(w, http.StatusForbidden, err.Error())
		default:
			slog.Warn("review requeue failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to requeue review")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issue_id":            uuidToString(issue.ID),
		"review_state":        result.ReviewState,
		"review_task_created": result.ReviewTaskCreated,
		"review_state_reason": nullIfEmpty(result.Reason),
	})
}

// reviewBackfillDryRunRequest is the dry-run body: issue ids plus the
// HIV-319 classification mapping for each.
type reviewBackfillDryRunRequest struct {
	Issues []reviewBackfillEntryRequest `json:"issues"`
}

type reviewBackfillEntryRequest struct {
	IssueID             string `json:"issue_id"`
	IntendedReviewState string `json:"intended_review_state"`
}

type reviewBackfillItemResponse struct {
	IssueID                   string   `json:"issue_id"`
	Number                    int32    `json:"number"`
	Title                     string   `json:"title"`
	CurrentStatus             string   `json:"current_status"`
	CurrentReviewState        *string  `json:"current_review_state"`
	LegacyMetadataReviewState string   `json:"legacy_metadata_review_state"`
	ProposedReviewState       string   `json:"proposed_review_state"`
	LineageValid              bool     `json:"lineage_valid"`
	LineageSubReason          string   `json:"lineage_sub_reason,omitempty"`
	Warnings                  []string `json:"warnings,omitempty"`
}

// ReviewBackfillDryRun computes the migration mapping for the HIV-319
// classification without writing anything (POST /api/review-backfill/dry-run;
// human-only).
func (h *Handler) ReviewBackfillDryRun(w http.ResponseWriter, r *http.Request) {
	if h.ReviewPipelineService == nil {
		writeError(w, http.StatusServiceUnavailable, "review pipeline is not enabled")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var req reviewBackfillDryRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if len(req.Issues) == 0 {
		writeError(w, http.StatusBadRequest, "issues list must not be empty")
		return
	}
	entries := make([]service.BackfillEntry, 0, len(req.Issues))
	for _, item := range req.Issues {
		issueID, err := util.ParseUUID(item.IssueID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid issue_id "+item.IssueID)
			return
		}
		entries = append(entries, service.BackfillEntry{
			IssueID:             issueID,
			IntendedReviewState: item.IntendedReviewState,
		})
	}
	items, summary, err := h.ReviewPipelineService.BackfillDryRun(r.Context(), wsUUID, entries)
	if err != nil {
		slog.Warn("review backfill dry-run failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to compute backfill dry-run")
		return
	}
	response := make([]reviewBackfillItemResponse, 0, len(items))
	for _, item := range items {
		response = append(response, reviewBackfillItemResponse{
			IssueID:                   uuidToString(item.IssueID),
			Number:                    item.Number,
			Title:                     item.Title,
			CurrentStatus:             item.CurrentStatus,
			CurrentReviewState:        textPtrOrNil(item.CurrentReviewState),
			LegacyMetadataReviewState: item.LegacyMetadataReviewState,
			ProposedReviewState:       item.ProposedReviewState,
			LineageValid:              item.LineageValid,
			LineageSubReason:          item.LineageSubReason,
			Warnings:                  item.Warnings,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":   response,
		"summary": summary.ByState,
		"total":   summary.Total,
	})
}

// reviewActor resolves the actor for review writes. Agents (the reviewer or
// coordinator) and members (the human Owner) are the only accepted identities.
func (h *Handler) reviewActor(r *http.Request, workspaceID string) (service.ReviewActor, bool) {
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if actorType != "agent" && actorType != "member" {
		return service.ReviewActor{}, false
	}
	parsed, err := util.ParseUUID(actorID)
	if err != nil {
		return service.ReviewActor{}, false
	}
	return service.ReviewActor{ActorType: actorType, ActorID: parsed}, true
}

func reviewQueueItem(row db.ListReviewQueueRow, prefix string) reviewQueueItemResponse {
	identifier := prefix + "-" + strconv.Itoa(int(row.IssueNumber))
	return reviewQueueItemResponse{
		IssueID:            uuidToString(row.IssueID),
		Identifier:         identifier,
		Title:              row.IssueTitle,
		ReviewState:        textPtrOrNil(row.ReviewState),
		ReviewStateReason:  textPtrOrNil(row.ReviewStateReason),
		ReviewerAgentID:    uuidPtrOrNil(row.ReviewerAgentID),
		ReviewerName:       textPtrOrNil(row.ReviewerName),
		ReviewTargetTaskID: uuidPtrOrNil(row.ReviewTargetTaskID),
		ReviewTaskStatus:   statusPtrOrNil(row.ReviewTaskStatus),
		IssueUpdatedAt:     row.IssueUpdatedAt.Time.Format(time.RFC3339),
	}
}

func textPtrOrNil(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func uuidPtrOrNil(v pgtype.UUID) *string {
	if !v.Valid {
		return nil
	}
	s := uuidToString(v)
	return &s
}

func statusPtrOrNil(v string) *string {
	if v == "" {
		return nil
	}
	s := v
	return &s
}

func nullIfEmpty(v string) *string {
	if v == "" {
		return nil
	}
	s := v
	return &s
}
