package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/util"
)

// TestOwnerToOutcomeMinimalSlice runs the FULL production Owner->Outcome
// vertical slice against a real DB + local storage (external HiveCosm authority
// stubbed, as in the existing artifact tests):
//
//	assignment dispatch -> execution claim -> task complete -> materialize
//	candidate -> Owner approve -> formal promotion
//
// It asserts every ledger table in the VC-06 lineage gains a row, proving the
// Project -> Issue -> Task/Run -> Candidate -> Review -> Formal ref chain is
// queryable end to end.
func TestOwnerToOutcomeMinimalSlice(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)

	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	promotion, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID,
		companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString()))
	if err != nil {
		t.Fatalf("PromoteArtifact: %v", err)
	}

	// Ledger assertions (the VC-06 lineage).
	assertLedgerCount(t, ctx, fixture, `SELECT count(*) FROM assignment_dispatch_receipt WHERE command_id = $1`, fixture.assignment.CommandID, "assignment_dispatch_receipt")
	assertLedgerCount(t, ctx, fixture, `SELECT count(*) FROM execution_receipt WHERE task_id = $1`, outcome.CurrentTaskID, "execution_receipt")
	assertLedgerCount(t, ctx, fixture, `SELECT count(*) FROM artifact_candidate WHERE lineage_id = $1`, util.MustParseUUID(outcome.Candidate.LineageID), "artifact_candidate")
	assertLedgerCount(t, ctx, fixture, `SELECT count(*) FROM artifact_event WHERE lineage_id = $1 AND event_type = 'approved'`, util.MustParseUUID(outcome.Candidate.LineageID), "artifact_event(approved)")
	assertLedgerCount(t, ctx, fixture, `SELECT count(*) FROM artifact_promotion_claim WHERE promotion_id = $1`, util.MustParseUUID(promotion.PromotionID), "artifact_promotion_claim")

	t.Logf("OWNER->OUTCOME SLICE OK: issue=%s task=%s candidate=%s formal=%s",
		util.UUIDToString(fixture.company.issueID),
		util.UUIDToString(outcome.CurrentTaskID),
		outcome.Candidate.ID,
		promotion.FormalArtifactRef,
	)
}

func assertLedgerCount(t *testing.T, ctx context.Context, fixture companyOpsExecutionTestFixture, query string, arg any, label string) {
	t.Helper()
	var n int
	if err := fixture.pool.QueryRow(ctx, query, arg).Scan(&n); err != nil {
		t.Fatalf("%s count: %v", label, err)
	}
	if n == 0 {
		t.Fatalf("%s has 0 rows, want >= 1", label)
	}
	t.Logf("%s rows = %d", label, n)
}
