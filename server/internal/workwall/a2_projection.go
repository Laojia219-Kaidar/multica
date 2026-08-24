package workwall

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/liveactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A2PaneSchemaV1 is the wire schema version for A2PaneV1.
const A2PaneSchemaV1 = "hivecrew.workwall.a2-pane.v1"

// A2ExecutionState is the closed execution-state enum of the A2 Work Wall
// projection. Only canonical evidence (work_event ledger arrival order plus
// Issue/Task/Assignment/Run/Receipt read models) may set these values.
type A2ExecutionState string

const (
	// A2ExecutionActive means the ledger shows in-flight work. It still only
	// counts as working with a fresh, session-matched heartbeat.
	A2ExecutionActive A2ExecutionState = "active"
	// A2ExecutionReplay marks a late or duplicated delivery that replays
	// already-terminated history. It never counts as working.
	A2ExecutionReplay A2ExecutionState = "replay"
	// A2ExecutionFailed comes only from canonical failure evidence
	// (execution_receipt / agent_task_queue).
	A2ExecutionFailed A2ExecutionState = "failed"
	// A2ExecutionCancelled comes only from canonical cancellation evidence
	// (execution_receipt / agent_task_queue / issue).
	A2ExecutionCancelled A2ExecutionState = "cancelled"
	// A2ExecutionIssueMismatch means a ledger claim (e.g. a finished event)
	// is not confirmed by the Issue state authority. It never counts as
	// working and never establishes completion.
	A2ExecutionIssueMismatch A2ExecutionState = "issue_state_mismatch"
	// A2ExecutionCompleted means completion was established by a canonical
	// receipt/task/issue authority only — terminal text never gets here.
	A2ExecutionCompleted A2ExecutionState = "completed"
)

// A2SurfaceKind describes how the pane's activity is observed.
type A2SurfaceKind string

const (
	// A2SurfaceTerminal is used only when a session-matched terminal_presence
	// row backs the pane.
	A2SurfaceTerminal A2SurfaceKind = "terminal"
	// A2SurfaceEventConsole is the honest surface for API-only routes: they
	// have no terminal, and the projection must never fake one.
	A2SurfaceEventConsole A2SurfaceKind = "event_console"
)

