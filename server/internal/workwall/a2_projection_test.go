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

// a2ClaimReceipt is an execution-receipt claim (claimed_at set, terminal
// fields NULL): exact task evidence that is NOT terminal.
func a2ClaimReceipt() *db.ExecutionReceipt {
	return &db.ExecutionReceipt{
		TaskID:      a2UUID(3),
		WorkspaceID: a2UUID(1),
		IssueID:     a2UUID(2),
	}
}

// a2ClaimReceiptForCommand is a receipt claim naming a specific assignment
// command: the B3-4 chain anchor a bound dispatch must match.
func a2ClaimReceiptForCommand(cmd pgtype.UUID) *db.ExecutionReceipt {
	r := a2ClaimReceipt()
	r.AssignmentCommandID = cmd
	return r
}

// a2FullEvidence returns a receipt claim + dispatch pair sharing one
// assignment command, fully bound to the task/issue/workspace fixtures:
// the complete B4-1 evidence chain.
func a2FullEvidence() (*db.ExecutionReceipt, *db.AssignmentDispatchReceipt) {
	cmd := a2UUID(71)
	return a2ClaimReceiptForCommand(cmd), a2DispatchForCommand(cmd, a2UUID(4))
}

// a2DispatchForCommand builds a dispatch receipt naming a command, bound to
// the current task/issue/workspace fixtures.
func a2DispatchForCommand(cmd, agent pgtype.UUID) *db.AssignmentDispatchReceipt {
	return &db.AssignmentDispatchReceipt{
		CommandID:     cmd,
		WorkspaceID:   a2UUID(1),
		IssueID:       a2UUID(2),
		LocalAgentID:  agent,
		InitialTaskID: a2UUID(3),
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

// a2WithRefIssue returns a copy of the input whose RefIssueID matches the
// fixture Issue (a2UUID(2)) — mirroring what the service always populates.
func a2WithRefIssue(in A2PaneInput) A2PaneInput {
	in.RefIssueID = a2UUID(2).String()
	return in
}

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
			name: "progress + full task->receipt->dispatch chain + fresh heartbeat -> active and working",
			in: func() A2PaneInput {
				r, d := a2FullEvidence()
				return A2PaneInput{
					WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
					Events:   []db.WorkEvent{a2Event(10, "started", now.Add(-2*time.Minute), now.Add(-2*time.Minute), `{}`), a2Event(11, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{"stage":"build"}`)},
					Issue:    a2OpenIssue(),
					Task:     a2Task("running"),
					Receipt:  r,
					Dispatch: d,
					Presence: a2Presence("sess-1", fresh),
					Now:      now,
				}
			}(),
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
			name: "finished event + completed receipt + matching dispatch -> completed by canonical chain only",
			in: func() A2PaneInput {
				cmd := a2UUID(72)
				r := a2Receipt("completed")
				r.AssignmentCommandID = cmd
				r.WorkspaceID = a2UUID(1)
				return A2PaneInput{
					WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
					Events:   []db.WorkEvent{a2Event(10, "finished", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
					Issue:    a2OpenIssue(),
					Task:     a2Task("completed"),
					Receipt:  r,
					Dispatch: a2DispatchForCommand(cmd, a2UUID(4)),
					Now:      now,
				}
			}(),
			want: A2ExecutionCompleted,
			work: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane, ok := ProjectA2Pane(a2WithRefIssue(tt.in))
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
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
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
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
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
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
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
	r, d := a2FullEvidence()
	in := A2PaneInput{
		WorkRef: "hivecrew://ws/work/prj/issue-1/task-1",
		Events: []db.WorkEvent{
			a2Event(10, "progress", now.Add(-2*time.Minute), now.Add(-2*time.Minute), `{}`),
			// The payload text claims completion; the only receipt is a
			// nonterminal claim and the issue is open.
			a2Event(11, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), term),
		},
		Issue:    a2OpenIssue(),
		Task:     a2Task("running"),
		Receipt:  r,
		Dispatch: d,
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
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
	first, ok := ProjectA2Pane(a2WithRefIssue(in))
	if !ok {
		t.Fatalf("expected pane")
	}
	second, ok := ProjectA2Pane(a2WithRefIssue(in))
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
	pane, ok := ProjectA2Pane(a2WithRefIssue(dup))
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
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
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
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.SurfaceKind != A2SurfaceTerminal {
			t.Fatalf("surface = %q, want terminal", pane.SurfaceKind)
		}
		if pane.LastHeartbeatAt == nil {
			t.Fatalf("matched presence must surface its heartbeat time")
		}
	})

	t.Run("api-only route (no presence) -> event_console, never fake terminal", func(t *testing.T) {
		in := base()
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.SurfaceKind != A2SurfaceEventConsole {
			t.Fatalf("surface = %q, want event_console", pane.SurfaceKind)
		}
	})

	t.Run("matched but stale presence -> terminal with stale freshness, not working", func(t *testing.T) {
		in := base()
		in.Presence = a2Presence("sess-1", now.Add(-10*time.Minute))
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
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
		Receipt:  a2ClaimReceiptForCommand(a2UUID(9)),
		Dispatch: dispatch,
		Agent:    agentRow,
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
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
	pane2, _ := ProjectA2Pane(a2WithRefIssue(shuffled))
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
	fullR, fullD := a2FullEvidence()
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			in := A2PaneInput{
				WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
				Events:   []db.WorkEvent{a2Event(10, tt.kind, now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
				Issue:    a2OpenIssue(),
				Task:     a2Task("running"),
				Receipt:  fullR,
				Dispatch: fullD,
				Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
				Now:      now,
			}
			pane, ok := ProjectA2Pane(a2WithRefIssue(in))
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
	pane, _ := ProjectA2Pane(a2WithRefIssue(in))
	if !strings.Contains(pane.ActivitySummary, "HIV-1234") {
		t.Fatalf("activity_summary = %q, want canonical blocker reason surfaced", pane.ActivitySummary)
	}
	if pane.Working {
		t.Fatalf("blocked pane must not count as working")
	}
}

// --- B2 rework: workspace/project drift, tenant-scoped reads, precise binding --

// A work_ref whose embedded workspace differs from the requesting workspace is
// foreign data: the pane must be skipped entirely (fail-closed, never render).
func TestProjectA2Pane_WorkspaceDriftFailsClosed(t *testing.T) {
	now := a2Now()
	in := A2PaneInput{
		WorkRef:            "hivecrew://ws-b/work/prj/issue-1/task-1",
		RequestWorkspaceID: a2UUID(1).String(),
		RefWorkspaceID:     a2UUID(99).String(), // != requesting workspace
		Events:             []db.WorkEvent{a2Event(10, "progress", now.Add(-time.Minute), now.Add(-time.Minute), `{}`)},
		Issue:              a2OpenIssue(),
		Task:               a2Task("running"),
		Presence:           a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:                now,
	}
	if pane, ok := ProjectA2Pane(a2WithRefIssue(in)); ok {
		t.Fatalf("workspace drift must fail closed and skip the pane, got %+v", pane)
	}
}

func TestProjectA2Pane_ProjectDriftIsMismatch(t *testing.T) {
	now := a2Now()
	issue := a2OpenIssue()
	issue.ProjectID = a2UUID(21) // issue authority says project P2

	newInput := func() A2PaneInput {
		return A2PaneInput{
			WorkRef:            "hivecrew://ws/work/" + a2UUID(20).String() + "/issue-1/task-1",
			RequestWorkspaceID: a2UUID(1).String(),
			RefWorkspaceID:     a2UUID(1).String(),  // embedded workspace matches the request
			RefProjectID:       a2UUID(20).String(), // work_ref claims P1
			Events:             []db.WorkEvent{a2Event(10, "progress", now.Add(-time.Minute), now.Add(-time.Minute), `{}`)},
			Issue:              issue,
			Task:               a2Task("running"),
			Presence:           a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:                now,
		}
	}

	t.Run("drift with live evidence -> issue_state_mismatch, never working", func(t *testing.T) {
		pane, ok := ProjectA2Pane(a2WithRefIssue(newInput()))
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.ExecutionState != A2ExecutionIssueMismatch {
			t.Fatalf("execution_state = %q, want issue_state_mismatch", pane.ExecutionState)
		}
		if pane.Working {
			t.Fatalf("project drift must never count as working")
		}
	})

	t.Run("drift outranks canonical completion", func(t *testing.T) {
		in := newInput()
		in.Receipt = a2Receipt("completed")
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.ExecutionState != A2ExecutionIssueMismatch {
			t.Fatalf("execution_state = %q, want issue_state_mismatch (drift fails closed first)", pane.ExecutionState)
		}
		if pane.CompletedAt != nil {
			t.Fatalf("drifted pane must never carry completed_at")
		}
	})

	t.Run("project-bearing issue: missing or malformed claim IS drift (B4-3)", func(t *testing.T) {
		for _, ref := range []string{
			"hivecrew://ws/work/inbox",       // missing claim against a project-bearing issue
			"hivecrew://ws/work/prj/issue-1", // malformed (non-UUID) claim
		} {
			in := newInput()
			in.WorkRef = ref
			in.RefProjectID = ""
			pane, ok := ProjectA2Pane(a2WithRefIssue(in))
			if !ok || pane.ExecutionState != A2ExecutionIssueMismatch {
				t.Fatalf("ref %q against a project-bearing issue must be issue_state_mismatch (%+v)", ref, pane)
			}
			if pane.Working {
				t.Fatalf("project drift must never be working")
			}
		}
	})

	t.Run("issue without project authority: inbox or missing claim passes", func(t *testing.T) {
		// Only an issue with NO project assigned may accept an inbox-style ref.
		projectless := a2OpenIssue() // ProjectID not set -> no authority
		for _, ref := range []string{
			"hivecrew://ws/work/inbox",
			"hivecrew://ws/work/prj/issue-1",
		} {
			in := newInput()
			in.WorkRef = ref
			in.RefProjectID = ""
			in.Issue = projectless
			r, d := a2FullEvidence()
			in.Receipt, in.Dispatch = r, d
			pane, ok := ProjectA2Pane(a2WithRefIssue(in))
			if !ok || pane.ExecutionState == A2ExecutionIssueMismatch {
				t.Fatalf("ref %q against a projectless issue must not be flagged as drift (%+v)", ref, pane)
			}
		}
	})
}

// A later re-dispatch of the same issue must never overwrite the current
// work_ref's accountable employee: only a dispatch receipt precisely bound to
// THIS task (initial_task_id) may carry ownership.
func TestProjectA2Pane_LaterRedispatchDoesNotHijackEmployee(t *testing.T) {
	now := a2Now()
	currentTask := a2Task("running") // task-1, agent a2UUID(4)
	redispatch := &db.AssignmentDispatchReceipt{
		CommandID:     a2UUID(31),
		WorkspaceID:   a2UUID(1),
		IssueID:       a2UUID(2),
		LocalAgentID:  a2UUID(41), // a different employee
		InitialTaskID: a2UUID(42), // bound to task-2, NOT the current task
	}
	in := A2PaneInput{
		WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
		Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-time.Minute), now.Add(-time.Minute), `{}`)},
		Issue:    a2OpenIssue(),
		Task:     currentTask,
		Receipt:  a2ClaimReceipt(), // exact evidence for THIS task
		Dispatch: redispatch,       // wrong (later) dispatch passed in
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
	if !ok {
		t.Fatalf("expected pane")
	}
	if pane.EmployeeID != a2UUID(4).String() {
		t.Fatalf("employee_id = %q, want the current task's agent %q (later re-dispatch must not hijack)", pane.EmployeeID, a2UUID(4).String())
	}
	if pane.DispatchCommandID != "" {
		t.Fatalf("dispatch_command_id = %q, want empty (receipt is not bound to this task)", pane.DispatchCommandID)
	}
	// B4-1 flip: the only dispatch present belongs to task-2, so the full
	// task->receipt->dispatch chain is broken and the pane is non-active and
	// non-working even with a fresh heartbeat. Employee attribution (the
	// point of this test) still resolves to the task's agent.
	if pane.Working {
		t.Fatalf("receipt without a task-matching dispatch must never count as working")
	}
	if pane.ExecutionState == A2ExecutionActive {
		t.Fatalf("receipt without a task-matching dispatch must never be active")
	}
}

// Precisely matched dispatch (initial_task_id == this task) still binds.
func TestProjectA2Pane_MatchedDispatchBindsOwnership(t *testing.T) {
	now := a2Now()
	matched := &db.AssignmentDispatchReceipt{
		CommandID:     a2UUID(51),
		WorkspaceID:   a2UUID(1),
		IssueID:       a2UUID(2),
		LocalAgentID:  a2UUID(4), // same agent as the task
		InitialTaskID: a2UUID(3), // == task id from a2Task()
	}
	in := A2PaneInput{
		WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
		Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
		Issue:    a2OpenIssue(),
		Task:     a2Task("running"),
		Receipt:  a2ClaimReceiptForCommand(a2UUID(51)), // names the same command
		Dispatch: matched,
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
	if !ok {
		t.Fatalf("expected pane")
	}
	if pane.DispatchCommandID != a2UUID(51).String() {
		t.Fatalf("dispatch_command_id = %q, want the matched command", pane.DispatchCommandID)
	}
	if pane.EmployeeID != a2UUID(4).String() {
		t.Fatalf("employee_id = %q", pane.EmployeeID)
	}
}

// A receipt row belonging to another workspace is foreign terminal evidence
// and must be ignored, never establishing completion.
func TestProjectA2Pane_ForeignWorkspaceReceiptIgnored(t *testing.T) {
	now := a2Now()
	foreign := a2Receipt("completed")
	foreign.WorkspaceID = a2UUID(98) // != requesting workspace
	in := A2PaneInput{
		WorkRef:            "hivecrew://ws/work/prj/issue-1/task-1",
		RequestWorkspaceID: a2UUID(1).String(),
		RefWorkspaceID:     a2UUID(1).String(),
		Events:             []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
		Issue:              a2OpenIssue(),
		Task:               a2Task("running"),
		Receipt:            foreign,
		Presence:           a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:                now,
	}
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
	if !ok {
		t.Fatalf("expected pane")
	}
	if pane.ExecutionState == A2ExecutionCompleted {
		t.Fatalf("foreign-workspace receipt must never establish completion")
	}
	if pane.CompletedAt != nil {
		t.Fatalf("foreign receipt must not set completed_at")
	}
}

// --- B2 blockers 4+5: strict working evidence + display sanitization -------

// Event + fresh heartbeat alone must NEVER count as working: without a
// current nonterminal tenant-bound task and exact dispatch/receipt evidence,
// the pane stays active-looking but not working.
func TestProjectA2Pane_NoTaskOrNoEvidenceNeverWorking(t *testing.T) {
	now := a2Now()
	base := func() A2PaneInput {
		return A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-time.Minute), now.Add(-time.Minute), `{}`)},
			Issue:    a2OpenIssue(),
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
	}

	t.Run("no task row at all", func(t *testing.T) {
		pane, ok := ProjectA2Pane(a2WithRefIssue(base()))
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.Working {
			t.Fatalf("event + heartbeat without a task must never count as working")
		}
		// B3-1: no task means the in-flight claim is unconfirmed — never
		// active, even with a fresh heartbeat.
		if pane.ExecutionState != A2ExecutionIssueMismatch {
			t.Fatalf("execution_state = %q, want issue_state_mismatch", pane.ExecutionState)
		}
		if pane.TaskID != "" {
			t.Fatalf("no task: task_id must stay empty, got %q", pane.TaskID)
		}
	})

	t.Run("task present but zero dispatch/receipt evidence", func(t *testing.T) {
		in := base()
		in.Task = a2Task("running")
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.Working {
			t.Fatalf("event + heartbeat + bare task without exact dispatch/receipt evidence must never count as working")
		}
	})

	t.Run("receipt without a matching dispatch is NOT evidence (B4-1)", func(t *testing.T) {
		in := base()
		in.Task = a2Task("running")
		in.Receipt = a2ClaimReceipt()
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.Working {
			t.Fatalf("receipt without its dispatch must never count as working")
		}
		if pane.ExecutionState == A2ExecutionActive {
			t.Fatalf("receipt without its dispatch must never be active")
		}
	})

	t.Run("full receipt->dispatch chain is exact evidence", func(t *testing.T) {
		in := base()
		in.Task = a2Task("running")
		in.Receipt, in.Dispatch = a2FullEvidence()
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if !pane.Working {
			t.Fatalf("task + full chain + fresh heartbeat should count as working")
		}
		if pane.ExecutionState != A2ExecutionActive {
			t.Fatalf("execution_state = %q, want active", pane.ExecutionState)
		}
	})

	t.Run("terminal task with a bound dispatch is never working", func(t *testing.T) {
		in := base()
		in.Task = a2Task("completed")
		in.Receipt = a2Receipt("completed")
		in.Dispatch = &db.AssignmentDispatchReceipt{
			CommandID:     a2UUID(51),
			WorkspaceID:   a2UUID(1),
			IssueID:       a2UUID(2),
			LocalAgentID:  a2UUID(4),
			InitialTaskID: a2UUID(3),
		}
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.Working {
			t.Fatalf("terminal task + terminal dispatch must never count as working")
		}
		if pane.ExecutionState != A2ExecutionCompleted {
			t.Fatalf("execution_state = %q, want completed", pane.ExecutionState)
		}
	})

	t.Run("dispatch bound to a terminal task claim still not working", func(t *testing.T) {
		in := base()
		in.Task = a2Task("failed")
		in.Receipt = a2Receipt("failed")
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.Working {
			t.Fatalf("failed task must never count as working")
		}
	})
}

// Credential-like synthetic values inside the allowlisted stage/phase/step
// keys and blocker_reason must never surface in a pane.
func TestProjectA2Pane_DisplayValueSanitization(t *testing.T) {
	now := a2Now()
	payload := `{"stage":"sk-SECRET-SENTINEL-5e11",` +
		`"phase":"ghp_TOKEN-SENTINEL-3d10",` +
		`"step":"hexdump-0123456789abcdef0123456789abcdef01234567",` +
		`"note":"built fine"}`
	in := A2PaneInput{
		WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
		Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), payload)},
		Issue:    a2OpenIssue(),
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
	if !ok {
		t.Fatalf("expected pane")
	}
	blob, err := json.Marshal(pane)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, sentinel := range []string{
		"sk-SECRET-SENTINEL-5e11",
		"ghp_TOKEN-SENTINEL-3d10",
		"0123456789abcdef0123456789abcdef01234567",
	} {
		if strings.Contains(string(blob), sentinel) {
			t.Fatalf("credential-shaped allowlist value leaked into the pane: %q", sentinel)
		}
	}
}

// A credential-shaped blocker_reason must also never surface; a clean one
// still does.
func TestProjectA2Pane_BlockerReasonSanitization(t *testing.T) {
	now := a2Now()

	blocked := func(reason string) db.WorkEvent {
		ev := a2Event(10, "blocked", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)
		ev.BlockerReason = a2Text(reason)
		return ev
	}
	build := func(ev db.WorkEvent) A2PaneV1 {
		in := A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{ev},
			Issue:    a2OpenIssue(),
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
		if !ok {
			t.Fatalf("expected pane")
		}
		return pane
	}

	t.Run("clean reason surfaces", func(t *testing.T) {
		pane := build(blocked("waiting on HIV-1234 review"))
		if !strings.Contains(pane.ActivitySummary, "HIV-1234") {
			t.Fatalf("activity_summary = %q, want clean reason kept", pane.ActivitySummary)
		}
	})

	t.Run("credential-shaped reason dropped", func(t *testing.T) {
		pane := build(blocked("password: PASSWORD-SENTINEL-c0de"))
		blob, _ := json.Marshal(pane)
		if strings.Contains(string(blob), "PASSWORD-SENTINEL-c0de") {
			t.Fatalf("credential-shaped blocker_reason leaked: %q", pane.ActivitySummary)
		}
		if strings.Contains(strings.ToLower(pane.ActivitySummary), "password") {
			t.Fatalf("sanitized summary still mentions the marker: %q", pane.ActivitySummary)
		}
	})

	t.Run("token-shaped reason dropped", func(t *testing.T) {
		pane := build(blocked("retry key AKIAIOSFODNN7EXAMPLE-a1b2"))
		blob, _ := json.Marshal(pane)
		if strings.Contains(string(blob), "AKIAIOSFODNN7EXAMPLE") {
			t.Fatalf("token-shaped blocker_reason leaked: %q", pane.ActivitySummary)
		}
	})
}

// --- B3 blockers: strict active gate, read boundary, cross-issue, dispatch/receipt command ---

// B3-1: an in-flight event plus a fresh heartbeat, with a nonterminal task
// but NO exact evidence, must be non-active AND non-working.
func TestProjectA2Pane_ActiveRequiresTaskAndEvidence(t *testing.T) {
	now := a2Now()
	base := func() A2PaneInput {
		return A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-time.Minute), now.Add(-time.Minute), `{}`)},
			Issue:    a2OpenIssue(),
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
	}

	t.Run("task without receipt: never active", func(t *testing.T) {
		in := base()
		in.Task = a2Task("running")
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.ExecutionState == A2ExecutionActive {
			t.Fatalf("in-flight claim without receipt evidence must never be active")
		}
		if pane.Working {
			t.Fatalf("never working without evidence")
		}
	})

	t.Run("full chain + nonterminal task + fresh heartbeat: active", func(t *testing.T) {
		in := base()
		in.Task = a2Task("running")
		in.Receipt, in.Dispatch = a2FullEvidence()
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.ExecutionState != A2ExecutionActive {
			t.Fatalf("execution_state = %q, want active", pane.ExecutionState)
		}
		if !pane.Working {
			t.Fatalf("full chain should count as working")
		}
	})

	t.Run("receipt claim + TERMINAL task: never active, never working", func(t *testing.T) {
		in := base()
		in.Task = a2Task("completed")
		in.Receipt = a2ClaimReceipt()
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.ExecutionState == A2ExecutionActive || pane.Working {
			t.Fatalf("terminal task must end active/working, got %q/%v", pane.ExecutionState, pane.Working)
		}
	})
}

// B3-3: a task row pointing at a DIFFERENT issue than the work_ref's issue
// must fail closed: no task fields, no employee from it, no evidence from it.
func TestProjectA2Pane_CrossIssueTaskFailsClosed(t *testing.T) {
	now := a2Now()
	cmd := a2UUID(51)
	crossTask := a2Task("running")
	crossTask.IssueID = a2UUID(77) // belongs to another issue

	in := A2PaneInput{
		WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
		Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-time.Minute), now.Add(-time.Minute), `{}`)},
		Issue:    a2OpenIssue(), // issue-1 (a2UUID(2))
		Task:     crossTask,
		Receipt:  a2ClaimReceiptForCommand(cmd),
		Dispatch: a2DispatchForCommand(cmd, a2UUID(4)),
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
	if !ok {
		t.Fatalf("expected pane")
	}
	if pane.TaskID != "" {
		t.Fatalf("cross-issue task id leaked into pane: %q", pane.TaskID)
	}
	for _, ref := range pane.SourceRefs {
		if strings.HasPrefix(ref, "task://") {
			t.Fatalf("cross-issue task must not appear in source_refs: %v", pane.SourceRefs)
		}
	}
	if pane.EmployeeID != "" {
		t.Fatalf("cross-issue task must not set the employee, got %q", pane.EmployeeID)
	}
	if pane.ExecutionState == A2ExecutionActive || pane.Working {
		t.Fatalf("cross-issue task must fail closed (state %q, working %v)", pane.ExecutionState, pane.Working)
	}

	t.Run("issue missing entirely also fails closed", func(t *testing.T) {
		in := in
		in.Issue = nil
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.TaskID != "" || pane.Working {
			t.Fatalf("unverifiable issue must drop the task: %+v", pane)
		}
	})
}

// B3-4: a dispatch that is task/issue/workspace-correct but whose command is
// NOT the canonical receipt's command can own neither the employee nor
// establish evidence.
func TestProjectA2Pane_DispatchMustMatchReceiptCommand(t *testing.T) {
	now := a2Now()
	in := A2PaneInput{
		WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
		Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
		Issue:    a2OpenIssue(),
		Task:     a2Task("running"),
		Receipt:  a2ClaimReceiptForCommand(a2UUID(51)),         // canonical command C1
		Dispatch: a2DispatchForCommand(a2UUID(52), a2UUID(41)), // C2: different command/agent
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
	if !ok {
		t.Fatalf("expected pane")
	}
	if pane.DispatchCommandID != "" {
		t.Fatalf("dispatch naming a different command must not be exposed, got %q", pane.DispatchCommandID)
	}
	if pane.EmployeeID == a2UUID(41).String() {
		t.Fatalf("mismatched dispatch must not own the employee")
	}
	// The receipt itself is still exact evidence, so the pane may be active
	// with the TASK's agent (fallback), but never via the rogue dispatch.
	if pane.EmployeeID != a2UUID(4).String() {
		t.Fatalf("employee = %q, want task fallback %q", pane.EmployeeID, a2UUID(4).String())
	}

	t.Run("dispatch without any receipt cannot own or evidence", func(t *testing.T) {
		in := in
		in.Receipt = nil
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.DispatchCommandID != "" {
			t.Fatalf("dispatch without receipt command must not be exposed")
		}
		if pane.EmployeeID == a2UUID(41).String() {
			t.Fatalf("dispatch without receipt must not own the employee")
		}
		if pane.ExecutionState == A2ExecutionActive || pane.Working {
			t.Fatalf("dispatch without receipt is not evidence (state %q, working %v)", pane.ExecutionState, pane.Working)
		}
	})

	t.Run("matching command still binds", func(t *testing.T) {
		in := in
		in.Dispatch = a2DispatchForCommand(a2UUID(51), a2UUID(4))
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.DispatchCommandID != a2UUID(51).String() {
			t.Fatalf("matching dispatch should bind, got %q", pane.DispatchCommandID)
		}
		if !pane.Working {
			t.Fatalf("full matched chain should be working")
		}
	})
}

// B3-2 (pure half): a receipt whose task id belongs to a foreign/missing
// tenant task is never canonical — no terminal state, no completion, no
// evidence — even when its workspace string matches the request.
func TestProjectA2Pane_UnscopedReceiptNeverCanonical(t *testing.T) {
	now := a2Now()
	terminal := a2Receipt("completed")
	terminal.WorkspaceID = a2UUID(1) // workspace string matches...
	terminal.TaskID = a2UUID(88)     // ...but the task is not this pane's task

	in := A2PaneInput{
		WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
		Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
		Issue:    a2OpenIssue(),
		Task:     a2Task("running"),
		Receipt:  terminal,
		Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
		Now:      now,
	}
	pane, ok := ProjectA2Pane(a2WithRefIssue(in))
	if !ok {
		t.Fatalf("expected pane")
	}
	if pane.ExecutionState == A2ExecutionCompleted || pane.CompletedAt != nil {
		t.Fatalf("receipt for a different task must never complete this pane (%q/%v)", pane.ExecutionState, pane.CompletedAt)
	}
}

// --- B4 blockers: full-chain evidence, receipt boundary, project claim, dispatch issue, agent name ---

// B4-1: every hop of Task -> Receipt.task_id -> Receipt.assignment_command_id
// -> Dispatch.command_id -> Dispatch.initial_task_id -> Task must hold; a
// break anywhere yields a non-active, non-working pane.
func TestProjectA2Pane_FullChainEvidenceRequired(t *testing.T) {
	now := a2Now()
	build := func(mutate func(*A2PaneInput)) A2PaneV1 {
		r, d := a2FullEvidence()
		in := A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-time.Minute), now.Add(-time.Minute), `{}`)},
			Issue:    a2OpenIssue(),
			Task:     a2Task("running"),
			Receipt:  r,
			Dispatch: d,
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
		if mutate != nil {
			mutate(&in)
		}
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
		if !ok {
			t.Fatalf("expected pane")
		}
		return pane
	}
	mustFail := func(name string, mutate func(*A2PaneInput)) {
		t.Run(name, func(t *testing.T) {
			pane := build(mutate)
			if pane.ExecutionState == A2ExecutionActive || pane.Working {
				t.Fatalf("broken chain must be non-active and non-working, got %q/%v", pane.ExecutionState, pane.Working)
			}
			if pane.DispatchCommandID != "" {
				t.Fatalf("broken chain must expose no dispatch command")
			}
		})
	}

	mustFail("no dispatch at all", func(in *A2PaneInput) { in.Dispatch = nil })
	mustFail("receipt without assignment command", func(in *A2PaneInput) {
		in.Receipt.AssignmentCommandID = pgtype.UUID{}
	})
	mustFail("dispatch command mismatch", func(in *A2PaneInput) {
		in.Dispatch.CommandID = a2UUID(80)
	})
	mustFail("dispatch initial_task_id mismatch", func(in *A2PaneInput) {
		in.Dispatch.InitialTaskID = a2UUID(81)
	})
	mustFail("receipt task_id mismatch", func(in *A2PaneInput) {
		in.Receipt.TaskID = a2UUID(82)
	})
	mustFail("task replaced by another issue's task", func(in *A2PaneInput) {
		cross := a2Task("running")
		cross.IssueID = a2UUID(77)
		in.Task = cross
	})

	t.Run("intact chain stays active and working", func(t *testing.T) {
		pane := build(nil)
		if pane.ExecutionState != A2ExecutionActive || !pane.Working {
			t.Fatalf("intact chain should be active/working, got %q/%v", pane.ExecutionState, pane.Working)
		}
	})
}

// B4-2: with no Task or no Issue, a receipt may never establish completed or
// working — the pane stays non-active.
func TestProjectA2Pane_ReceiptRejectedWithoutTaskOrIssue(t *testing.T) {
	now := a2Now()
	terminal := a2Receipt("completed")
	terminal.WorkspaceID = a2UUID(1)

	t.Run("no task: terminal receipt ignored", func(t *testing.T) {
		in := A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Issue:    a2OpenIssue(),
			Receipt:  terminal,
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.ExecutionState == A2ExecutionCompleted || pane.CompletedAt != nil {
			t.Fatalf("receipt without a task must never complete (%q/%v)", pane.ExecutionState, pane.CompletedAt)
		}
		if pane.Working || pane.ExecutionState == A2ExecutionActive {
			t.Fatalf("receipt without a task must never be active/working")
		}
	})

	t.Run("no issue: terminal receipt ignored", func(t *testing.T) {
		in := A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Task:     a2Task("running"),
			Receipt:  terminal,
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.ExecutionState == A2ExecutionCompleted || pane.CompletedAt != nil {
			t.Fatalf("receipt without an issue must never complete (%q/%v)", pane.ExecutionState, pane.CompletedAt)
		}
		if pane.Working || pane.ExecutionState == A2ExecutionActive {
			t.Fatalf("receipt without an issue must never be active/working")
		}
	})
}

// B4-4: a dispatch whose IssueID is missing (or unequal) against a known
// Issue is a mismatch and can never own the employee or bind.
func TestProjectA2Pane_DispatchIssueIDRequired(t *testing.T) {
	now := a2Now()

	build := func(d *db.AssignmentDispatchReceipt) A2PaneV1 {
		cmd := a2UUID(83)
		in := A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Issue:    a2OpenIssue(),
			Task:     a2Task("running"),
			Receipt:  a2ClaimReceiptForCommand(cmd),
			Dispatch: d,
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
		if !ok {
			t.Fatalf("expected pane")
		}
		return pane
	}

	t.Run("missing dispatch IssueID is a mismatch", func(t *testing.T) {
		cmd := a2UUID(83)
		d := a2DispatchForCommand(cmd, a2UUID(4))
		d.IssueID = pgtype.UUID{} // missing
		pane := build(d)
		if pane.DispatchCommandID != "" {
			t.Fatalf("dispatch without an issue must not bind")
		}
		if pane.ExecutionState == A2ExecutionActive || pane.Working {
			t.Fatalf("dispatch without an issue must not be evidence (%q/%v)", pane.ExecutionState, pane.Working)
		}
	})

	t.Run("unequal dispatch IssueID is a mismatch", func(t *testing.T) {
		cmd := a2UUID(83)
		d := a2DispatchForCommand(cmd, a2UUID(4))
		d.IssueID = a2UUID(84) // different issue
		pane := build(d)
		if pane.DispatchCommandID != "" {
			t.Fatalf("cross-issue dispatch must not bind")
		}
		if pane.ExecutionState == A2ExecutionActive || pane.Working {
			t.Fatalf("cross-issue dispatch must not be evidence (%q/%v)", pane.ExecutionState, pane.Working)
		}
	})

	t.Run("exact dispatch IssueID binds", func(t *testing.T) {
		cmd := a2UUID(83)
		pane := build(a2DispatchForCommand(cmd, a2UUID(4)))
		if pane.DispatchCommandID == "" {
			t.Fatalf("exact-issue dispatch should bind")
		}
	})
}

// B4-6 (appended review blocker): a display Agent whose ID is not exactly the
// resolved EmployeeID must never label the pane — the name stays empty.
func TestProjectA2Pane_CrossEmployeeAgentCannotPolluteName(t *testing.T) {
	now := a2Now()
	r, d := a2FullEvidence()

	t.Run("foreign agent row: name stays empty", func(t *testing.T) {
		in := A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Issue:    a2OpenIssue(),
			Task:     a2Task("running"), // agent a2UUID(4)
			Receipt:  r,
			Dispatch: d,
			Agent:    &db.Agent{ID: a2UUID(90), WorkspaceID: a2UUID(1), Name: "Impostor"},
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
		pane, ok := ProjectA2Pane(a2WithRefIssue(in))
		if !ok {
			t.Fatalf("expected pane")
		}
		if pane.EmployeeID != a2UUID(4).String() {
			t.Fatalf("employee_id = %q, want the dispatch-bound agent", pane.EmployeeID)
		}
		if pane.EmployeeName == "Impostor" {
			t.Fatalf("cross-employee agent name polluted the pane")
		}
		if pane.EmployeeName != "" {
			t.Fatalf("unverified agent name must stay empty, got %q", pane.EmployeeName)
		}
	})

	t.Run("matching agent row still labels", func(t *testing.T) {
		in := A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Issue:    a2OpenIssue(),
			Task:     a2Task("running"),
			Receipt:  r,
			Dispatch: d,
			Agent:    &db.Agent{ID: a2UUID(4), WorkspaceID: a2UUID(1), Name: "Shard"},
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.EmployeeName != "Shard" {
			t.Fatalf("verified agent name should label the pane, got %q", pane.EmployeeName)
		}
	})

	t.Run("agent with invalid ID never labels", func(t *testing.T) {
		in := A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Issue:    a2OpenIssue(),
			Task:     a2Task("running"),
			Receipt:  r,
			Dispatch: d,
			Agent:    &db.Agent{WorkspaceID: a2UUID(1), Name: "Ghost"},
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
		pane, _ := ProjectA2Pane(a2WithRefIssue(in))
		if pane.EmployeeName != "" {
			t.Fatalf("agent without a valid id must never label, got %q", pane.EmployeeName)
		}
	})
}

// --- B5 blockers: dispatch/task agent equality, strict test-DB guard, inbox-only, issue claim ---

// B5-1: a dispatch naming a DIFFERENT employee than the task's agent breaks
// the chain — never active, never working, never owning the pane.
func TestProjectA2Pane_DispatchAgentMustEqualTaskAgent(t *testing.T) {
	now := a2Now()
	build := func(dispatchAgent pgtype.UUID) A2PaneV1 {
		cmd := a2UUID(91)
		pane, _ := ProjectA2Pane(a2WithRefIssue(A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Issue:    a2OpenIssue(),
			Task:     a2Task("running"), // agent a2UUID(4)
			Receipt:  a2ClaimReceiptForCommand(cmd),
			Dispatch: a2DispatchForCommand(cmd, dispatchAgent),
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}))
		return pane
	}

	t.Run("foreign employee on the dispatch: fail closed", func(t *testing.T) {
		pane := build(a2UUID(92)) // a different employee
		if pane.DispatchCommandID != "" {
			t.Fatalf("foreign-employee dispatch must not bind")
		}
		if pane.EmployeeID == a2UUID(92).String() {
			t.Fatalf("foreign employee must never own the pane")
		}
		if pane.ExecutionState == A2ExecutionActive || pane.Working {
			t.Fatalf("foreign-employee dispatch must never be active/working (%q/%v)", pane.ExecutionState, pane.Working)
		}
	})

	t.Run("missing dispatch agent id: fail closed", func(t *testing.T) {
		pane := build(pgtype.UUID{})
		if pane.Working || pane.ExecutionState == A2ExecutionActive {
			t.Fatalf("dispatch without an agent id must never be active/working")
		}
	})

	t.Run("exact task agent still binds", func(t *testing.T) {
		pane := build(a2UUID(4))
		if pane.DispatchCommandID == "" || !pane.Working {
			t.Fatalf("matching dispatch should bind and be working")
		}
		if pane.EmployeeID != a2UUID(4).String() {
			t.Fatalf("employee = %q, want task agent", pane.EmployeeID)
		}
	})
}

// B5-3: a projectless Issue accepts ONLY a missing or reserved-inbox project
// claim; any other concrete claim (including another project's uuid or a
// random word) is drift.
func TestProjectA2Pane_ProjectlessIssueInboxOnly(t *testing.T) {
	now := a2Now()
	projectless := a2OpenIssue() // ProjectID unset

	build := func(claim string) A2PaneV1 {
		r, d := a2FullEvidence()
		in := a2WithRefIssue(A2PaneInput{
			WorkRef:  "hivecrew://ws/work/" + claim + "/issue-1/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Issue:    projectless,
			Task:     a2Task("running"),
			Receipt:  r,
			Dispatch: d,
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		})
		in.RefProjectID = claim
		pane, _ := ProjectA2Pane(in)
		return pane
	}

	for _, ok := range []string{"", "inbox", "INBOX"} {
		t.Run("allowed claim "+ok, func(t *testing.T) {
			pane := build(ok)
			if pane.ExecutionState == A2ExecutionIssueMismatch {
				t.Fatalf("claim %q against a projectless issue must not be drift", ok)
			}
		})
	}
	for _, bad := range []string{"prj", a2UUID(20).String(), "personal", "contest"} {
		t.Run("rejected claim "+bad, func(t *testing.T) {
			pane := build(bad)
			if pane.ExecutionState != A2ExecutionIssueMismatch {
				t.Fatalf("claim %q against a projectless issue must be issue_state_mismatch, got %q", bad, pane.ExecutionState)
			}
			if pane.Working {
				t.Fatalf("unattributable claim must never be working")
			}
		})
	}
}

// B5-6: with a known Issue, the ref's embedded issue claim must be present
// and exactly equal; missing, malformed, or unequal fails closed.
func TestProjectA2Pane_RefIssueClaimMustMatch(t *testing.T) {
	now := a2Now()
	r, d := a2FullEvidence()
	build := func(claim string) A2PaneV1 {
		in := A2PaneInput{
			WorkRef:  "hivecrew://ws/work/prj/" + claim + "/task-1",
			Events:   []db.WorkEvent{a2Event(10, "progress", now.Add(-1*time.Minute), now.Add(-1*time.Minute), `{}`)},
			Issue:    a2OpenIssue(), // a2UUID(2)
			Task:     a2Task("running"),
			Receipt:  r,
			Dispatch: d,
			Presence: a2Presence("sess-1", now.Add(-5*time.Second)),
			Now:      now,
		}
		in.RefIssueID = claim
		pane, _ := ProjectA2Pane(in)
		return pane
	}

	for _, bad := range []string{"", "issue-1", a2UUID(93).String()} {
		t.Run("rejected claim "+bad, func(t *testing.T) {
			pane := build(bad)
			if pane.ExecutionState != A2ExecutionIssueMismatch {
				t.Fatalf("issue claim %q must be issue_state_mismatch, got %q", bad, pane.ExecutionState)
			}
			if pane.Working {
				t.Fatalf("issue-claim mismatch must never be working")
			}
		})
	}

	t.Run("exact claim passes", func(t *testing.T) {
		pane := build(a2UUID(2).String())
		if pane.ExecutionState == A2ExecutionIssueMismatch {
			t.Fatalf("exact issue claim must not be flagged")
		}
		if !pane.Working {
			t.Fatalf("full chain with exact claim should be working")
		}
	})
}
