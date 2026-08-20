package main

import (
	"testing"

	"github.com/google/uuid"
)

func TestReviewCellConfigFromEnvIsAlwaysAuthorityOnly(t *testing.T) {
	t.Setenv("REVIEW_CELL_ENABLED", "true")
	cfg := reviewCellConfigFromEnv()
	if !cfg.AuthorityDispatchOnly {
		t.Fatal("review-cell production config must be Authority-only")
	}
}

func TestReviewCellEnabledFailsClosedByDefaultAndOnDrift(t *testing.T) {
	for _, value := range []string{"", "TRUE", "1", " true", "true "} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("REVIEW_CELL_ENABLED", value)
			if reviewCellEnabledFromEnv() {
				t.Fatalf("REVIEW_CELL_ENABLED=%q must fail closed", value)
			}
		})
	}
}

func TestReviewCellStagingWiringAcceptsExactReviewer(t *testing.T) {
	const reviewer = "708a49ba-7e7d-4363-8b9b-e6a4aeb5d980"
	t.Setenv("REVIEW_CELL_ENABLED", "true")
	t.Setenv("REVIEW_CELL_L1_AGENT_ID", reviewer)
	t.Setenv("REVIEW_CELL_COORDINATOR_AGENT_ID", "")
	t.Setenv("REVIEW_CELL_REVIEW_WIP_LIMIT", "1")

	cfg := reviewCellConfigFromEnv()
	if !cfg.Enabled || !cfg.ReviewerAgentIDSet {
		t.Fatalf("exact staging wiring not enabled: %+v", cfg)
	}
	if got := uuid.UUID(cfg.ReviewerAgentID.Bytes).String(); got != reviewer {
		t.Fatalf("reviewer = %s, want %s", got, reviewer)
	}
	if cfg.CoordinatorAgentSet {
		t.Fatal("missing coordinator must retain member-owner PASS boundary")
	}
	if cfg.ReviewWIPLimit != 1 || !cfg.AuthorityDispatchOnly {
		t.Fatalf("unexpected staging limits/boundary: %+v", cfg)
	}
}