// A2PaneV1 is the projected Work Wall pane for one work_ref execution. It is
// a strict allowlist projection: event payloads are never copied (only a few
// structured, sanitized summary fields survive), so secrets, raw stdout or
// transcripts cannot reach the wire.
type A2PaneV1 struct {
	SchemaVersion string `json:"schema_version"`
	WorkspaceID   string `json:"workspace_id"`
	WorkRef       string `json:"work_ref"`
	// SourceEventID is the stable id of the canonical work_event row that
	// anchors the pane (the latest observed event of the work_ref).
	SourceEventID string `json:"source_event_id"`
	SessionID     string `json:"session_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`

	// Dispatch-to-employee ownership: the assignment dispatch receipt binds
	// the work to the accountable digital employee (agent).
	EmployeeID        string `json:"employee_id,omitempty"`
	EmployeeName      string `json:"employee_name,omitempty"`
	DispatchCommandID string `json:"dispatch_command_id,omitempty"`

	ProjectID  string `json:"project_id,omitempty"`
	IssueID    string `json:"issue_id,omitempty"`
	IssueState string `json:"issue_state,omitempty"`
	TaskID     string `json:"task_id,omitempty"`

	ExecutionState A2ExecutionState            `json:"execution_state"`
	Working        bool                        `json:"working"`
	SurfaceKind    A2SurfaceKind               `json:"surface_kind"`
	Freshness      liveactivity.FreshnessState `json:"freshness_state"`

	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	LastEventAt     *time.Time `json:"last_event_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	ObservedAt      time.Time  `json:"observed_at"`

	ActivityKind    string   `json:"activity_kind,omitempty"`
	ActivitySummary string   `json:"activity_summary,omitempty"`
	SourceRefs      []string `json:"source_refs"`
}

// A2PaneInput carries the canonical inputs for one work_ref. All fields are
// read models; the projection performs no I/O and mutates nothing.
type A2PaneInput struct {
	WorkRef        string
	Events         []db.WorkEvent // every ledger row of the work_ref; order irrelevant
	Issue          *db.Issue      // Issue authority (nil when unresolvable)
	Task           *db.AgentTaskQueue
	Dispatch       *db.AssignmentDispatchReceipt // Assignment ownership
	Receipt        *db.ExecutionReceipt          // Receipt terminal truth
	Presence       *db.TerminalPresence          // latest session-matched terminal heartbeat
	Agent          *db.Agent                     // employee display identity
	Now            time.Time
	StaleThreshold time.Duration
}

// a2TerminalEvent is the closed event type whose arrival claims the work
// reached termination.
const a2TerminalEvent = "finished"

// a2WorkingKinds are the ledger event kinds that mean the actor is actively
// executing. Transitional kinds (blocked, candidate_ready, review_requested,
// repair_requested, handoff, abandoned_recovered) keep the pane in the active
// state but never count as working.
func a2WorkingKind(t string) bool {
	switch t {
	case "started", "progress", "tool_file_scope", "checkpoint", "resumed":
		return true
	default:
		return false
	}
}

// a2IssueTerminal reports whether the Issue state authority confirms the work
// reached completion ("done"). "cancelled" is handled separately as its own
// canonical state.
func a2IssueTerminal(status string) bool { return status == "done" }

// compareA2Observation orders two ledger rows by arrival: observed_at first
// (an invalid observed_at loses — fail-closed, it cannot prove it arrived
// later), then created_at, then id. Returns <0 when a arrived before b.
func compareA2Observation(a, b db.WorkEvent) int {
	av, bv := a.ObservedAt.Valid, b.ObservedAt.Valid
	if av != bv {
		if av {
			return 1
		}
		return -1
	}
	if av && a.ObservedAt.Time.Compare(b.ObservedAt.Time) != 0 {
		return a.ObservedAt.Time.Compare(b.ObservedAt.Time)
	}
	av, bv = a.CreatedAt.Valid, b.CreatedAt.Valid
	if av != bv {
		if av {
			return 1
		}
		return -1
	}
	if av && a.CreatedAt.Time.Compare(b.CreatedAt.Time) != 0 {
		return a.CreatedAt.Time.Compare(b.CreatedAt.Time)
	}
	return strings.Compare(uuidStr(a.ID), uuidStr(b.ID))
}

// a2AnchorEvent returns the latest-observed event of the stream (the stable
// pane anchor) and the latest-observed terminal-claim event, or nil.
func a2AnchorEvent(events []db.WorkEvent) (anchor, terminal *db.WorkEvent) {
	for i := range events {
		e := &events[i]
		if anchor == nil || compareA2Observation(*e, *anchor) > 0 {
			anchor = e
		}
		if e.EventType == a2TerminalEvent && (terminal == nil || compareA2Observation(*e, *terminal) > 0) {
			terminal = e
		}
	}
	return anchor, terminal
}

// a2HasDuplicateEventID defensively detects the same ledger row delivered
// twice in one stream (exact replay of the same event id).
func a2HasDuplicateEventID(events []db.WorkEvent) bool {
	seen := make(map[string]bool, len(events))
	for i := range events {
		k := uuidStr(events[i].ID)
		if k == "" {
			continue
		}
		if seen[k] {
			return true
		}
		seen[k] = true
	}
	return false
}

// a2IsReplay decides whether the anchored stream is a replay of
// already-terminated history: a terminal event was observed, and the anchor
// is a different event that cannot prove it occurred strictly after the
// terminal claim. Ambiguous timestamps fail closed into replay.
func a2IsReplay(anchor, terminal *db.WorkEvent, duplicateID bool) bool {
	if duplicateID {
		return true
	}
	if terminal == nil || anchor == nil || uuidStr(anchor.ID) == uuidStr(terminal.ID) {
		return false
	}
	if !anchor.OccurredAt.Valid || !terminal.OccurredAt.Valid {
		return true // cannot prove the anchor is newer — never resurrect work
	}
	return anchor.OccurredAt.Time.Compare(terminal.OccurredAt.Time) <= 0
}

// a2CanonicalTerminal maps canonical terminal evidence (receipt, then task,
// then issue cancellation) to an execution state. Terminal TEXT anywhere is
// deliberately never consulted.
func a2CanonicalTerminal(in A2PaneInput) (A2ExecutionState, bool) {
	if in.Receipt != nil && in.Receipt.TerminalStatus.Valid {
		switch in.Receipt.TerminalStatus.String {
		case "completed":
			return A2ExecutionCompleted, true
		case "failed":
			return A2ExecutionFailed, true
		case "cancelled":
			return A2ExecutionCancelled, true
		default:
			// Unrecognized terminal claim: unconfirmed by any authority.
			return A2ExecutionIssueMismatch, true
		}
	}
	if in.Task != nil {
		switch in.Task.Status {
		case "completed":
			return A2ExecutionCompleted, true
		case "failed":
			return A2ExecutionFailed, true
		case "cancelled":
			return A2ExecutionCancelled, true
		}
	}
	if in.Issue != nil && in.Issue.Status == "cancelled" {
		return A2ExecutionCancelled, true
	}
	return "", false
}

// a2Heartbeat classifies the session-matched terminal presence. Missing or
// foreign presence is treated as absent: an API-only route must surface as
// event_console and can never certify work.
func a2Heartbeat(in A2PaneInput, anchor *db.WorkEvent, threshold time.Duration) (fresh bool, surface A2SurfaceKind, freshness liveactivity.FreshnessState, at *time.Time) {
	if in.Presence == nil || anchor == nil || !anchor.SessionID.Valid || in.Presence.SessionName != anchor.SessionID.String {
		return false, A2SurfaceEventConsole, liveactivity.FreshnessMissing, nil
	}
	hb := in.Presence.HeartbeatAt
	if !hb.Valid {
		return false, A2SurfaceTerminal, liveactivity.FreshnessStale, nil
	}
	age := in.Now.Sub(hb.Time)
	t := hb.Time
	if age > threshold {
		return false, A2SurfaceTerminal, liveactivity.FreshnessStale, &t
	}
	return true, A2SurfaceTerminal, liveactivity.FreshnessFresh, &t
}

// a2SafePayloadKeys is the closed allowlist of structured payload keys that
// may surface in a pane summary. Everything else in event_payload (stdout,
// transcripts, env, credentials, …) is dropped unconditionally.
var a2SafePayloadKeys = []string{"stage", "phase", "step"}

const a2MaxSummaryRunes = 160

func a2Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// a2EventSummary renders a sanitized human summary from the structured event
// kind, the canonical blocker_reason column, and the allowlisted payload keys
// above — never from raw payload text.
func a2EventSummary(anchor db.WorkEvent) string {
	var b strings.Builder
	switch anchor.EventType {
	case "started":
		b.WriteString("开始执行")
	case "progress":
		b.WriteString("执行中")
	case "tool_file_scope":
		b.WriteString("文件范围变更")
	case "checkpoint":
		b.WriteString("检查点")
	case "blocked":
		b.WriteString("受阻等待")
	case "resumed":
		b.WriteString("恢复执行")
	case "candidate_ready":
		b.WriteString("候选就绪")
	case "review_requested":
		b.WriteString("请求评审")
	case "repair_requested":
		b.WriteString("请求修复")
	case "handoff":
		b.WriteString("交接")
	case a2TerminalEvent:
		b.WriteString("结束")
	case "abandoned_recovered":
		b.WriteString("弃用后恢复")
	default:
		b.WriteString("事件更新")
	}
	if anchor.BlockerReason.Valid && strings.TrimSpace(anchor.BlockerReason.String) != "" {
		b.WriteString("：")
		b.WriteString(a2Truncate(strings.TrimSpace(anchor.BlockerReason.String), 80))
		return a2Truncate(b.String(), a2MaxSummaryRunes)
	}
	if stage := a2SafePayloadStage(anchor.EventPayload); stage != "" {
		b.WriteString(" · ")
		b.WriteString(stage)
	}
	return a2Truncate(b.String(), a2MaxSummaryRunes)
}

// a2SafePayloadStage extracts the first allowlisted structured display field
// from the payload JSON. Any decode failure yields "" (fail-closed).
func a2SafePayloadStage(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return ""
	}
	for _, k := range a2SafePayloadKeys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return a2Truncate(strings.TrimSpace(v), 60)
		}
	}
	return ""
}

// ProjectA2Pane derives one Work Wall pane from the canonical inputs. It is
// a pure function: the same input always yields the same pane (idempotent
// replay), and only the A2 rules below decide state and working.
//
// State precedence:
//  1. canonical terminal evidence from receipt / task / issue-cancelled —
//     the authorities describe the WORK, so they outrank delivery noise
//     (terminal text anywhere is never evidence);
//  2. replay — terminal history replayed late or duplicated (never working);
//  3. issue_state_mismatch — a finished claim the Issue authority does not
//     confirm (never working, never completion);
//  4. active — in-flight work; Working additionally requires an actively
//     executing event kind AND a fresh session-matched heartbeat, so a
//     missing heartbeat fails closed.
//
// The returned bool is false when the stream carries no event: panes are
// always anchored on a canonical event row.
func ProjectA2Pane(in A2PaneInput) (A2PaneV1, bool) {
	if len(in.Events) == 0 {
		return A2PaneV1{}, false
	}
	threshold := in.StaleThreshold
	if threshold <= 0 {
		threshold = defaultStaleThreshold
	}
	anchor, terminal := a2AnchorEvent(in.Events)

	pane := A2PaneV1{
		SchemaVersion: A2PaneSchemaV1,
		WorkspaceID:   uuidStr(anchor.WorkspaceID),
		WorkRef:       in.WorkRef,
		SourceEventID: uuidStr(anchor.ID),
		SessionID:     textStr(anchor.SessionID),
		RunID:         textStr(anchor.RunID),
		ActivityKind:  "workevent." + anchor.EventType,
		ObservedAt:    in.Now,
		SourceRefs:    []string{"work_event://" + uuidStr(anchor.ID)},
	}
	if anchor.OccurredAt.Valid {
		t := anchor.OccurredAt.Time
		pane.LastEventAt = &t
	} else if anchor.ObservedAt.Valid {
		t := anchor.ObservedAt.Time
		pane.LastEventAt = &t
	}

	// Dispatch-to-employee ownership: the assignment receipt binds the work
	// to the accountable digital employee; the task assignee is the fallback.
	employeeID := ""
	if in.Dispatch != nil && in.Dispatch.LocalAgentID.Valid {
		employeeID = uuidStr(in.Dispatch.LocalAgentID)
		pane.DispatchCommandID = uuidStr(in.Dispatch.CommandID)
		pane.SourceRefs = append(pane.SourceRefs, "dispatch://"+pane.DispatchCommandID)
	}
	if employeeID == "" && in.Task != nil && in.Task.AgentID.Valid {
		employeeID = uuidStr(in.Task.AgentID)
	}
	pane.EmployeeID = employeeID
	if in.Agent != nil {
		pane.EmployeeName = in.Agent.Name
	}
	if in.Issue != nil {
		pane.IssueID = uuidStr(in.Issue.ID)
		pane.IssueState = in.Issue.Status
		pane.ProjectID = uuidStr(in.Issue.ProjectID)
		pane.SourceRefs = append(pane.SourceRefs, "issue://"+pane.IssueID)
	}
	if in.Task != nil {
		pane.TaskID = uuidStr(in.Task.ID)
		pane.SourceRefs = append(pane.SourceRefs, "task://"+pane.TaskID)
	}

	heartbeatFresh, surface, freshness, heartbeatAt := a2Heartbeat(in, anchor, threshold)
	pane.SurfaceKind = surface
	pane.Freshness = freshness
	pane.LastHeartbeatAt = heartbeatAt
	if heartbeatAt != nil {
		pane.SourceRefs = append(pane.SourceRefs, "presence://"+uuidStr(in.Presence.ID))
	}

	// Execution state (see function doc for precedence).
	state := A2ExecutionActive
	if canonical, ok := a2CanonicalTerminal(in); ok {
		state = canonical
	} else if a2IsReplay(anchor, terminal, a2HasDuplicateEventID(in.Events)) {
		state = A2ExecutionReplay
	} else if anchor.EventType == a2TerminalEvent {
		// A terminal claim is only honored when the Issue authority confirms
		// it; otherwise it is surfaced as an unconfirmed mismatch.
		if in.Issue != nil && a2IssueTerminal(in.Issue.Status) {
			state = A2ExecutionCompleted
		} else {
			state = A2ExecutionIssueMismatch
		}
	}
	pane.ExecutionState = state

	// Working is the single fail-closed conclusion: only genuinely active,
	// actively-executing work with a fresh session-matched heartbeat counts.
	pane.Working = state == A2ExecutionActive &&
		a2WorkingKind(anchor.EventType) &&
		heartbeatFresh

	// Completion time comes from canonical columns only.
	if state == A2ExecutionCompleted {
		switch {
		case in.Receipt != nil && in.Receipt.CompletedAt.Valid:
			t := in.Receipt.CompletedAt.Time
			pane.CompletedAt = &t
		case in.Task != nil && in.Task.CompletedAt.Valid:
			t := in.Task.CompletedAt.Time
			pane.CompletedAt = &t
		}
	}

	pane.ActivitySummary = a2EventSummary(*anchor)
	return pane, true
}

// SortA2Panes orders panes deterministically: newest anchor event first,
// then work_ref ascending, so equal inputs always produce equal output.
func SortA2Panes(panes []A2PaneV1) {
	sort.SliceStable(panes, func(i, j int) bool {
		ti, tj := a2PaneSortTime(panes[i]), a2PaneSortTime(panes[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return panes[i].WorkRef < panes[j].WorkRef
	})
}

func a2PaneSortTime(p A2PaneV1) time.Time {
	if p.LastEventAt != nil {
		return *p.LastEventAt
	}
	return time.Time{}
}
