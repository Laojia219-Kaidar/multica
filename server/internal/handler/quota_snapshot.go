package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/metrics"
)

const maxQuotaObservationBody = 32 * 1024

var quotaObservationSources = map[string]bool{
	"task_usage": true, "live_vendor": true, "manual_cap": true, "hivecosm": true, "console": true,
}

var quotaObservationWindows = map[string]bool{
	"5h": true, "7d": true, "30d": true, "monthly": true, "daily": true,
	"mcp_monthly": true, "credits": true, "cny_balance": true, "package": true,
	"code_5h": true, "code_7d": true, "session": true, "unlimited": true, "30d_cost": true,
}

var quotaObservationUnits = map[string]bool{
	"tokens": true, "percent": true, "credits": true, "cny": true,
}

type quotaObservationItem struct {
	Provider        string   `json:"provider"`
	Plan            string   `json:"plan"`
	Account         string   `json:"account"`
	APIKeyLabel     string   `json:"api_key_label"`
	WindowKind      string   `json:"window_kind"`
	LimitTokens     *int64   `json:"limit_tokens,omitempty"`
	UsedTokens      int64    `json:"used_tokens"`
	RemainingTokens *int64   `json:"remaining_tokens,omitempty"`
	Percentage      *float64 `json:"percentage,omitempty"`
	Unit            string   `json:"unit"`
	ResetAt         *string  `json:"reset_at,omitempty"`
	ObservedAt      *string  `json:"observed_at,omitempty"`
	Source          string   `json:"source"`
	SourceRef       string   `json:"source_ref"`
}

type quotaObservationRequest struct {
	Observations []quotaObservationItem `json:"observations"`
}

// PostProviderQuotaObservation ingests one or more quota snapshots from a Mac-side
// HiveCosm wrapper or operator tooling. Secrets must not appear in the payload.
func (h *Handler) PostProviderQuotaObservation(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQuotaObservationBody))
	decoder.DisallowUnknownFields()
	var req quotaObservationRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Observations) == 0 {
		writeError(w, http.StatusBadRequest, "observations is required")
		return
	}
	if len(req.Observations) > 64 {
		writeError(w, http.StatusBadRequest, "too many observations")
		return
	}

	service := metrics.NewQuotaSnapshotService(h.DB)
	for i, item := range req.Observations {
		in, err := validateQuotaObservationItem(item)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := service.UpsertSnapshot(r.Context(), workspaceID, in); err != nil {
			slog.Error("failed to upsert quota observation", "error", err, "index", i)
			writeError(w, http.StatusInternalServerError, "failed to save observation")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateQuotaObservationItem(item quotaObservationItem) (metrics.UpsertSnapshotInput, error) {
	item.Provider = strings.TrimSpace(item.Provider)
	item.Plan = strings.TrimSpace(item.Plan)
	item.Account = strings.TrimSpace(item.Account)
	item.APIKeyLabel = strings.TrimSpace(item.APIKeyLabel)
	item.WindowKind = strings.TrimSpace(item.WindowKind)
	item.Source = strings.TrimSpace(item.Source)
	item.SourceRef = strings.TrimSpace(item.SourceRef)

	if item.Provider == "" || item.Plan == "" {
		return metrics.UpsertSnapshotInput{}, errors.New("provider and plan are required")
	}
	if !quotaObservationWindows[item.WindowKind] {
		return metrics.UpsertSnapshotInput{}, errors.New("window_kind is not supported")
	}
	if !quotaObservationSources[item.Source] {
		return metrics.UpsertSnapshotInput{}, errors.New("source must be task_usage, live_vendor, manual_cap, hivecosm, or console")
	}
	item.Unit = strings.TrimSpace(item.Unit)
	if item.Unit == "" {
		item.Unit = "tokens"
	}
	if !quotaObservationUnits[item.Unit] {
		return metrics.UpsertSnapshotInput{}, errors.New("unit must be tokens, percent, credits, or cny")
	}
	if item.Percentage != nil && (*item.Percentage < 0 || *item.Percentage > 100) {
		return metrics.UpsertSnapshotInput{}, errors.New("percentage must be between 0 and 100")
	}
	if containsSecretMaterial(item.APIKeyLabel) || containsSecretMaterial(item.SourceRef) {
		return metrics.UpsertSnapshotInput{}, errors.New("payload must not contain secret material")
	}

	var resetAt *time.Time
	if item.ResetAt != nil && strings.TrimSpace(*item.ResetAt) != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(*item.ResetAt))
		if err != nil {
			return metrics.UpsertSnapshotInput{}, errors.New("reset_at must be RFC3339")
		}
		resetAt = &t
	}
	observedAt := time.Now().UTC()
	if item.ObservedAt != nil && strings.TrimSpace(*item.ObservedAt) != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(*item.ObservedAt))
		if err != nil {
			return metrics.UpsertSnapshotInput{}, errors.New("observed_at must be RFC3339")
		}
		observedAt = t.UTC()
	}

	return metrics.UpsertSnapshotInput{
		Provider:        item.Provider,
		Plan:            item.Plan,
		Account:         item.Account,
		APIKeyLabel:     item.APIKeyLabel,
		WindowKind:      item.WindowKind,
		LimitTokens:     item.LimitTokens,
		UsedTokens:      item.UsedTokens,
		RemainingTokens: item.RemainingTokens,
		Percentage:      item.Percentage,
		Unit:            item.Unit,
		ResetAt:         resetAt,
		ObservedAt:      observedAt,
		Source:          item.Source,
		SourceRef:       item.SourceRef,
	}, nil
}

