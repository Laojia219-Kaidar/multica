package service

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func parsePGUUID(s string) (pgtype.UUID, error) { return util.ParseUUID(s) }

// TestProjectLifecyclePortfolioDiagnostic is a guarded diagnostic that prints
// the derived A-G health for the real project portfolio. It is evidence for
// VC-08 (existing-portfolio disposition) and is skipped unless
// PROJECT_LIFECYCLE_SMOKE=1 is set with a reachable DATABASE_URL. It asserts
// nothing about seed data so it stays safe under arbitrary workspaces.
func TestProjectLifecyclePortfolioDiagnostic(t *testing.T) {
	if os.Getenv("PROJECT_LIFECYCLE_SMOKE") != "1" {
		t.Skip("set PROJECT_LIFECYCLE_SMOKE=1 and DATABASE_URL to run the portfolio diagnostic")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var wsID string
	if err := pool.QueryRow(ctx, `SELECT id FROM workspace ORDER BY created_at LIMIT 1`).Scan(&wsID); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	wsUUID, err := parsePGUUID(wsID)
	if err != nil {
		t.Fatalf("parse workspace uuid: %v", err)
	}

	projector := NewProjectLifecycleProjector(db.New(pool))
	snaps, err := projector.ListPortfolio(ctx, wsUUID)
	if err != nil {
		t.Fatalf("ListPortfolio: %v", err)
	}

	t.Logf("portfolio snapshot: %d projects", len(snaps))
	for _, s := range snaps {
		lead := "<nil>"
		if s.LeadID != nil {
			lead = *s.LeadID
		}
		lp := "<nil>"
		if s.LastProgressAt != nil {
			lp = *s.LastProgressAt
		}
		t.Logf("health=%s owner_decision=%v active=%d nonterminal_issues=%d blocked=%d review=%d terminal=%d lead_id=%s last_progress=%s next_action=%q",
			s.Health, s.OwnerDecisionRequired, s.ActiveTaskCount, s.NonterminalIssueCount,
			s.BlockedIssueCount, s.ReviewIssueCount, s.TerminalIssueCount, lead, lp, s.NextAction)
	}
}
