package workwall

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// uuidFromByte returns a valid UUID whose first byte is b (rest zero) so the
// final id DESC tie-breaker can be exercised with an explicit byte ordering.
func uuidFromByte(b byte) pgtype.UUID {
	var raw [16]byte
	raw[0] = b
	return pgtype.UUID{Bytes: raw, Valid: true}
}

func tsAt(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

var selBase = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

// selTask builds a synthetic active/outcome row for the shared agent.
func selTask(id byte, status string) *db.AgentTaskQueue {
	return &db.AgentTaskQueue{
		ID:      uuidFromByte(id),
		AgentID: uuidFromByte(0xAA),
		IssueID: uuidFromByte(id),
		Status:  status,
	}
}

func perms[T any](xs []T) [][]T {
	if len(xs) <= 1 {
		return [][]T{append([]T{}, xs...)}
	}
	var out [][]T
	for i := range xs {
		rest := append(append([]T{}, xs[:i]...), xs[i+1:]...)
		for _, p := range perms(rest) {
			out = append(out, append([]T{xs[i]}, p...))
		}
	}
	return out
}

// TestSelectPreferredActiveTask_StatusPairs walks every ordered pair of the
// four active statuses plus a stray unknown one and asserts the higher-ranked
// status always wins, in both argument orders.
func TestSelectPreferredActiveTask_StatusPairs(t *testing.T) {
	ranks := []struct {
		status string
		rank   int
	}{
		{"running", 4},
		{"waiting_local_directory", 3},
		{"dispatched", 2},
		{"queued", 1},
		{"teleported", 0}, // unknown statuses must never outrank known ones
	}
	for _, a := range ranks {
		for _, b := range ranks {
			want := a.status
			if b.rank > a.rank {
				want = b.status
			}
			gotAB := selectPreferredActiveTask(selTask(1, a.status), selTask(2, b.status))
			gotBA := selectPreferredActiveTask(selTask(2, b.status), selTask(1, a.status))
			if gotAB.Status != want || gotBA.Status != want {
				t.Fatalf("%s vs %s: picked %q / %q, want %q",
					a.status, b.status, gotAB.Status, gotBA.Status, want)
			}
		}
	}
}

func TestSelectPreferredActiveTask_TieBreakers(t *testing.T) {
	tests := []struct {
		name    string
		current *db.AgentTaskQueue
		cand    *db.AgentTaskQueue
		wantID  byte
	}{
		{
			name:    "equal status: later started_at wins",
			current: withStarted(selTask(1, "running"), selBase.Add(-time.Minute)),
			cand:    withStarted(selTask(2, "running"), selBase),
			wantID:  2,
		},
		{
			name:    "NULL started_at loses to present one (NULLS LAST)",
			current: selTask(1, "dispatched"),
			cand:    withStarted(selTask(2, "dispatched"), selBase),
			wantID:  2,
		},
		{
			name:    "NULL started_at wins when both NULL is not the case reversed",
			current: withStarted(selTask(1, "queued"), selBase),
			cand:    selTask(2, "queued"),
			wantID:  1,
		},
		{
			name:    "started_at tied: later dispatched_at wins",
			current: withDispatched(withStarted(selTask(1, "running"), selBase), selBase.Add(-time.Hour)),
			cand:    withDispatched(withStarted(selTask(2, "running"), selBase), selBase),
			wantID:  2,
		},
		{
			name:    "NULL dispatched_at loses when started_at ties on NULL",
			current: withDispatched(selTask(1, "queued"), selBase),
			cand:    selTask(2, "queued"),
			wantID:  1,
		},
		{
			name: "start+dispatch tie: later created_at wins",
			current: withCreated(
				withDispatched(withStarted(selTask(1, "waiting_local_directory"), selBase), selBase),
				selBase.Add(-time.Minute)),
			cand: withCreated(
				withDispatched(withStarted(selTask(2, "waiting_local_directory"), selBase), selBase),
				selBase),
			wantID: 2,
		},
		{
			name: "invalid created_at treated as oldest despite larger id",
			current: func() *db.AgentTaskQueue {
				tk := withStarted(selTask(1, "running"), selBase)
				tk.CreatedAt = tsAt(selBase)
				return tk
			}(),
			cand: func() *db.AgentTaskQueue {
				tk := withStarted(selTask(9, "running"), selBase)
				tk.CreatedAt = pgtype.Timestamptz{}
				return tk
			}(),
			wantID: 1,
		},
		{
			name: "all timestamps tied: higher id bytes win (id DESC)",
			current: withCreated(
				withDispatched(withStarted(selTask(1, "dispatched"), selBase), selBase), selBase),
			cand: withCreated(
				withDispatched(withStarted(selTask(2, "dispatched"), selBase), selBase), selBase),
			wantID: 2,
		},
		{
			name: "all timestamps tied: lower id loses regardless of fold side",
			current: withCreated(
				withDispatched(withStarted(selTask(3, "dispatched"), selBase), selBase), selBase),
			cand: withCreated(
				withDispatched(withStarted(selTask(4, "dispatched"), selBase), selBase), selBase),
			wantID: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectPreferredActiveTask(tt.current, tt.cand); got.ID != uuidFromByte(tt.wantID) {
				t.Fatalf("pick id byte = %d, want %d", got.ID.Bytes[0], tt.wantID)
			}
			if got := selectPreferredActiveTask(tt.cand, tt.current); got.ID != uuidFromByte(tt.wantID) {
				t.Fatalf("reversed pick id byte = %d, want %d", got.ID.Bytes[0], tt.wantID)
			}
		})
	}
}

