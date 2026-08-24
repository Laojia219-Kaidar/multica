package workwall

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRecentEventFromActivity_Sanitized(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	a := db.ActivityLog{
		ID:        tu,
		Action:    "task_completed",
		Details:   []byte(`{"stdout":"DB_URL=postgres://user:secret@host","chain_of_thought":"password hunter2"}`),
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}
	re := RecentEventFromActivity(a)
	if re.Kind != "activity.task_completed" {
		t.Fatalf("kind = %q", re.Kind)
	}
	if re.SafeSummary != "任务完成" {
		t.Fatalf("safe_summary = %q", re.SafeSummary)
	}
	// The raw details JSON must never be exposed.
	if re.SafeSummary == "" || len(re.SafeSummary) > 20 {
		t.Fatalf("safe_summary looks wrong: %q", re.SafeSummary)
	}
}

func TestRecentEventsFromActivities_DescInput_PresentsNewestFirst(t *testing.T) {
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	mk := func(id byte, ts time.Time) db.ActivityLog {
		return db.ActivityLog{
			ID:        pgtype.UUID{Bytes: [16]byte{id}, Valid: true},
			Action:    "issue_updated",
			CreatedAt: pgtype.Timestamptz{Time: ts, Valid: true},
		}
	}
	// Simulate ListRecentActivitiesForIssue output: 5 rows already in DESC
	// order (newest first), as the DB query returns them after LIMIT 5.
	rows := []db.ActivityLog{
		mk(8, base.Add(7*time.Hour)),
		mk(7, base.Add(6*time.Hour)),
		mk(6, base.Add(5*time.Hour)),
		mk(5, base.Add(4*time.Hour)),
		mk(4, base.Add(3*time.Hour)),
	}
	out := RecentEventsFromActivities(rows, 5)
	if len(out) != 5 {
		t.Fatalf("expected 5 events, got %d", len(out))
	}
	for i := 1; i < len(out); i++ {
		if !out[i-1].OccurredAt.After(out[i].OccurredAt) {
			t.Fatalf("not newest-first at index %d: %v <= %v",
				i, out[i-1].OccurredAt, out[i].OccurredAt)
		}
	}
	if out[0].OccurredAt != base.Add(7*time.Hour) {
		t.Fatalf("newest event should be +7h, got %v", out[0].OccurredAt)
	}
}

func TestRecentEventsFromActivities_SortedAndCapped(t *testing.T) {
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	mk := func(id byte, ts time.Time) db.ActivityLog {
		return db.ActivityLog{
			ID:        pgtype.UUID{Bytes: [16]byte{id}, Valid: true},
			Action:    "issue_created",
			CreatedAt: pgtype.Timestamptz{Time: ts, Valid: true},
		}
	}
	rows := []db.ActivityLog{
		mk(1, base),
		mk(2, base.Add(time.Minute)),
		mk(3, base.Add(-time.Minute)),
	}
	out := RecentEventsFromActivities(rows, 2)
	if len(out) != 2 {
		t.Fatalf("expected cap 2, got %d", len(out))
	}
	if out[0].SafeSummary != "议题创建" || out[1].SafeSummary != "议题创建" {
		t.Fatalf("unexpected summaries: %+v", out)
	}
	if !out[0].OccurredAt.After(out[1].OccurredAt) {
		t.Fatalf("not sorted newest-first: %v vs %v", out[0].OccurredAt, out[1].OccurredAt)
	}
}
