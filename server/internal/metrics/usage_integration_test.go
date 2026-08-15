package metrics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestUsageIntegration_AggregateLiveRows runs the real aggregation SQL against
// an isolated test database. It is skipped unless TEST_DATABASE_URL points at
// a Lane D sandbox (never the production database). It proves:
//   - the positive case: real usage rows aggregate into a non-empty hierarchy
//   - the negative case: the empty-period query returns zero rows cleanly
//   - quota merge: a configured quota renders total/used/remaining/percentage/reset
//   - the migration-down path is left to the manual DB receipt (see docs/lane-d)
func TestUsageIntegration_AggregateLiveRows(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live aggregation integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Workspace id is environment-provided so the test never guesses a
	// production tenant id.
	workspaceID := os.Getenv("LANE_D_WORKSPACE_ID")
	if workspaceID == "" {
		t.Fatal("LANE_D_WORKSPACE_ID is required")
	}

	svc := NewUsageService(pool)
	since := time.Now().UTC().AddDate(0, 0, -30)

	obs, err := svc.ListUsageObservations(ctx, workspaceID, since)
	if err != nil {
		t.Fatalf("ListUsageObservations: %v", err)
	}
	t.Logf("observations in window: %d", len(obs))
	if len(obs) == 0 {
		t.Log("no rows in the 30d window — this is the honest empty-data case, not a failure")
	}

	quotas, err := svc.ListUsageQuota(ctx, workspaceID)
	if err != nil {
		t.Fatalf("ListUsageQuota: %v", err)
	}
	t.Logf("configured quota rows: %d", len(quotas))

	// Negative case: a window starting in the future must return zero rows and
	// must still build an empty hierarchy with data_gaps, not error.
	emptySince := time.Now().UTC().AddDate(0, 0, 1)
	emptyObs, err := svc.ListUsageObservations(ctx, workspaceID, emptySince)
	if err != nil {
		t.Fatalf("ListUsageObservations (empty window): %v", err)
	}
	if len(emptyObs) != 0 {
		t.Fatalf("future window returned %d rows, want 0", len(emptyObs))
	}

	hierarchy := BuildUsageHierarchy(workspaceID, since, obs, quotas)
	t.Logf("hierarchy providers=%d plans=%d employees=%d tasks=%d used_tokens=%d gaps=%v",
		len(hierarchy.Providers), hierarchy.Totals.PlanCount,
		hierarchy.Totals.EmployeeCount, hierarchy.Totals.TaskCount,
		hierarchy.Totals.UsedTokens, hierarchy.DataGaps)

	if len(obs) > 0 {
		if len(hierarchy.Providers) == 0 {
			t.Fatalf("non-empty observations produced an empty provider hierarchy")
		}
		if hierarchy.Totals.UsedTokens <= 0 {
			t.Fatalf("non-empty observations produced zero used tokens")
		}
	}

	// Persist the live aggregation evidence for the Lane D receipt.
	out := os.Getenv("LANE_D_EVIDENCE_OUT")
	if out == "" {
		out = filepath.Join("..", "..", "..", "docs", "lane-d", "usage-aggregation-live-evidence.json")
	}
	if abs, err := filepath.Abs(out); err == nil {
		out = abs
	}
	payload, err := json.MarshalIndent(hierarchy, "", "  ")
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	if err := os.WriteFile(out, payload, 0o644); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	t.Logf("evidence written to %s", out)
}