func withStarted(tk *db.AgentTaskQueue, at time.Time) *db.AgentTaskQueue {
	tk.StartedAt = tsAt(at)
	return tk
}

func withDispatched(tk *db.AgentTaskQueue, at time.Time) *db.AgentTaskQueue {
	tk.DispatchedAt = tsAt(at)
	return tk
}

func withCreated(tk *db.AgentTaskQueue, at time.Time) *db.AgentTaskQueue {
	tk.CreatedAt = tsAt(at)
	return tk
}

func TestSelectPreferredActiveTask_NilArguments(t *testing.T) {
	tk := selTask(1, "running")
	if got := selectPreferredActiveTask(nil, nil); got != nil {
		t.Fatalf("nil+nil = %v, want nil", got)
	}
	if got := selectPreferredActiveTask(nil, tk); got != tk {
		t.Fatalf("nil+task should return candidate")
	}
	if got := selectPreferredActiveTask(tk, nil); got != tk {
		t.Fatalf("task+nil should return current")
	}
}

// TestPartitionWorkspaceTasks_InputOrderInvariance folds all 24 permutations
// of one agent's four active tasks and requires the same winner every time:
// running (newest start) must beat waiting_local_directory, dispatched and
// queued no matter how the SQL result was ordered.
func TestPartitionWorkspaceTasks_InputOrderInvariance(t *testing.T) {
	tasks := []db.AgentTaskQueue{
		*withStarted(selTask(1, "queued"), selBase.Add(-4*time.Hour)),
		*withDispatched(selTask(2, "dispatched"), selBase.Add(-3*time.Hour)),
		*withStarted(selTask(3, "waiting_local_directory"), selBase.Add(-2*time.Hour)),
		*withStarted(selTask(4, "running"), selBase.Add(-time.Hour)),
	}
	var wantID pgtype.UUID
	for i, order := range perms(tasks) {
		active, _ := partitionWorkspaceTasks(order)
		got, ok := active[uuidFromByte(0xAA).String()]
		if !ok {
			t.Fatalf("permutation %d: no active task for agent", i)
		}
		if i == 0 {
			wantID = got.ID
			continue
		}
		if got.ID != wantID {
			t.Fatalf("permutation %d picked task %v, want %v", i, got.ID, wantID)
		}
	}
	if wantID != uuidFromByte(4) {
		t.Fatalf("baseline pick = %v, want running task 4", wantID)
	}
}

// TestPartitionWorkspaceTasks_WorkspaceIsolation interleaves two agents'
// tasks and asserts each agent keeps its own preferred active task and its own
// outcome — no cross-agent bleed through the shared result set.
func TestPartitionWorkspaceTasks_WorkspaceIsolation(t *testing.T) {
	agentA, agentB := uuidFromByte(0x01), uuidFromByte(0x02)
	mk := func(agent pgtype.UUID, id byte, status string) db.AgentTaskQueue {
		tk := *selTask(id, status)
		tk.AgentID = agent
		return tk
	}
	orders := [][]db.AgentTaskQueue{
		{
			mk(agentA, 0x10, "running"),
			mk(agentB, 0x20, "waiting_local_directory"),
			mk(agentA, 0x11, "queued"),
			mk(agentB, 0x21, "dispatched"),
			mk(agentA, 0x12, "completed"),
			mk(agentB, 0x22, "failed"),
		},
		{
			mk(agentB, 0x22, "failed"),
			mk(agentA, 0x12, "completed"),
			mk(agentB, 0x21, "dispatched"),
			mk(agentA, 0x11, "queued"),
			mk(agentB, 0x20, "waiting_local_directory"),
			mk(agentA, 0x10, "running"),
		},
	}
	for i, tasks := range orders {
		active, outcome := partitionWorkspaceTasks(tasks)
		if ga := active[agentA.String()]; ga == nil || ga.Status != "running" {
			t.Fatalf("order %d: agent A active = %+v, want running", i, ga)
		}
		if gb := active[agentB.String()]; gb == nil || gb.Status != "waiting_local_directory" {
			t.Fatalf("order %d: agent B active = %+v, want waiting_local_directory", i, gb)
		}
		if oa := outcome[agentA.String()]; oa == nil || oa.Status != "completed" {
			t.Fatalf("order %d: agent A outcome = %+v, want completed", i, oa)
		}
		if ob := outcome[agentB.String()]; ob == nil || ob.Status != "failed" {
			t.Fatalf("order %d: agent B outcome = %+v, want failed", i, ob)
		}
	}
}

