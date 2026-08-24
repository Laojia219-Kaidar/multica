package workwall

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// --- A2 fixture helpers -----------------------------------------------------

func a2UUID(seed byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{seed}, Valid: true}
}

func a2Text(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func a2Ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// a2Event builds one canonical work_event row. observedAt is arrival order
// (projection truth); occurredAt is the actor's claimed time.
func a2Event(idSeed byte, eventType string, observed, occurred time.Time, payload string) db.WorkEvent {
	return db.WorkEvent{
		ID:             a2UUID(idSeed),
		WorkspaceID:    a2UUID(1),
		WorkRef:        "hivecrew://ws/work/prj/issue-1/task-1",
		SessionID:      a2Text("sess-1"),
		RunID:          a2Text("run-1"),
		EventType:      eventType,
		EventPayload:   []byte(payload),
		IdempotencyKey: "idem-" + string(rune('a'+idSeed)),
		OccurredAt:     a2Ts(occurred),
		ObservedAt:     a2Ts(observed),
		CreatedAt:      a2Ts(observed),
	}
}

func a2OpenIssue() *db.Issue {
	return &db.Issue{ID: a2UUID(2), WorkspaceID: a2UUID(1), Status: "in_progress", Title: "T"}
}

func a2Task(status string) *db.AgentTaskQueue {
	return &db.AgentTaskQueue{ID: a2UUID(3), AgentID: a2UUID(4), IssueID: a2UUID(2), Status: status}
}

func a2Presence(session string, heartbeat time.Time) *db.TerminalPresence {
	return &db.TerminalPresence{
		ID:          a2UUID(5),
		WorkspaceID: a2UUID(1),
		Host:        "mac-1",
		SessionName: session,
		HeartbeatAt: a2Ts(heartbeat),
	}
}

func a2Receipt(terminal string) *db.ExecutionReceipt {
	return &db.ExecutionReceipt{
		TaskID:         a2UUID(3),
		WorkspaceID:    a2UUID(1),
		IssueID:        a2UUID(2),
		TerminalStatus: a2Text(terminal),
	}
}

func a2Now() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) }

// --- required execution states ---------------------------------------------

