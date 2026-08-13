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
