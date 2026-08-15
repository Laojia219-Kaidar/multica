package workwall

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var tu = pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, Valid: true}

func agent() db.Agent {
	return db.Agent{
		ID:          tu,
		WorkspaceID: tu,
		Name:        "Emory",
		AvatarUrl:   pgtype.Text{String: "https://cdn/e.png", Valid: true},
		Model:       pgtype.Text{String: "deepseek-v4", Valid: true},
		RuntimeID:   tu,
	}
}

func rt(status string, lastSeen time.Time) *db.AgentRuntime {
	return &db.AgentRuntime{
		ID:         tu,
		Provider:   "prime",
		Status:     status,
		LastSeenAt: pgtype.Timestamptz{Time: lastSeen, Valid: true},
	}
}

func task(status string, t time.Time) *db.AgentTaskQueue {
	atq := &db.AgentTaskQueue{
		ID:      tu,
		AgentID: tu,
		IssueID: tu,
		Status:  status,
	}
	switch status {
	case "completed", "failed":
		if !t.IsZero() {
			atq.CompletedAt = pgtype.Timestamptz{Time: t, Valid: true}
		}
	default:
		if !t.IsZero() {
			atq.StartedAt = pgtype.Timestamptz{Time: t, Valid: true}
		}
	}
	return atq
}

func TestAssembleAgent_PresenceMatrix(t *testing.T) {
	now := time.Now().UTC()
	fresh := now.Add(-5 * time.Second)
	stale := now.Add(-10 * time.Minute)

	tests := []struct {
		name        string
		rt          *db.AgentRuntime
		active      *db.AgentTaskQueue
		lastOutcome *db.AgentTaskQueue
		want        liveactivity.PresenceState
		wantFresh   liveactivity.FreshnessState
	}{
		{"runtime offline -> offline", rt("offline", fresh), task("running", now.Add(-time.Minute)), nil, liveactivity.PresenceOffline, liveactivity.FreshnessFresh},
		{"online + no task -> idle", rt("online", fresh), nil, nil, liveactivity.PresenceIdle, liveactivity.FreshnessFresh},
		{"queued task -> queued", rt("online", fresh), task("queued", time.Time{}), nil, liveactivity.PresenceQueued, liveactivity.FreshnessFresh},
		{"running + fresh heartbeat -> working", rt("online", fresh), task("running", now.Add(-time.Minute)), nil, liveactivity.PresenceWorking, liveactivity.FreshnessFresh},
		{"running + stale heartbeat -> unknown", rt("online", stale), task("running", now.Add(-time.Minute)), nil, liveactivity.PresenceUnknown, liveactivity.FreshnessStale},
		{"waiting_local_directory -> waiting", rt("online", fresh), task("waiting_local_directory", now.Add(-time.Minute)), nil, liveactivity.PresenceWaiting, liveactivity.FreshnessFresh},
		{"missing runtime -> offline + missing freshness", nil, nil, nil, liveactivity.PresenceOffline, liveactivity.FreshnessMissing},
		{"recent completed -> recently_completed", rt("online", fresh), nil, task("completed", now.Add(-time.Minute)), liveactivity.PresenceRecentlyCompleted, liveactivity.FreshnessFresh},
		{"old completed -> idle", rt("online", fresh), nil, task("completed", now.Add(-time.Hour)), liveactivity.PresenceIdle, liveactivity.FreshnessFresh},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssembleAgent(agent(), tt.rt, tt.active, tt.lastOutcome, nil, now, 0)
			if got.PresenceState != tt.want {
				t.Fatalf("presence = %q, want %q", got.PresenceState, tt.want)
			}
			if got.FreshnessState != tt.wantFresh {
				t.Fatalf("freshness = %q, want %q", got.FreshnessState, tt.wantFresh)
			}
		})
	}
}

func TestAssembleAgent_IdentityAndRefs(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, nil, nil, now, 0)
	if got.DisplayName != "Emory" {
		t.Fatalf("display_name = %q", got.DisplayName)
	}
	if got.ModelName != "deepseek-v4" {
		t.Fatalf("model_name = %q", got.ModelName)
	}
	if got.SchemaVersion != liveactivity.SchemaVersionV1 {
		t.Fatalf("schema_version = %q", got.SchemaVersion)
	}
	if len(got.SourceRefs) < 2 {
		t.Fatalf("source_refs too short: %v", got.SourceRefs)
	}
}

func TestAssembleAgent_PopulatesRecentActivity(t *testing.T) {
	now := time.Now().UTC()
	acts := []db.ActivityLog{
		{
			ID:        tu,
			Action:    "task_completed",
			CreatedAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
		},
	}
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, nil, acts, now, 0)
	if len(got.RecentEvents) != 1 {
		t.Fatalf("expected 1 recent event, got %d", len(got.RecentEvents))
	}
	if got.ActivityKind != "activity.task_completed" {
		t.Fatalf("activity_kind = %q", got.ActivityKind)
	}
	if got.ActivitySummary != "任务完成" {
		t.Fatalf("activity_summary = %q", got.ActivitySummary)
	}
}