func TestProjectA2Pane_ExecutionStateMatrix(t *testing.T) {
	now := a2Now()
	fresh := now.Add(-10 * time.Second)

	tests := []struct {
		name string
		in   A2PaneInput
		want A2ExecutionState
		work bool
	}{
		{
			name: "progress + fresh heartbeat -> active and working",
			in: A2PaneInput{
				WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
				Events:   []db.WorkEvent{a2Event(10, "started", now.Add(-2*time.Minute), now.Add(-2*time.Minute), `{}`), a2Event(11, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{"stage":"build"}`)},
				Issue:    a2OpenIssue(),
				Task:     a2Task("running"),
				Presence: a2Presence("sess-1", fresh),
				Now:      now,
			},
			want: A2ExecutionActive,
			work: true,
		},
		{
			name: "late pre-terminal event after finished -> replay, never working",
			in: A2PaneInput{
				WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
				// finished arrives first; a stale progress event is observed
				// afterwards but occurred before the finish.
				Events: []db.WorkEvent{
					a2Event(11, "progress", now.Add(-3*time.Minute), now.Add(-3*time.Minute), `{}`),
					a2Event(12, "finished", now.Add(-2*time.Minute), now.Add(-2*time.Minute), `{}`),
					a2Event(13, "progress", now.Add(-1*time.Minute), now.Add(-4*time.Minute), `{}`),
				},
				Issue:    a2OpenIssue(),
				Presence: a2Presence("sess-1", fresh),
				Now:      now,
			},
			want: A2ExecutionReplay,
			work: false,
		},
		{
			name: "execution receipt failed -> failed, never working",
			in: A2PaneInput{
				WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
				Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
				Issue:    a2OpenIssue(),
				Task:     a2Task("failed"),
				Receipt:  a2Receipt("failed"),
				Presence: a2Presence("sess-1", fresh),
				Now:      now,
			},
			want: A2ExecutionFailed,
			work: false,
		},
		{
			name: "issue cancelled -> cancelled, never working",
			in: A2PaneInput{
				WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
				Events:  []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
				Issue:   func() *db.Issue { i := a2OpenIssue(); i.Status = "cancelled"; return i }(),
				Task:    a2Task("cancelled"),
				Now:     now,
			},
			want: A2ExecutionCancelled,
			work: false,
		},
		{
			name: "finished event + open issue + no receipt -> issue_state_mismatch, never working",
			in: A2PaneInput{
				WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
				Events:   []db.WorkEvent{a2Event(10, "finished", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
				Issue:    a2OpenIssue(),
				Presence: a2Presence("sess-1", fresh),
				Now:      now,
			},
			want: A2ExecutionIssueMismatch,
			work: false,
		},
		{
			name: "finished event + receipt completed -> completed by canonical receipt only",
			in: A2PaneInput{
				WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
				Events:  []db.WorkEvent{a2Event(10, "finished", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
				Issue:   a2OpenIssue(),
				Receipt: a2Receipt("completed"),
				Now:     now,
			},
			want: A2ExecutionCompleted,
			work: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane, ok := ProjectA2Pane(tt.in)
			if !ok {
				t.Fatalf("expected a projected pane")
			}
			if pane.ExecutionState != tt.want {
				t.Fatalf("execution_state = %q, want %q", pane.ExecutionState, tt.want)
			}
			if pane.Working != tt.work {
				t.Fatalf("working = %v, want %v (replay and mismatch must never count as working)", pane.Working, tt.work)
			}
			if pane.SourceEventID == "" {
				t.Fatalf("pane must carry a stable source_event_id")
			}
			if pane.WorkRef != tt.in.WorkRef {
				t.Fatalf("work_ref = %q, want %q", pane.WorkRef, tt.in.WorkRef)
			}
		})
	}
}

func TestProjectA2Pane_NoEventsYieldsNoPane(t *testing.T) {
	if _, ok := ProjectA2Pane(A2PaneInput{WorkRef: "hivecrew://ws/work/prj/issue-1", Now: a2Now()}); ok {
		t.Fatalf("a work_ref with zero events must not project a pane")
	}
}

// Canonical terminal evidence describes the work itself, so it outranks the
// replay classification of a noisy late delivery.
func TestProjectA2Pane_CanonicalTerminalOutranksReplay(t *testing.T) {
	now := a2Now()
	in := A2PaneInput{
		WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
		Events: []db.WorkEvent{
			a2Event(11, "progress", now.Add(-3*time.Minute), now.Add(-3*time.Minute), `{}`),
			a2Event(12, "finished", now.Add(-2*time.Minute), now.Add(-2*time.Minute), `{}`),
			a2Event(13, "progress", now.Add(-1*time.Minute), now.Add(-4*time.Minute), `{}`),
		},
		Issue:    func() *db.Issue { i := a2OpenIssue(); i.Status = "cancelled"; return i }(),
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(in)
	if !ok {
		t.Fatalf("expected pane")
	}
	if pane.ExecutionState != A2ExecutionCancelled {
		t.Fatalf("execution_state = %q, want cancelled (issue authority outranks replay noise)", pane.ExecutionState)
	}
	if pane.Working {
		t.Fatalf("cancelled must never count as working")
	}
}

// --- missing heartbeat fails closed -----------------------------------------

func TestProjectA2Pane_MissingHeartbeatFailsClosed(t *testing.T) {
	now := a2Now()
	base := A2PaneInput{
		WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
		Events:  []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
		Issue:   a2OpenIssue(),
		Task:    a2Task("running"),
		Now:     now,
	}

	t.Run("no presence row at all", func(t *testing.T) {
		pane, ok := ProjectA2Pane(base)
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.Working {
			t.Fatalf("missing heartbeat must fail closed: working must be false")
		}
		if pane.Freshness != liveactivity.FreshnessMissing {
			t.Fatalf("freshness = %q, want missing", pane.Freshness)
		}
		if pane.SurfaceKind != A2SurfaceEventConsole {
			t.Fatalf("surface = %q, want event_console (never fake a terminal)", pane.SurfaceKind)
		}
	})

	t.Run("stale presence row", func(t *testing.T) {
		in := base
		in.Presence = a2Presence("sess-1", now.Add(-10*time.Minute))
		pane, ok := ProjectA2Pane(in)
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.Working {
			t.Fatalf("stale heartbeat must fail closed: working must be false")
		}
		if pane.Freshness != liveactivity.FreshnessStale {
			t.Fatalf("freshness = %q, want stale", pane.Freshness)
		}
	})

	t.Run("presence for a different session must not certify this pane", func(t *testing.T) {
		in := base
		in.Presence = a2Presence("other-session", now.Add(-1*time.Second))
		pane, ok := ProjectA2Pane(in)
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.Working {
			t.Fatalf("foreign-session heartbeat must fail closed: working must be false")
		}
		if pane.SurfaceKind != A2SurfaceEventConsole {
			t.Fatalf("surface = %q, want event_console", pane.SurfaceKind)
		}
	})
}

// --- terminal text never establishes completion ------------------------------

func TestProjectA2Pane_TerminalTextNeverEstablishesCompletion(t *testing.T) {
	now := a2Now()
	term := `{"status":"completed","message":"task completed successfully","note":"done done done"}`
	in := A2PaneInput{
		WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
		Events: []db.WorkEvent{
			a2Event(10, "progress", now.Add(-2*time.Minute), now.Add(-2*time.Minute), `{}`),
			// The payload text claims completion; the ledger has no finished
			// event, no receipt, and the issue is open.
			a2Event(11, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), term),
		},
		Issue:    a2OpenIssue(),
		Task:     a2Task("running"),
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(in)
	if !ok {
		t.Fatalf("expected pane")
	}
	if pane.ExecutionState == A2ExecutionCompleted {
		t.Fatalf("terminal text in a payload must never establish completion")
	}
	if pane.CompletedAt != nil {
		t.Fatalf("terminal text must never set completed_at, got %v", pane.CompletedAt)
	}
	if pane.ExecutionState != A2ExecutionActive {
		t.Fatalf("execution_state = %q, want active", pane.ExecutionState)
	}
	if !pane.Working {
		t.Fatalf("fresh heartbeat + live evidence should still count as working")
	}
}

// --- idempotent replay -------------------------------------------------------

func TestProjectA2Pane_IdempotentReplay(t *testing.T) {
	now := a2Now()
	in := A2PaneInput{
		WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
		Events: []db.WorkEvent{
			a2Event(10, "started", now.Add(-3*time.Minute), now.Add(-3*time.Minute), `{}`),
			a2Event(11, "progress", now.Add(-2*time.Minute), now.Add(-2*time.Minute), `{"stage":"build"}`),
			a2Event(12, "checkpoint", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`),
		},
		Issue:    a2OpenIssue(),
		Task:     a2Task("running"),
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	first, ok := ProjectA2Pane(in)
	if !ok {
		t.Fatalf("expected pane")
	}
	second, ok := ProjectA2Pane(in)
	if !ok {
		t.Fatalf("expected pane")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("projection must be idempotent: panes differ:\n%+v\n%+v", first, second)
	}

	// The same event delivered twice (defensive duplicate row) must not
	// duplicate work or panic: it classifies as replay and never works.
	dup := in
	dup.Events = append(append([]db.WorkEvent{}, in.Events...), in.Events[len(in.Events)-1])
	pane, ok := ProjectA2Pane(dup)
	if !ok {
		t.Fatalf("expected pane")
	}
	if pane.Working {
		t.Fatalf("duplicate terminal-side delivery must not count as working")
	}
	if pane.ExecutionState != A2ExecutionReplay {
		t.Fatalf("execution_state = %q, want replay", pane.ExecutionState)
	}
}

// --- no secret / raw transcript projection -----------------------------------

func TestProjectA2Pane_NoSecretOrRawTranscriptProjection(t *testing.T) {
	now := a2Now()
	payload := `{"stage":"build",` +
		`"stdout":"RAW-STDOUT-SENTINEL-9f2c",` +
		`"stderr":"RAW-STDERR-SENTINEL-1a3b",` +
		`"transcript":"RAW-TRANSCRIPT-SENTINEL-77de",` +
		`"chain_of_thought":"COT-SENTINEL-42aa",` +
		`"env":{"OPENAI_API_KEY":"sk-SECRET-SENTINEL-5e11"},` +
		`"api_key":"sk-SECRET-SENTINEL-5e11",` +
		`"password":"PASSWORD-SENTINEL-c0de",` +
		`"token":"TOKEN-SENTINEL-3d10"}`
	in := A2PaneInput{
		WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
		Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), payload)},
		Issue:    a2OpenIssue(),
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(in)
	if !ok {
		t.Fatalf("expected pane")
	}
	blob, err := json.Marshal(pane)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{
		"RAW-STDOUT-SENTINEL-9f2c",
		"RAW-STDERR-SENTINEL-1a3b",
		"RAW-TRANSCRIPT-SENTINEL-77de",
		"COT-SENTINEL-42aa",
		"sk-SECRET-SENTINEL-5e11",
		"PASSWORD-SENTINEL-c0de",
		"TOKEN-SENTINEL-3d10",
	} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("projected pane leaked %q", secret)
		}
	}
	if !strings.Contains(string(blob), "build") {
		t.Fatalf("allowlisted structured stage should be projected")
	}
}

