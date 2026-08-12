package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

// reviewPipelineV2Enabled is the single source of truth for the
// REVIEW_PIPELINE_V2 feature switch inside cmd/server. When off, no review
// listener is registered, no review routes are wired, and every terminal-status
// interpretation keeps its legacy behavior (HIV-326 §3 "功能开关").
var reviewPipelineV2Enabled = os.Getenv("REVIEW_PIPELINE_V2") == "true"

// reviewPipelineConfigFromEnv builds the review pipeline wiring from
// environment. The L1 reviewer and coordinator agent ids are workspace-local
// agent UUIDs; an invalid or missing id keeps the pipeline fail-closed at
// review-task creation time (the issue escalates to owner_decision instead of
// silently queuing with no claimable reviewer).
func reviewPipelineConfigFromEnv() service.ReviewPipelineConfig {
	cfg := service.ReviewPipelineConfig{
		Enabled:        reviewPipelineV2Enabled,
		ReviewWIPLimit: int32(envPositiveInt("REVIEW_PIPELINE_V2_REVIEW_WIP_LIMIT", 10)),
		ReviewPriority: int32(envPositiveInt("REVIEW_PIPELINE_V2_REVIEW_PRIORITY", 5)),
	}
	if raw := strings.TrimSpace(os.Getenv("REVIEW_PIPELINE_V2_L1_AGENT_ID")); raw != "" {
		if parsed, err := uuid.Parse(raw); err == nil {
			cfg.ReviewerAgentID = pgtype.UUID{Bytes: parsed, Valid: true}
			cfg.ReviewerAgentIDSet = true
		} else {
			slog.Warn("invalid REVIEW_PIPELINE_V2_L1_AGENT_ID, review pipeline will fail closed",
				"value", raw, "error", err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("REVIEW_PIPELINE_V2_COORDINATOR_AGENT_ID")); raw != "" {
		if parsed, err := uuid.Parse(raw); err == nil {
			cfg.CoordinatorAgentID = pgtype.UUID{Bytes: parsed, Valid: true}
			cfg.CoordinatorAgentSet = true
		} else {
			slog.Warn("invalid REVIEW_PIPELINE_V2_COORDINATOR_AGENT_ID", "value", raw, "error", err)
		}
	}
	return cfg
}
