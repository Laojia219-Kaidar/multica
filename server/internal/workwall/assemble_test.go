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
			got := AssembleAgent(agent(), tt.rt, tt.active, tt.lastOutcome, nil, nil, now, 0)
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
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, nil, nil, nil, now, 0)
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
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, nil, nil, acts, now, 0)
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

// chainFixture builds a fully hydrated chain for the chain-overlay tests.
func chainFixture() *ExecutionChain {
	return &ExecutionChain{
		TaskID:                 uuidStr(tu),
		IssueID:                uuidStr(tu),
		IssueIdentifier:        "HIV-797",
		IssueTitle:             "[DEV] Work Wall complete execution-chain projection",
		ProjectID:              uuidStr(tu),
		ProjectTitle:           "HiveCrew",
		RuntimeProfileID:       uuidStr(tu),
		RuntimeProfileName:     "glm-5.3-profile",
		RunID:                  uuidStr(tu),
		ExecutionReceiptRef:    "receipt://" + uuidStr(tu),
		ExecutionReceiptStatus: "completed",
	}
}

func TestAssembleAgent_ChainOverlayHydratesIdentifiers(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), task("running", now.Add(-time.Minute)), nil, chainFixture(), nil, now, 0)

	if got.IssueIdentifier != "HIV-797" {
		t.Fatalf("issue_identifier = %q", got.IssueIdentifier)
	}
	if got.IssueTitle != "[DEV] Work Wall complete execution-chain projection" {
		t.Fatalf("issue_title = %q", got.IssueTitle)
	}
	if got.ProjectID != uuidStr(tu) || got.ProjectTitle != "HiveCrew" {
		t.Fatalf("project chain = %q / %q", got.ProjectID, got.ProjectTitle)
	}
	if got.RuntimeProfileID != uuidStr(tu) || got.RuntimeProfileName != "glm-5.3-profile" {
		t.Fatalf("runtime profile chain = %q / %q", got.RuntimeProfileID, got.RuntimeProfileName)
	}
	if got.RunID != uuidStr(tu) {
		t.Fatalf("run_id = %q", got.RunID)
	}
	if got.ExecutionReceiptRef != "receipt://"+uuidStr(tu) || got.ExecutionReceiptStatus != "completed" {
		t.Fatalf("receipt = %q / %q", got.ExecutionReceiptRef, got.ExecutionReceiptStatus)
	}

	refs := map[string]bool{}
	for _, r := range got.SourceRefs {
		refs[r] = true
	}
	for _, want := range []string{"issue://" + uuidStr(tu), "project://" + uuidStr(tu), "profile://" + uuidStr(tu), "receipt://" + uuidStr(tu)} {
		if !refs[want] {
			t.Fatalf("source_refs missing %q: %v", want, got.SourceRefs)
		}
	}
}

func TestAssembleAgent_NilChainLeavesIdentifiersAbsent(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), task("running", now.Add(-time.Minute)), nil, nil, nil, now, 0)
	if got.IssueIdentifier != "" || got.IssueTitle != "" || got.ProjectID != "" || got.ProjectTitle != "" {
		t.Fatalf("issue/project identifiers must stay absent without chain evidence: %+v", got)
	}
	if got.RuntimeProfileID != "" || got.RuntimeProfileName != "" || got.RunID != "" {
		t.Fatalf("profile/run identifiers must stay absent without chain evidence: %+v", got)
	}
	if got.ExecutionReceiptRef != "" || got.ExecutionReceiptStatus != "" {
		t.Fatalf("receipt must stay absent without chain evidence: %+v", got)
	}
	if got.PresenceState != liveactivity.PresenceWorking {
		t.Fatalf("presence derivation must be untouched, got %q", got.PresenceState)
	}
}

func TestAssembleAgent_RecentTerminalTaskTracesTaskAndChain(t *testing.T) {
	now := time.Now().UTC()
	outcome := task("completed", now.Add(-time.Minute))
	chain := chainFixture()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, outcome, chain, nil, now, 0)

	if got.PresenceState != liveactivity.PresenceRecentlyCompleted {
		t.Fatalf("presence = %q, want recently_completed", got.PresenceState)
	}
	if got.TaskID != chain.TaskID {
		t.Fatalf("task_id = %q, want the recent terminal task %q", got.TaskID, chain.TaskID)
	}
	if got.IssueIdentifier != "HIV-797" {
		t.Fatalf("issue_identifier = %q for the recent terminal task", got.IssueIdentifier)
	}
	if got.ExecutionReceiptStatus != "completed" {
		t.Fatalf("receipt status = %q", got.ExecutionReceiptStatus)
	}
}

func TestAssembleAgent_EmptyChainEvidenceStaysEmpty(t *testing.T) {
	now := time.Now().UTC()
	chain := &ExecutionChain{TaskID: uuidStr(tu)} // task exists, every link missing
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), task("running", now.Add(-time.Minute)), nil, chain, nil, now, 0)
	if got.IssueIdentifier != "" || got.ProjectTitle != "" || got.RuntimeProfileName != "" || got.RunID != "" || got.ExecutionReceiptRef != "" {
		t.Fatalf("missing evidence must render empty, got %+v", got)
	}
}