// --- surface kind rules -------------------------------------------------------

func TestProjectA2Pane_SurfaceKindRules(t *testing.T) {
	now := a2Now()
	base := func() A2PaneInput {
		return A2PaneInput{
			WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
			Events:  []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Issue:   a2OpenIssue(),
			Now:     now,
		}
	}

	t.Run("matched fresh presence -> terminal", func(t *testing.T) {
		in := base()
		in.Presence = a2Presence("sess-1", now.Add(-5*time.Second))
		pane, _ := ProjectA2Pane(in)
		if pane.SurfaceKind != A2SurfaceTerminal {
			t.Fatalf("surface = %q, want terminal", pane.SurfaceKind)
		}
		if pane.LastHeartbeatAt == nil {
			t.Fatalf("matched presence must surface its heartbeat time")
		}
	})

	t.Run("api-only route (no presence) -> event_console, never fake terminal", func(t *testing.T) {
		in := base()
		pane, _ := ProjectA2Pane(in)
		if pane.SurfaceKind != A2SurfaceEventConsole {
			t.Fatalf("surface = %q, want event_console", pane.SurfaceKind)
		}
	})

	t.Run("matched but stale presence -> terminal with stale freshness, not working", func(t *testing.T) {
		in := base()
		in.Presence = a2Presence("sess-1", now.Add(-10*time.Minute))
		pane, _ := ProjectA2Pane(in)
		if pane.SurfaceKind != A2SurfaceTerminal {
			t.Fatalf("surface = %q, want terminal (route is real, heartbeat is stale)", pane.SurfaceKind)
		}
		if pane.Working {
			t.Fatalf("stale heartbeat must not count as working")
		}
		if pane.Freshness != liveactivity.FreshnessStale {
			t.Fatalf("freshness = %q, want stale", pane.Freshness)
		}
	})
}

