package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/reviewcellconfig"
	"github.com/multica-ai/multica/server/internal/service"
)

// reviewCellEnabledFromEnv is the single source of truth for the Lane B
// review-cell feature switch inside cmd/server. Only the exact value "true"
// enables it; missing, malformed, or case-drifted values fail closed.
func reviewCellEnabledFromEnv() bool {
	return reviewcellconfig.Enabled(os.Getenv("REVIEW_CELL_ENABLED"))
}

// reviewCellConfigFromEnv builds the review-cell wiring from environment. The
// L1 reviewer and coordinator agent ids are workspace-local agent UUIDs; an
// invalid or missing reviewer id keeps auto-selection (least-loaded workspace
// reviewer != implementer) as the fallback, and a missing coordinator keeps
// PASS restricted to member owners. An invalid coordinator id fails closed at
// verdict time. Review-cell production wiring is always Authority-only; the
// service-level tests explicitly set AuthorityDispatchOnly=false to exercise
// the legacy local-task behavior.
func reviewCellConfigFromEnv() service.ReviewCellConfig {
	cfg := service.ReviewCellConfig{
		Enabled:               reviewCellEnabledFromEnv(),
		AuthorityDispatchOnly: true,
		ReviewWIPLimit:        int32(envPositiveInt("REVIEW_CELL_REVIEW_WIP_LIMIT", 10)),
		ReviewPriority:        int32(envPositiveInt("REVIEW_CELL_REVIEW_PRIORITY", 5)),
		RepairPriority:        int32(envPositiveInt("REVIEW_CELL_REPAIR_PRIORITY", 5)),
	}
	if raw := strings.TrimSpace(os.Getenv("REVIEW_CELL_L1_AGENT_ID")); raw != "" {
		if parsed, err := uuid.Parse(raw); err == nil {
			cfg.ReviewerAgentID = pgtype.UUID{Bytes: parsed, Valid: true}
			cfg.ReviewerAgentIDSet = true
		} else {
			slog.Warn("invalid REVIEW_CELL_L1_AGENT_ID; review cell will auto-select a reviewer",
				"value", raw, "error", err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("REVIEW_CELL_COORDINATOR_AGENT_ID")); raw != "" {
		if parsed, err := uuid.Parse(raw); err == nil {
			cfg.CoordinatorAgentID = pgtype.UUID{Bytes: parsed, Valid: true}
			cfg.CoordinatorAgentSet = true
		} else {
			slog.Warn("invalid REVIEW_CELL_COORDINATOR_AGENT_ID; PASS will be member-owner only",
				"value", raw, "error", err)
		}
	}
	return cfg
}
