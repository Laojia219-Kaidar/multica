package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func squadStatusPtr(value string) *string { return &value }

func TestDeriveSquadMemberStatus(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	online := pgtype.Text{String: "online", Valid: true}
	offline := pgtype.Text{String: "offline", Valid: true}
	missing := pgtype.Text{}

	tsAgo := func(d time.Duration) pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: now.Add(-d), Valid: true}
	}
	tsNone := pgtype.Timestamptz{}

	cases := []struct {
		name          string
		archived      bool
		runtimeStatus pgtype.Text
		lastSeen      pgtype.Timestamptz
		hasActiveTask bool
		want          string
	}{
		{"active wins over offline runtime", false, offline, tsAgo(time.Hour), true, "working"},
		{"active wins over missing runtime", false, missing, tsNone, true, "working"},
		{"online runtime, no task", false, online, tsAgo(2 * time.Second), false, "idle"},
		{"offline runtime, recent heartbeat", false, offline, tsAgo(2 * time.Minute), false, "unstable"},
		{"offline runtime, stale heartbeat", false, offline, tsAgo(2 * time.Hour), false, "offline"},
		{"offline runtime, no heartbeat", false, offline, tsNone, false, "offline"},
		{"no runtime row", false, missing, tsNone, false, "offline"},
		// Archived agents always report archived regardless of any leftover
		// runtime row or task — they should appear in the squad listing
		// but never look like they're still working or merely offline.
		{"archived agent with active task", true, online, tsAgo(time.Second), true, "archived"},
		{"archived agent with online runtime", true, online, tsAgo(time.Second), false, "archived"},
		{"archived agent already offline", true, offline, tsAgo(time.Hour), false, "archived"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveSquadMemberStatus(tc.archived, tc.runtimeStatus, tc.lastSeen, tc.hasActiveTask, now)
			if got != tc.want {
				t.Fatalf("deriveSquadMemberStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeriveSquadExecutionStateUsesCurrentTaskStatuses(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	online := pgtype.Text{String: "online", Valid: true}
	seen := pgtype.Timestamptz{Time: now, Valid: true}
	cases := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"running wins", []string{"queued", "running"}, "working"},
		{"directory wait is blocked", []string{"waiting_local_directory"}, "blocked"},
		{"queued is not working", []string{"queued"}, "queued"},
		{"dispatched is queued", []string{"dispatched"}, "queued"},
		{"no active task uses runtime availability", nil, "idle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveSquadExecutionState(tc.statuses, false, online, seen, now); got != tc.want {
				t.Fatalf("deriveSquadExecutionState = %q, want %q", got, tc.want)
			}
		})
	}
	if got := deriveSquadExecutionState(nil, true, online, seen, now); got != "archived" {
		t.Fatalf("archived execution state = %q, want archived", got)
	}
}

func TestSquadMemberStatusResponseExposesObservedStateAdditively(t *testing.T) {
	state := "queued"
	encoded, err := json.Marshal(SquadMemberStatusResponse{
		MemberType: "agent", MemberID: "agent-1", Status: squadStatusPtr("working"),
		ExecutionState: &state, ActiveTaskCount: 2, ActiveIssues: []SquadActiveIssueBrief{},
	})
	if err != nil {
		t.Fatalf("marshal status response: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if got["status"] != "working" || got["execution_state"] != "queued" || got["active_task_count"] != float64(2) {
		t.Fatalf("response lost legacy/observed fields: %#v", got)
	}
}