// --- ownership + stable source_event_id ---------------------------------------

func TestProjectA2Pane_OwnershipAndStableSourceEventID(t *testing.T) {
	now := a2Now()
	agentRow := &db.Agent{ID: a2UUID(4), WorkspaceID: a2UUID(1), Name: "Shard"}
	dispatch := &db.AssignmentDispatchReceipt{
		CommandID:     a2UUID(9),
		WorkspaceID:   a2UUID(1),
		IssueID:       a2UUID(2),
		LocalAgentID:  a2UUID(4),
		InitialTaskID: a2UUID(3),
		EmployeeRef:   "employee://hcops/shard",
	}
	in := A2PaneInput{
		WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
		Events: []db.WorkEvent{
			a2Event(10, "started", now.Add(-3*time.Minute), now.Add(-3*time.Minute), `{}`),
			a2Event(11, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`),
			a2Event(12, "progress", now.Add(-2*time.Minute), now.Add(-2*time.Minute), `{}`), // out of order
		},
		Issue:    a2OpenIssue(),
		Task:     a2Task("running"),
		Dispatch: dispatch,
		Agent:    agentRow,
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(in)
	if !ok {
		t.Fatalf("expected pane")
	}
	// Anchor is the last OBSERVED event (seed 11), regardless of input order.
	if pane.SourceEventID != a2UUID(11).String() {
		t.Fatalf("source_event_id = %q, want anchor %q", pane.SourceEventID, a2UUID(11).String())
	}
	if pane.EmployeeID != a2UUID(4).String() {
		t.Fatalf("employee_id = %q, want dispatch-bound agent %q", pane.EmployeeID, a2UUID(4).String())
	}
	if pane.EmployeeName != "Shard" {
		t.Fatalf("employee_name = %q, want Shard", pane.EmployeeName)
	}
	if pane.DispatchCommandID != a2UUID(9).String() {
		t.Fatalf("dispatch_command_id = %q", pane.DispatchCommandID)
	}
	// Reordering the input slice must not move the anchor: stable projection.
	shuffled := in
	shuffled.Events = []db.WorkEvent{in.Events[2], in.Events[0], in.Events[1]}
	pane2, _ := ProjectA2Pane(shuffled)
	if pane2.SourceEventID != pane.SourceEventID {
		t.Fatalf("source_event_id moved under input reordering: %q vs %q", pane2.SourceEventID, pane.SourceEventID)
	}
	if pane2.ExecutionState != pane.ExecutionState {
		t.Fatalf("execution_state moved under input reordering")
	}
}

// --- working kinds ------------------------------------------------------------

func TestProjectA2Pane_WorkingKinds(t *testing.T) {
	now := a2Now()
	tests := []struct {
		kind    string
		working bool
	}{
		{"started", true},
		{"progress", true},
		{"tool_file_scope", true},
		{"checkpoint", true},
		{"resumed", true},
		{"blocked", false},         // waiting on a blocker, not executing
		{"candidate_ready", false}, // awaiting review
		{"review_requested", false},
		{"repair_requested", false},
		{"handoff", false},
		{"abandoned_recovered", false},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			in := A2PaneInput{
				WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
				Events:   []db.WorkEvent{a2Event(10, tt.kind, now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
				Issue:    a2OpenIssue(),
				Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
				Now:      now,
			}
			pane, ok := ProjectA2Pane(in)
			if !ok {
				t.Fatalf("expected pane")
			}
			if pane.Working != tt.working {
				t.Fatalf("kind %q: working = %v, want %v", tt.kind, pane.Working, tt.working)
			}
		})
	}
}

// --- blocked summary carries the canonical blocker reason ----------------------

func TestProjectA2Pane_BlockedSummaryUsesCanonicalReason(t *testing.T) {
	now := a2Now()
	ev := a2Event(10, "blocked", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)
	ev.BlockerReason = a2Text("waiting on HIV-1234 review")
	in := A2PaneInput{
		WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
		Events:   []db.WorkEvent{ev},
		Issue:    a2OpenIssue(),
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, _ := ProjectA2Pane(in)
	if !strings.Contains(pane.ActivitySummary, "HIV-1234") {
		t.Fatalf("activity_summary = %q, want canonical blocker reason surfaced", pane.ActivitySummary)
	}
	if pane.Working {
		t.Fatalf("blocked pane must not count as working")
	}
}