func containsSecretMaterial(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "bearer ") ||
		strings.Contains(lower, "sk-") ||
		strings.Contains(lower, "api_key=") ||
		len(value) > 80
}

// GetWorkQuota returns a machine-readable quota snapshot for daemons/PAT without
// RequireHumanActor. Never returns secret material.
func (h *Handler) GetWorkQuota(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	now := time.Now().UTC()
	since := metrics.WindowSince(now, "monthly")

	usageSvc := metrics.NewUsageService(h.DB)
	observations, err := usageSvc.ListUsageObservations(r.Context(), workspaceID, since)
	if err != nil {
		slog.Error("failed to list work quota usage", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list usage")
		return
	}
	quotas, err := usageSvc.ListUsageQuota(r.Context(), workspaceID)
	if err != nil {
		slog.Error("failed to list work quota caps", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list quota")
		return
	}
	snapSvc := metrics.NewQuotaSnapshotService(h.DB)
	snapshots, err := snapSvc.ListSnapshots(r.Context(), workspaceID)
	if err != nil {
		slog.Error("failed to list work quota snapshots", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list snapshots")
		return
	}

	writeJSON(w, http.StatusOK, metrics.BuildWorkQuotaSnapshot(workspaceID, observations, quotas, snapshots, now))
}

// PollVendorQuotaIfConfigured fetches live vendor remaining when env keys are set.
func (h *Handler) PollVendorQuotaIfConfigured(ctx context.Context, workspaceID string, cfg metrics.VendorQuotaConfig) error {
	if strings.TrimSpace(cfg.MiniMaxAPIKey) == "" &&
		strings.TrimSpace(cfg.ZhipuAPIKey) == "" &&
		strings.TrimSpace(cfg.VolcengineAK) == "" &&
		strings.TrimSpace(cfg.DeepSeekAPIKey) == "" {
		return nil
	}

	service := metrics.NewQuotaSnapshotService(h.DB)
	upsertVendor := func(obs metrics.VendorQuotaObservation, runtimeName string) error {
		provider, plan, account := metrics.MapVendorObservationToPlan(obs, runtimeName)
		return service.UpsertSnapshot(ctx, workspaceID, metrics.UpsertSnapshotInput{
			Provider:        provider,
			Plan:            plan,
			Account:         account,
			WindowKind:      obs.WindowKind,
			LimitTokens:     obs.Limit,
			UsedTokens:      obs.Used,
			RemainingTokens: obs.Remaining,
			Percentage:      obs.Percentage,
			Unit:            obs.Unit,
			ResetAt:         obs.ResetAt,
			ObservedAt:      obs.ObservedAt,
			Source:          "live_vendor",
			SourceRef:       obs.SourceRef,
		})
	}

	if obs, err := metrics.FetchMiniMaxRemains(ctx, cfg); err == nil {
		for _, row := range obs {
			if err := upsertVendor(row, "secure minimax"); err != nil {
				return err
			}
		}
	} else {
		slog.Debug("minimax quota poll skipped", "error", err)
	}

	if obs, err := metrics.FetchZhipuQuota(ctx, cfg); err == nil {
		for _, row := range obs {
			if err := upsertVendor(row, "secure zhipu"); err != nil {
				return err
			}
		}
	} else {
		slog.Debug("zhipu quota poll skipped", "error", err)
	}

	if obs, err := metrics.FetchVolcengineArkUsage(ctx, cfg); err == nil {
		for _, row := range obs {
			if err := upsertVendor(row, "volcengine-agent"); err != nil {
				return err
			}
		}
	} else {
		slog.Debug("volcengine quota poll skipped", "error", err)
	}

	if obs, err := metrics.FetchDeepSeekBalance(ctx, cfg); err == nil && obs != nil {
		if err := upsertVendor(*obs, "secure deepseek"); err != nil {
			return err
		}
	} else if err != nil {
		slog.Debug("deepseek quota poll skipped", "error", err)
	}
	return nil
}

func vendorQuotaConfigFromEnv() metrics.VendorQuotaConfig {
	return metrics.VendorQuotaConfig{
		MiniMaxAPIKey:  strings.TrimSpace(os.Getenv("QUOTA_POLL_MINIMAX_API_KEY")),
		ZhipuAPIKey:    strings.TrimSpace(os.Getenv("QUOTA_POLL_ZHIPU_API_KEY")),
		VolcengineAK:   strings.TrimSpace(os.Getenv("QUOTA_POLL_VOLCENGINE_AK")),
		VolcengineSK:   strings.TrimSpace(os.Getenv("QUOTA_POLL_VOLCENGINE_SK")),
		DeepSeekAPIKey: strings.TrimSpace(os.Getenv("QUOTA_POLL_DEEPSEEK_API_KEY")),
	}
}
