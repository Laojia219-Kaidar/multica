package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const clientUsageBodyLimit = 16 * 1024

var (
	providerNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	clientVersionPattern = regexp.MustCompile(`^[\x20-\x7e]{1,64}$`)
)

type clientUsageRequest struct {
	InstallID string                   `json:"install_id"`
	Runtime   *clientUsageRuntimeProbe `json:"runtime,omitempty"`
}

type clientUsageRuntimeProbe struct {
	ProbeResult     string         `json:"probe_result"`
	RuntimeCount    *int32         `json:"runtime_count,omitempty"`
	ProviderSummary map[string]int `json:"provider_summary,omitempty"`
	OnlineCount     *int32         `json:"online_count,omitempty"`
	OfflineCount    *int32         `json:"offline_count,omitempty"`
}

type validatedRuntimeProbe struct {
	Result          pgtype.Text
	RuntimeCount    pgtype.Int4
	ProviderSummary []byte
	OnlineCount     pgtype.Int4
	OfflineCount    pgtype.Int4
}

// UpsertClientUsage records at most one row per user, client installation, and
// UTC day. Repeated reports refresh the same row; an activity-only report never
// clears a runtime snapshot already collected earlier that day.
func (h *Handler) UpsertClientUsage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, clientUsageBodyLimit))
	decoder.DisallowUnknownFields()
	var req clientUsageRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	installID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.InstallID), "install_id")
	if !ok {
		return
	}

	clientType, clientVersion, clientOS := middleware.ClientMetadataFromContext(r.Context())
	clientType = strings.ToLower(strings.TrimSpace(clientType))
	if clientType != "web" && clientType != "desktop" {
		writeError(w, http.StatusBadRequest, "client platform must be web or desktop")
		return
	}
	clientVersion = strings.TrimSpace(clientVersion)
	if clientVersion == "" {
		clientVersion = "unknown"
	}
	if !clientVersionPattern.MatchString(clientVersion) {
		writeError(w, http.StatusBadRequest, "invalid client version")
		return
	}
	clientOS = normalizeClientUsageOS(clientOS)

	var runtime validatedRuntimeProbe
	if req.Runtime != nil {
		if clientType != "desktop" {
			writeError(w, http.StatusBadRequest, "runtime data is only accepted from desktop")
			return
		}
		var err error
		runtime, err = validateClientUsageRuntime(*req.Runtime)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	workspaceUUID := pgtype.UUID{}
	queries := h.Queries
	var tx pgx.Tx
	if workspaceID := h.resolveWorkspaceID(r); workspaceID != "" {
		workspaceUUID, ok = parseUUIDOrBadRequest(w, workspaceID, "workspace id")
		if !ok {
			return
		}
		var err error
		tx, err = h.TxStarter.Begin(r.Context())
		if err != nil {
			slog.Error("failed to begin client usage transaction", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to record client usage")
			return
		}
		defer tx.Rollback(r.Context())
		queries = h.Queries.WithTx(tx)
		// Share the explicit workspace delete/create lock protocol: if deletion
		// wins, the row is gone and this report fails; if reporting wins, deletion
		// waits and clears the context after the upsert commits.
		if _, err := queries.LockWorkspaceForChatSessionCreate(r.Context(), workspaceUUID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusForbidden, "workspace not found")
				return
			}
			slog.Error("failed to lock client usage workspace", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to record client usage")
			return
		}
		if _, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID: userUUID, WorkspaceID: workspaceUUID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusForbidden, "workspace not found")
				return
			}
			slog.Error("failed to validate client usage workspace", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to record client usage")
			return
		}
	}

	if _, err := queries.UpsertClientUsageDaily(r.Context(), db.UpsertClientUsageDailyParams{
		UserID:          userUUID,
		ClientType:      clientType,
		InstallID:       installID,
		WorkspaceID:     workspaceUUID,
		ClientVersion:   clientVersion,
		Os:              clientOS,
		HasRuntimeProbe: req.Runtime != nil,
		ProbeResult:     runtime.Result,
		RuntimeCount:    runtime.RuntimeCount,
		ProviderSummary: runtime.ProviderSummary,
		OnlineCount:     runtime.OnlineCount,
		OfflineCount:    runtime.OfflineCount,
	}); err != nil {
		slog.Error("failed to upsert client usage", "error", err, "client_type", clientType)
		writeError(w, http.StatusInternalServerError, "failed to record client usage")
		return
	}
	if tx != nil {
		if err := tx.Commit(r.Context()); err != nil {
			slog.Error("failed to commit client usage", "error", err, "client_type", clientType)
			writeError(w, http.StatusInternalServerError, "failed to record client usage")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func normalizeClientUsageOS(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "macos", "windows", "linux", "ios", "android", "chromeos":
		return value
	default:
		return "unknown"
	}
}

func validateClientUsageRuntime(probe clientUsageRuntimeProbe) (validatedRuntimeProbe, error) {
	result := strings.ToLower(strings.TrimSpace(probe.ProbeResult))
	if result != "success" && result != "error" {
		return validatedRuntimeProbe{}, errors.New("runtime probe_result must be success or error")
	}
	validated := validatedRuntimeProbe{Result: pgtype.Text{String: result, Valid: true}}
	if result == "error" {
		if probe.RuntimeCount != nil || probe.ProviderSummary != nil || probe.OnlineCount != nil || probe.OfflineCount != nil {
			return validatedRuntimeProbe{}, errors.New("failed runtime probes must not include counts")
		}
		return validated, nil
	}

	if probe.RuntimeCount == nil || probe.ProviderSummary == nil || probe.OnlineCount == nil || probe.OfflineCount == nil {
		return validatedRuntimeProbe{}, errors.New("successful runtime probes require all counts")
	}
	if *probe.RuntimeCount < 0 || *probe.RuntimeCount > 1000 || *probe.OnlineCount < 0 || *probe.OfflineCount < 0 || *probe.OnlineCount+*probe.OfflineCount != *probe.RuntimeCount {
		return validatedRuntimeProbe{}, errors.New("invalid runtime counts")
	}
	if len(probe.ProviderSummary) > 32 {
		return validatedRuntimeProbe{}, errors.New("too many runtime providers")
	}
	var providerTotal int64
	for provider, count := range probe.ProviderSummary {
		if !providerNamePattern.MatchString(provider) || count < 0 || count > 1000 {
			return validatedRuntimeProbe{}, errors.New("invalid runtime provider summary")
		}
		providerTotal += int64(count)
	}
	if providerTotal != int64(*probe.RuntimeCount) {
		return validatedRuntimeProbe{}, errors.New("runtime provider counts do not match runtime_count")
	}
	summary, err := json.Marshal(probe.ProviderSummary)
	if err != nil {
		return validatedRuntimeProbe{}, errors.New("invalid runtime provider summary")
	}
	validated.RuntimeCount = pgtype.Int4{Int32: *probe.RuntimeCount, Valid: true}
	validated.ProviderSummary = summary
	validated.OnlineCount = pgtype.Int4{Int32: *probe.OnlineCount, Valid: true}
	validated.OfflineCount = pgtype.Int4{Int32: *probe.OfflineCount, Valid: true}
	return validated, nil
}

// ---------------------------------------------------------------------------
// Lane D (P3 usage aggregation + capacity routing) — provider/plan usage page.
//
// GET /api/company-ops/usage returns the provider -> plan/account/API-key label
// -> model -> employee -> task hierarchy with configured quota merged in
// (cycle / total / used / remaining / percentage / reset time). Local models
// are aggregated from the same task_usage rows (runtime_mode = 'local').
//
// PUT /api/company-ops/usage/quota upserts one provider/plan quota row. It is
// the operator-configured half of the page; the table ships empty so a plan
// without a configured quota renders as "配额未配置" instead of an invented
// total.
// ---------------------------------------------------------------------------

const maxProviderUsageQuotaBody = 8 * 1024

var providerUsageQuotaCycles = map[string]bool{
	"daily": true, "weekly": true, "monthly": true, "never": true,
}

type providerUsageQuotaRequest struct {
	Provider    string `json:"provider"`
	Plan        string `json:"plan"`
	Account     string `json:"account"`
	APIKeyLabel string `json:"api_key_label"`
	Cycle       string `json:"cycle"`
	TotalTokens int64  `json:"total_tokens"`
	ResetDay    *int   `json:"reset_day,omitempty"`
	LocalModel  bool   `json:"local_model"`
}

// GetProviderPlanUsage returns the workspace usage hierarchy for the requested
// period (?days=N, default 30, capped at 365).
func (h *Handler) GetProviderPlanUsage(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}
	since := metrics.SinceBound(days)

	service := metrics.NewUsageService(h.DB)
	observations, err := service.ListUsageObservations(r.Context(), workspaceID, since)
	if err != nil {
		slog.Error("failed to list provider plan usage", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list usage")
		return
	}
	quotas, err := service.ListUsageQuota(r.Context(), workspaceID)
	if err != nil {
		slog.Error("failed to list provider plan quota", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list quota")
		return
	}

	writeJSON(w, http.StatusOK, metrics.BuildUsageHierarchy(workspaceID, since, observations, quotas))
}

// PutProviderUsageQuota upserts one operator-configured provider/plan quota.
// It is an exact (workspace, provider, plan, account, api-key label) key.
func (h *Handler) PutProviderUsageQuota(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProviderUsageQuotaBody))
	decoder.DisallowUnknownFields()
	var req providerUsageQuotaRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Provider = strings.TrimSpace(req.Provider)
	req.Plan = strings.TrimSpace(req.Plan)
	req.Account = strings.TrimSpace(req.Account)
	req.APIKeyLabel = strings.TrimSpace(req.APIKeyLabel)
	req.Cycle = strings.TrimSpace(req.Cycle)
	if req.Provider == "" || len(req.Provider) > 128 {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}
	if req.Plan == "" || len(req.Plan) > 128 {
		writeError(w, http.StatusBadRequest, "plan is required")
		return
	}
	if len(req.Account) > 256 || len(req.APIKeyLabel) > 256 {
		writeError(w, http.StatusBadRequest, "account or api key label is too large")
		return
	}
	if !providerUsageQuotaCycles[req.Cycle] {
		writeError(w, http.StatusBadRequest, "cycle must be daily, weekly, monthly, or never")
		return
	}
	if req.TotalTokens < 0 {
		writeError(w, http.StatusBadRequest, "total_tokens must be >= 0")
		return
	}
	if req.ResetDay != nil && (*req.ResetDay < 1 || *req.ResetDay > 28) {
		writeError(w, http.StatusBadRequest, "reset_day must be between 1 and 28")
		return
	}
	if req.Cycle == "daily" || req.Cycle == "weekly" {
		req.ResetDay = nil
	}

	var resetDay pgtype.Int4
	if req.ResetDay != nil {
		resetDay = pgtype.Int4{Int32: int32(*req.ResetDay), Valid: true}
	}
	if _, err := h.DB.Exec(r.Context(), `
INSERT INTO provider_usage_quota (
    workspace_id, provider, plan, account_label, api_key_label,
    cycle, total_tokens, reset_day, local_model
) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (workspace_id, provider, plan, account_label, api_key_label)
DO UPDATE SET
    cycle = EXCLUDED.cycle,
    total_tokens = EXCLUDED.total_tokens,
    reset_day = EXCLUDED.reset_day,
    local_model = EXCLUDED.local_model,
    updated_at = now()
`,
		workspaceID,
		req.Provider,
		req.Plan,
		req.Account,
		req.APIKeyLabel,
		req.Cycle,
		req.TotalTokens,
		resetDay,
		req.LocalModel,
	); err != nil {
		slog.Error("failed to upsert provider usage quota", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save quota")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
