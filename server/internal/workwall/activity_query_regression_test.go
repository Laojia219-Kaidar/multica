//go:build integration

package workwall

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestActivityQueryOrdering_Regression_HIV981 verifies that
// ListRecentActivitiesForIssue returns the newest 5 rows in DESC order and
// ListActivitiesForIssue returns rows in chronological ASC order, even when
// multiple rows share the same created_at (tie-break by id DESC / id ASC).
//
// Safety: uses TEST_DATABASE_URL only, refuses non-loopback hosts and port
// 5432. All seed/query runs inside a single transaction that is always
// rolled back so no rows persist regardless of pass/fail.
func TestActivityQueryOrdering_Regression_HIV981(t *testing.T) {
	rawURL := os.Getenv("TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	host := u.Hostname()
	if host == "" {
		t.Fatalf("TEST_DATABASE_URL has no host")
	}

	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			t.Fatalf("TEST_DATABASE_URL host %s is not loopback", host)
		}
	} else if host != "localhost" {
		t.Fatalf("TEST_DATABASE_URL host %s is not loopback", host)
	}

	if u.Port() == "5432" {
		t.Fatalf("TEST_DATABASE_URL must not use port 5432 (refusing production port)")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	slug := fmt.Sprintf("hiv981r2-%d", time.Now().UnixNano())
	var wsID, issueID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id::text`,
		slug, slug).Scan(&wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO issue (workspace_id, title, creator_type, creator_id)
		 VALUES ($1, 'HIV-981 regression', 'member', $1) RETURNING id::text`,
		wsID).Scan(&issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	var wsUUID, issueUUID pgtype.UUID
	if err := wsUUID.Scan(wsID); err != nil {
		t.Fatalf("parse ws uuid: %v", err)
	}
	if err := issueUUID.Scan(issueID); err != nil {
		t.Fatalf("parse issue uuid: %v", err)
	}

	// Insert 7 activity rows with explicit UUIDs and controlled timestamps.
	// Explicit UUIDs with known byte ordering eliminate any dependence on
	// gen_random_uuid() insertion order.
	//
	//  label | explicit id (last byte) | created_at | note
	//  ------+-------------------------+------------+------
	//  r1    | ...01                   |   T+0      | oldest
	//  r2    | ...02                   |   T+1      |
	//  r3    | ...03                   |   T+2      | \
	//  r4    | ...04                   |   T+2      |  > tied group
	//  r5    | ...05                   |   T+2      | /
	//  r6    | ...06                   |   T+3      |
	//  r7    | ...07                   |   T+4      | newest
	//
	// Total order by (created_at ASC, id ASC): r1 r2 r3 r4 r5 r6 r7
	// Total order by (created_at DESC, id DESC): r7 r6 r5 r4 r3 r2 r1
	//
	// ListRecentActivitiesForIssue LIMIT 5 → {r7, r6, r5, r4, r3}
	// ListActivitiesForIssue    LIMIT 5 → {r1, r2, r3, r4, r5}

	baseTime := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)

	type row struct {
		label     string
		explicitID string
		action    string
		createdAt time.Time
	}
	rows := []row{
		{"r1", "00000000-0000-0000-0000-000000000001", "oldest", baseTime},
		{"r2", "00000000-0000-0000-0000-000000000002", "r2", baseTime.Add(1 * time.Hour)},
		{"r3", "00000000-0000-0000-0000-000000000003", "r3", baseTime.Add(2 * time.Hour)},
		{"r4", "00000000-0000-0000-0000-000000000004", "r4", baseTime.Add(2 * time.Hour)},
		{"r5", "00000000-0000-0000-0000-000000000005", "r5", baseTime.Add(2 * time.Hour)},
		{"r6", "00000000-0000-0000-0000-000000000006", "r6", baseTime.Add(3 * time.Hour)},
		{"r7", "00000000-0000-0000-0000-000000000007", "newest", baseTime.Add(4 * time.Hour)},
	}

	var insertedIDs []pgtype.UUID
	for _, r := range rows {
		var idStr string
		if err := tx.QueryRow(ctx,
			`INSERT INTO activity_log (id, workspace_id, issue_id, actor_type, actor_id, action, details, created_at)
			 VALUES ($1, $2, $3, 'member', $2, $4, '{}', $5) RETURNING id::text`,
			r.explicitID, wsID, issueID, r.action, r.createdAt).Scan(&idStr); err != nil {
			t.Fatalf("insert %s: %v", r.label, err)
		}
		var id pgtype.UUID
		if err := id.Scan(idStr); err != nil {
			t.Fatalf("parse %s id: %v", r.label, err)
		}
		insertedIDs = append(insertedIDs, id)
	}

	q := db.New(tx)

	// --- ListRecentActivitiesForIssue: newest-first, LIMIT 5 ---
	recent, err := q.ListRecentActivitiesForIssue(ctx, db.ListRecentActivitiesForIssueParams{
		IssueID: issueUUID,
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("ListRecentActivitiesForIssue: %v", err)
	}
	if len(recent) != 5 {
		t.Fatalf("ListRecentActivitiesForIssue: got %d rows, want 5", len(recent))
	}

	// Expected exact set: {r7, r6, r5, r4, r3} at indices [6,5,4,3,2].
	expectedRecent := []int{6, 5, 4, 3, 2}
	for i, idx := range expectedRecent {
		if recent[i].ID != insertedIDs[idx] {
			t.Errorf("ListRecentActivitiesForIssue[%d]: got id %v, want %v (%s)",
				i, recent[i].ID, insertedIDs[idx], rows[idx].label)
		}
	}

	// Verify total order: (created_at DESC, id DESC).
	for i := 1; i < len(recent); i++ {
		prev, curr := recent[i-1], recent[i]
		if prev.CreatedAt.Time.Before(curr.CreatedAt.Time) {
			t.Errorf("recent not DESC at [%d]: created_at %v < %v",
				i, prev.CreatedAt.Time, curr.CreatedAt.Time)
		}
		if prev.CreatedAt.Time.Equal(curr.CreatedAt.Time) {
			prevBytes := prev.ID.Bytes
			currBytes := curr.ID.Bytes
			if cmp := compareUUIDBytes(prevBytes[:], currBytes[:]); cmp <= 0 {
				t.Errorf("recent tie-break not id DESC at [%d]: prev id %v <= curr id %v",
					i, prev.ID, curr.ID)
			}
		}
	}

	// --- ListActivitiesForIssue: chronological ASC, LIMIT 5 ---
	chrono, err := q.ListActivitiesForIssue(ctx, db.ListActivitiesForIssueParams{
		IssueID: issueUUID,
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("ListActivitiesForIssue: %v", err)
	}
	if len(chrono) != 5 {
		t.Fatalf("ListActivitiesForIssue: got %d rows, want 5", len(chrono))
	}

	// Expected exact set: {r1, r2, r3, r4, r5} at indices [0,1,2,3,4].
	expectedChrono := []int{0, 1, 2, 3, 4}
	for i, idx := range expectedChrono {
		if chrono[i].ID != insertedIDs[idx] {
			t.Errorf("ListActivitiesForIssue[%d]: got id %v, want %v (%s)",
				i, chrono[i].ID, insertedIDs[idx], rows[idx].label)
		}
	}

	// Verify total order: (created_at ASC, id ASC).
	for i := 1; i < len(chrono); i++ {
		prev, curr := chrono[i-1], chrono[i]
		if prev.CreatedAt.Time.After(curr.CreatedAt.Time) {
			t.Errorf("chrono not ASC at [%d]: created_at %v > %v",
				i, prev.CreatedAt.Time, curr.CreatedAt.Time)
		}
		if prev.CreatedAt.Time.Equal(curr.CreatedAt.Time) {
			prevBytes := prev.ID.Bytes
			currBytes := curr.ID.Bytes
			if cmp := compareUUIDBytes(prevBytes[:], currBytes[:]); cmp >= 0 {
				t.Errorf("chrono tie-break not id ASC at [%d]: prev id %v >= curr id %v",
					i, prev.ID, curr.ID)
			}
		}
	}
}

// compareUUIDBytes returns -1 if a < b, 0 if a == b, +1 if a > b,
// comparing 16-byte UUID values lexicographically.
func compareUUIDBytes(a, b []byte) int {
	for i := 0; i < 16 && i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
