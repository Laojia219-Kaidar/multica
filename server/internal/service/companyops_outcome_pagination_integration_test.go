package service

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func sha256Digest(seed byte) string {
	d := make([]byte, 64)
	for i := range d {
		d[i] = '0' + (seed+byte(i))%10
	}
	return "sha256:" + string(d)
}

// newOutcomePaginationPool opens the loopback DATABASE_URL for the Lane E
// isolated test database. It refuses the production port (5432) but accepts any
// other loopback port so the test runs against the lane-owned container
// (127.0.0.1:55438) without colliding with the shared 55432 convention.
func newOutcomePaginationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping outcome pagination integration test")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if parsed.Port() == "5432" {
		t.Skip("refusing to run outcome pagination test against production port 5432")
	}
	if host := parsed.Hostname(); host != "127.0.0.1" && host != "localhost" && host != "::1" {
		t.Skipf("outcome pagination test requires loopback host, got %q", host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open DATABASE_URL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestCompanyOpsOutcomePaginationIntegration(t *testing.T) {
	pool := newOutcomePaginationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	queries := db.New(pool)
	svc := NewCompanyOpsOutcomeCenterService(queries)

	wsUUID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	slug := "lanee-pagination-" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace (id, name, slug) VALUES ($1, $2, $3)`,
		wsUUID, "Lane E Pagination", slug,
	); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM assignment_dispatch_receipt WHERE workspace_id = $1`, wsUUID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspace WHERE id = $1`, wsUUID)
	})

	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	const total = 7
	commandIDs := make([]pgtype.UUID, 0, total)
	for i := 0; i < total; i++ {
		cmd := pgtype.UUID{Bytes: uuid.New(), Valid: true}
		commandIDs = append(commandIDs, cmd)
		createdAt := base.Add(time.Duration(total-i) * time.Minute)
		_, err := pool.Exec(ctx, `
			INSERT INTO assignment_dispatch_receipt (
				command_id, workspace_id, issue_id, local_agent_id, initial_task_id,
				work_order_ref, work_order_revision, work_order_digest,
				input_digest,
				employee_ref, employee_revision, employee_digest,
				binding_ref, binding_revision, binding_digest,
				agent_ref, agent_revision, agent_digest,
				created_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8,
				$9,
				$10, $11, $12,
				$13, $14, $15,
				$16, $17, $18,
				$19
			)`,
			cmd, wsUUID,
			pgtype.UUID{Bytes: uuid.New(), Valid: true},
			pgtype.UUID{Bytes: uuid.New(), Valid: true},
			pgtype.UUID{Bytes: uuid.New(), Valid: true},
			fmt.Sprintf("WO-LANEE-%d", i), fmt.Sprintf("rev-%d", i), sha256Digest(byte(i)),
			sha256Digest(byte(20+i)),
			fmt.Sprintf("hivecosm://employees/EMP-LANEE-%d", i), fmt.Sprintf("emp-rev-%d", i), sha256Digest(byte(40+i)),
			fmt.Sprintf("hivecosm://identity-bindings/BIND-LANEE-%d", i), fmt.Sprintf("bind-rev-%d", i), sha256Digest(byte(60+i)),
			fmt.Sprintf("/api/agents/%s", util.UUIDToString(pgtype.UUID{Bytes: uuid.New(), Valid: true})), fmt.Sprintf("agent-rev-%d", i), sha256Digest(byte(80+i)),
			createdAt,
		)
		if err != nil {
			t.Fatalf("insert assignment_dispatch_receipt %d: %v", i, err)
		}
	}

	req := func() CompanyOpsOutcomeListRequest {
		return CompanyOpsOutcomeListRequest{WorkspaceID: wsUUID}
	}

	// Positive case 1: first page with limit/offset returns the newest window.
	r1 := req()
	r1.Limit = 3
	page1, err := svc.ListOutcomesPage(ctx, r1)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Summaries) != 3 || page1.Total != total || !page1.HasMore || page1.NextCursor == nil {
		t.Fatalf("page1 = len=%d total=%d hasMore=%v nextCursor=%v", len(page1.Summaries), page1.Total, page1.HasMore, page1.NextCursor)
	}

	// Positive case 2: cursor walk continues without overlap.
	r2 := req()
	r2.Limit = 3
	r2.Cursor = *page1.NextCursor
	page2, err := svc.ListOutcomesPage(ctx, r2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Summaries) != 3 {
		t.Fatalf("page2 len = %d", len(page2.Summaries))
	}
	seen := map[string]bool{}
	for _, s := range append(page1.Summaries, page2.Summaries...) {
		if seen[s.ID] {
			t.Fatalf("overlapping outcome id %q across cursor pages", s.ID)
		}
		seen[s.ID] = true
	}

	// Positive case 3: final cursor page has no next cursor.
	r3 := req()
	r3.Limit = 3
	r3.Cursor = *page2.NextCursor
	page3, err := svc.ListOutcomesPage(ctx, r3)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3.Summaries) != 1 || page3.HasMore || page3.NextCursor != nil {
		t.Fatalf("page3 = len=%d hasMore=%v nextCursor=%v", len(page3.Summaries), page3.HasMore, page3.NextCursor)
	}

	// Negative case 1: out-of-bounds offset returns empty, total intact.
	r4 := req()
	r4.Limit = 3
	r4.Offset = 9999
	page4, err := svc.ListOutcomesPage(ctx, r4)
	if err != nil {
		t.Fatalf("page4: %v", err)
	}
	if len(page4.Summaries) != 0 || page4.Total != total || page4.HasMore {
		t.Fatalf("page4 = len=%d total=%d hasMore=%v", len(page4.Summaries), page4.Total, page4.HasMore)
	}

	// Negative case 2: oversized limit is clamped, not rejected.
	r5 := req()
	r5.Limit = 100000
	page5, err := svc.ListOutcomesPage(ctx, r5)
	if err != nil {
		t.Fatalf("page5: %v", err)
	}
	if len(page5.Summaries) != total || page5.HasMore {
		t.Fatalf("page5 = len=%d hasMore=%v", len(page5.Summaries), page5.HasMore)
	}

	// Negative case 3: malformed cursor is rejected with an error.
	r6 := req()
	r6.Cursor = "not-a-valid-cursor"
	if _, err := svc.ListOutcomesPage(ctx, r6); err == nil {
		t.Fatal("expected error for malformed cursor")
	}
}