// TestPartitionWorkspaceTasks_NilIssueStaysVisible asserts an active task
// without an issue stays eligible for selection (Snapshot skips only its Issue
// activity lookup, never the row itself).
func TestPartitionWorkspaceTasks_NilIssueStaysVisible(t *testing.T) {
	noIssue := selTask(5, "running")
	noIssue.IssueID = pgtype.UUID{}
	withIssue := withStarted(selTask(6, "queued"), selBase)

	active, _ := partitionWorkspaceTasks([]db.AgentTaskQueue{*noIssue, *withIssue})
	got := active[uuidFromByte(0xAA).String()]
	if got == nil || got.Status != "running" {
		t.Fatalf("active = %+v, want the nil-issue running task", got)
	}
	if got.IssueID.Valid {
		t.Fatalf("nil-issue task came back with a valid issue id")
	}
}

// TestCancelledIsNotActiveStatus pins the Go-side half of the outcome
// contract: cancel is procedural, so a cancelled row can never occupy the
// active/presence slot. (The query additionally excludes cancelled from the
// outcome half, so it can never mask a prior completed/failed outcome either.)
func TestCancelledIsNotActiveStatus(t *testing.T) {
	if isActiveTaskStatus("cancelled") {
		t.Fatal("cancelled must not count as an active task status")
	}
	active, outcome := partitionWorkspaceTasks([]db.AgentTaskQueue{
		*selTask(7, "cancelled"),
		*selTask(8, "completed"),
	})
	if got := active[uuidFromByte(0xAA).String()]; got != nil {
		t.Fatalf("cancelled row surfaced as active task %+v", got)
	}
	// Non-active rows keep routing to the outcome slot exactly as before R2;
	// ListWorkspaceAgentTaskSnapshot guarantees at most one outcome row per
	// agent and never returns cancelled rows, so routing order cannot mask a
	// real outcome in practice.
	if got := outcome[uuidFromByte(0xAA).String()]; got == nil || got.Status != "completed" {
		t.Fatalf("outcome = %+v, want completed", got)
	}
}

// TestSelectedActiveFeedsAssembleAgent_RuntimeSemantics verifies the selected
// winner plugs into the unchanged presence derivation, including the existing
// offline/unknown runtime semantics.
func TestSelectedActiveFeedsAssembleAgent_RuntimeSemantics(t *testing.T) {
	now := selBase
	freshHeartbeat := now.Add(-5 * time.Second)
	staleHeartbeat := now.Add(-10 * time.Minute)

	tasks := []db.AgentTaskQueue{
		*withStarted(selTask(1, "queued"), now.Add(-time.Hour)),
		*withStarted(selTask(2, "running"), now.Add(-time.Minute)),
	}
	active, _ := partitionWorkspaceTasks(tasks)
	winner := active[uuidFromByte(0xAA).String()]
	if winner.Status != "running" {
		t.Fatalf("winner = %q, want running", winner.Status)
	}

	ag := agent()
	ag.ID = uuidFromByte(0xAA)

	offline := AssembleAgent(ag, rt("offline", freshHeartbeat), winner, nil, nil, now, 0)
	if offline.PresenceState != liveactivity.PresenceOffline {
		t.Fatalf("offline runtime presence = %q, want offline", offline.PresenceState)
	}

	stale := AssembleAgent(ag, rt("online", staleHeartbeat), winner, nil, nil, now, 0)
	if stale.PresenceState != liveactivity.PresenceUnknown {
		t.Fatalf("stale heartbeat presence = %q, want unknown", stale.PresenceState)
	}

	fresh := AssembleAgent(ag, rt("online", freshHeartbeat), winner, nil, nil, now, 0)
	if fresh.PresenceState != liveactivity.PresenceWorking {
		t.Fatalf("fresh running presence = %q, want working", fresh.PresenceState)
	}

	// Outcome routing still drives recently_completed after the refactor.
	completed := selTask(9, "completed")
	completed.CompletedAt = tsAt(now.Add(-time.Minute))
	_, outcome := partitionWorkspaceTasks([]db.AgentTaskQueue{*completed})
	idleDone := AssembleAgent(ag, rt("online", freshHeartbeat), nil, outcome[uuidFromByte(0xAA).String()], nil, now, 0)
	if idleDone.PresenceState != liveactivity.PresenceRecentlyCompleted {
		t.Fatalf("routed outcome presence = %q, want recently_completed", idleDone.PresenceState)
	}
}
