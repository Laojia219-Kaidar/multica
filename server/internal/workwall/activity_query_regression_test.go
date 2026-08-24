//go:build integration

package workwall

import (
	"context"
	"fmt"
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
func TestActivityQueryOrdering_Regression_HIV981(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	slug := fmt.Sprintf("hiv981-reg-%d", time.Now().UnixNano())
	var wsID, issueID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id::text`,
		slug, slug).Scan(&wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := pool.QueryRow(ctx,
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

	// Insert 7 activity rows with controlled timestamps.
	// Rows 3-5 share the same created_at to exercise the tie-break.
	//
	//  id  | created_at | label
	// -----+------------+-------
	//  r1  |   T+0      | oldest
	//  r2  |   T+1      |
	//  r3  |   T+2      | \
	//  r4  |   T+2      |  > tied group (3 rows at same timestamp)
	//  r5  |   T+2      | /
	//  r6  |   T+3      |
	//  r7  |   T+4      | newest
	//
	// ListRecentActivitiesForIssue LIMIT 5 should return: r7, r6, r5, r4, r3
	// (newest 5, with tied group broken by id DESC).
	// ListActivitiesForIssue LIMIT 5 should return: r1, r2, r3, r4, r5
	// (oldest 5, with tied group broken by id ASC).

	baseTime := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	timestamps := []time.Time{
		baseTime,                          // r1
		baseTime.Add(1 * time.Hour),       // r2
		baseTime.Add(2 * time.Hour),       // r3 (tied)
		baseTime.Add(2 * time.Hour),       // r4 (tied)
		baseTime.Add(2 * time.Hour),       // r5 (tied)
		baseTime.Add(3 * time.Hour),       // r6
		baseTime.Add(4 * time.Hour),       // r7
	}
	actions := []string{"oldest", "r2", "r3", "r4", "r5", "r6", "newest"}

	var insertedIDs []pgtype.UUID
	for i := 0; i < 7; i++ {
		var idStr string
		if err := pool.QueryRow(ctx,
			`INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details, created_at)
			 VALUES ($1, $2, 'member', $1, $3, '{}', $4) RETURNING id::text`,
			wsID, issueID, actions[i], timestamps[i]).Scan(&idStr); err != nil {
			t.Fatalf("insert activity %d: %v", i, err)
		}
		var id pgtype.UUID
		if err := id.Scan(idStr); err != nil {
			t.Fatalf("parse activity id %d: %v", i, err)
		}
		insertedIDs = append(insertedIDs, id)
	}

	q := db.New(pool)

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

	// Expected: r7, r6, r5, r4, r3 (indices 6, 5, 4, 3, 2)
	expectedRecentOrder := []int{6, 5, 4, 3, 2}
	for i, idx := range expectedRecentOrder {
		if recent[i].ID != insertedIDs[idx] {
			t.Errorf("ListRecentActivitiesForIssue[%d]: got id %v, want %v (row r%d)",
				i, recent[i].ID, insertedIDs[idx], idx+1)
		}
	}

	// Verify strict DESC ordering by (created_at DESC, id DESC).
	for i := 1; i < len(recent); i++ {
		prev, curr := recent[i-1], recent[i]
		if prev.CreatedAt.Time.Before(curr.CreatedAt.Time) {
			t.Fatalf("ListRecentActivitiesForIssue not DESC at [%d]: %v < %v",
				i, prev.CreatedAt.Time, curr.CreatedAt.Time)
		}
		if prev.CreatedAt.Time.Equal(curr.CreatedAt.Time) {
			// Tie-break: id DESC means prev.ID > curr.ID (by insertion order).
			if prev.ID.Bytes == curr.ID.Bytes {
				t.Fatalf("ListRecentActivitiesForIssue duplicate id at [%d]", i)
			}
		}
	}

	// --- ListActivitiesForIssue: chronological ASC, LIMIT 5 ---
	chronological, err := q.ListActivitiesForIssue(ctx, db.ListActivitiesForIssueParams{
		IssueID: issueUUID,
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("ListActivitiesForIssue: %v", err)
	}
	if len(chronological) != 5 {
		t.Fatalf("ListActivitiesForIssue: got %d rows, want 5", len(chronological))
	}

	// Expected: r1, r2, r3, r4, r5 (indices 0, 1, 2, 3, 4)
	expectedChronoOrder := []int{0, 1, 2, 3, 4}
	for i, idx := range expectedChronoOrder {
		if chronological[i].ID != insertedIDs[idx] {
			t.Errorf("ListActivitiesForIssue[%d]: got id %v, want %v (row r%d)",
				i, chronological[i].ID, insertedIDs[idx], idx+1)
		}
	}

	// Verify strict ASC ordering by (created_at ASC, id ASC).
	for i := 1; i < len(chronological); i++ {
		prev, curr := chronological[i-1], chronological[i]
		if prev.CreatedAt.Time.After(curr.CreatedAt.Time) {
			t.Fatalf("ListActivitiesForIssue not ASC at [%d]: %v > %v",
				i, prev.CreatedAt.Time, curr.CreatedAt.Time)
		}
		if prev.CreatedAt.Time.Equal(curr.CreatedAt.Time) {
			if prev.ID.Bytes == curr.ID.Bytes {
				t.Fatalf("ListActivitiesForIssue duplicate id at [%d]", i)
			}
		}
	}
}
