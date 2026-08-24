package workwall

import (
	"encoding/json"
	"regexp"
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

	// RequestWorkspaceID is the workspace the snapshot was requested for.
	// RefWorkspaceID / RefProjectID are the ids embedded in the work_ref.
	// When RequestWorkspaceID is set, a work_ref embedding a different (or
	// missing) workspace is foreign data and fails closed: no pane.
	RequestWorkspaceID string
	RefWorkspaceID     string
	RefProjectID       string
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

// a2ForeignWorkRef reports whether the work_ref embeds a workspace that does
// not match the requesting workspace. A missing embedded workspace is also
// foreign when a request workspace is known: an unattributable work_ref must
// never project.
func a2ForeignWorkRef(in A2PaneInput) bool {
	if in.RequestWorkspaceID == "" {
		return false
	}
	return in.RefWorkspaceID == "" || !strings.EqualFold(in.RefWorkspaceID, in.RequestWorkspaceID)
}

// a2ProjectDrift reports whether the work_ref's embedded project id disagrees
// with the Issue authority's project. Empty/non-UUID claims are not compared
// (nothing provable to check); a provable disagreement is drift.
func a2ProjectDrift(in A2PaneInput) bool {
	if in.Issue == nil || !in.Issue.ProjectID.Valid || in.RefProjectID == "" {
		return false
	}
	return !strings.EqualFold(in.RefProjectID, uuidStr(in.Issue.ProjectID))
}

// a2CanonicalReceipt returns the receipt only when it is provably about this
// tenant, task, and issue. Foreign rows are dropped, never trusted.
func a2CanonicalReceipt(in A2PaneInput) *db.ExecutionReceipt {
	r := in.Receipt
	if r == nil {
		return nil
	}
	if in.RequestWorkspaceID != "" && !strings.EqualFold(uuidStr(r.WorkspaceID), in.RequestWorkspaceID) {
		return nil
	}
	if r.TaskID.Valid && in.Task != nil && in.Task.ID.Valid &&
		uuidStr(r.TaskID) != uuidStr(in.Task.ID) {
		return nil
	}
	if r.IssueID.Valid && in.Issue != nil && in.Issue.ID.Valid &&
		uuidStr(r.IssueID) != uuidStr(in.Issue.ID) {
		return nil
	}
	return r
}

// a2BoundDispatch returns the dispatch receipt only when it is precisely
// bound to THIS task (and issue, when known): a later re-dispatch of the
// issue for a different task must never hijack this pane's ownership.
func a2BoundDispatch(in A2PaneInput) *db.AssignmentDispatchReceipt {
	d := in.Dispatch
	if d == nil {
		return nil
	}
	task := a2TaskForRef(in)
	if task == nil || !task.ID.Valid {
		return nil
	}
	if !d.InitialTaskID.Valid || uuidStr(d.InitialTaskID) != uuidStr(task.ID) {
		return nil
	}
	if in.Issue != nil && in.Issue.ID.Valid && d.IssueID.Valid &&
		uuidStr(d.IssueID) != uuidStr(in.Issue.ID) {
		return nil
	}
	if in.RequestWorkspaceID != "" && !strings.EqualFold(uuidStr(d.WorkspaceID), in.RequestWorkspaceID) {
		return nil
	}
	// B3-4: the dispatch must be the exact command this task's canonical
	// receipt names. A dispatch without that receipt — or naming a different
	// command — can neither own the pane's employee nor establish evidence.
	r := a2CanonicalReceipt(in)
	if r == nil || !r.AssignmentCommandID.Valid {
		return nil
	}
	if !strings.EqualFold(uuidStr(d.CommandID), uuidStr(r.AssignmentCommandID)) {
		return nil
	}
	return d
}

// a2TaskForRef returns the attached task only when it provably belongs to
// this work_ref: the Issue authority must be present and the task row must
// reference exactly that issue. A cross-issue (or unverifiable) task is
// dropped and can never affect the pane — not its id, its assignee, its
// terminal state, or its evidence.
func a2TaskForRef(in A2PaneInput) *db.AgentTaskQueue {
	t := in.Task
	if t == nil || in.Issue == nil || !in.Issue.ID.Valid || !t.IssueID.Valid {
		return nil
	}
	if uuidStr(t.IssueID) != uuidStr(in.Issue.ID) {
		return nil
	}
	return t
}

// a2TaskNonTerminal reports whether the tenant-bound task is currently in
// flight. A missing task is never in flight.
func a2TaskNonTerminal(t *db.AgentTaskQueue) bool {
	if t == nil {
		return false
	}
	switch t.Status {
	case "queued", "dispatched", "running", "waiting_local_directory":
		return true
	default:
		return false
	}
}

// a2HasExactEvidence reports whether this pane carries exact execution
// evidence: a tenant/task/issue-verified execution receipt for this
// work_ref's task. Since B3-4 a dispatch alone is never evidence — it only
// carries ownership on top of the receipt it must match. Ledger events and
// heartbeats alone are never evidence.
func a2HasExactEvidence(in A2PaneInput) bool {
	return a2CanonicalReceipt(in) != nil
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

// a2CredentialMarkers are substrings that never belong in a projected
// display value (stage / phase / step / blocker_reason). Matching is
// case-insensitive and fail-closed: a value that merely looks like it could
// carry a credential is dropped entirely, never surfaced.
var a2CredentialMarkers = []string{
	"sk-", "gsk_", "rk-", "ghp_", "gho_", "ghu_", "github_pat_",
	"xoxb-", "xoxp-", "xoxa-", "akia", "asymakey",
	"api_key", "apikey", "secret", "token", "password", "passwd",
	"bearer ", "authorization:", "credential", "private_key",
	"-----begin", "ssh-rsa", "ecdsa-", "connect.sid",
}

// a2TokenShapedRun matches an unbroken 24+ character run of token-alphabet
// characters (hex/base64/url-safe), which no honest stage or blocker reason
// contains but every synthetic key material string does.
var a2TokenShapedRun = regexp.MustCompile(`[A-Za-z0-9_-]{24,}`)

// a2DisplayValue sanitizes one allowlisted display value: it collapses
// control characters and whitespace, drops credential-marked or token-shaped
// values entirely (empty string), and truncates to max runes. It is the only
// path by which payload or blocker text may reach a pane.
func a2DisplayValue(s string, max int) string {
	if max <= 0 {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	for _, marker := range a2CredentialMarkers {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	if a2TokenShapedRun.MatchString(s) {
		return ""
	}
	return a2Truncate(s, max)
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
	if reason := a2DisplayValue(textStr(anchor.BlockerReason), 80); reason != "" {
		b.WriteString("：")
		b.WriteString(reason)
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
		if v, ok := m[k].(string); ok {
			if clean := a2DisplayValue(v, 60); clean != "" {
				return clean
			}
		}
	}
	return ""
}

// a2VerifiedTerminalInput returns a copy of the input whose receipt is the
// tenant/task/issue-verified one (or none). Unverified receipts must never
// feed the terminal-state decision.
func a2VerifiedTerminalInput(in A2PaneInput) A2PaneInput {
	in.Task = a2TaskForRef(in)
	in.Receipt = a2CanonicalReceipt(in)
	return in
}

// ProjectA2Pane derives one Work Wall pane from the canonical inputs. It is
// a pure function: the same input always yields the same pane (idempotent
// replay), and only the A2 rules below decide state and working.
//
// State precedence:
//  1. project drift — the work_ref and the Issue authority disagree about
//     the project; renders only as issue_state_mismatch (never working,
//     never completion) so drift is visible but never trusted;
//  2. canonical terminal evidence from receipt / task / issue-cancelled —
//     the authorities describe the WORK, so they outrank delivery noise;
//     a receipt is only canonical after tenant/task/issue verification,
//     and only an issue-verified task may speak for the pane
//     (terminal text anywhere is never evidence);
//  3. replay — terminal history replayed late or duplicated (never working);
//  4. unconfirmed claim — an in-flight claim without a current nonterminal
//     tenant-bound task or without exact execution evidence renders as
//     issue_state_mismatch: the ledger says work, the authorities do not;
//     never active, never working;
//  5. a finished claim the Issue authority does not confirm is likewise
//     issue_state_mismatch (never working, never completion);
//  6. active — in-flight work; Working additionally requires an actively
//     executing event kind, a current nonterminal tenant-bound task, exact
//     dispatch/receipt evidence, and a fresh session-matched heartbeat, so
//     an event plus a heartbeat alone never counts as working.
//
// The returned bool is false when the stream carries no event: panes are
// always anchored on a canonical event row.
func ProjectA2Pane(in A2PaneInput) (A2PaneV1, bool) {
	if len(in.Events) == 0 {
		return A2PaneV1{}, false
	}
	// B2 fail-closed: a work_ref that does not embed the requesting
	// workspace is foreign data; it must never project into this snapshot.
	if a2ForeignWorkRef(in) {
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

	// B3-3: the task only speaks for this pane when it provably belongs to
	// this work_ref's issue; a cross-issue task influences nothing below.
	task := a2TaskForRef(in)

	// Dispatch-to-employee ownership: ONLY a dispatch receipt precisely
	// bound to this task (initial_task_id) AND matching the canonical
	// receipt's command may carry ownership — the latest issue dispatch must
	// never win, because a later re-dispatch would silently re-attribute
	// finished work to a different employee. Without a task-bound dispatch
	// the pane falls back to the task assignee (tenant-verified via
	// GetAgentTaskInWorkspace) and exposes no dispatch command.
	employeeID := ""
	if bound := a2BoundDispatch(in); bound != nil && bound.LocalAgentID.Valid {
		employeeID = uuidStr(bound.LocalAgentID)
		pane.DispatchCommandID = uuidStr(bound.CommandID)
		pane.SourceRefs = append(pane.SourceRefs, "dispatch://"+pane.DispatchCommandID)
	}
	if employeeID == "" && task != nil && task.AgentID.Valid {
		employeeID = uuidStr(task.AgentID)
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
	if task != nil {
		pane.TaskID = uuidStr(task.ID)
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
	if a2ProjectDrift(in) {
		state = A2ExecutionIssueMismatch
	} else if canonical, ok := a2CanonicalTerminal(a2VerifiedTerminalInput(in)); ok {
		state = canonical
	} else if a2IsReplay(anchor, terminal, a2HasDuplicateEventID(in.Events)) {
		state = A2ExecutionReplay
	} else if !a2TaskNonTerminal(task) || !a2HasExactEvidence(in) {
		// B3-1: an in-flight ledger claim with no current nonterminal
		// tenant-bound task, or with no exact execution evidence, is
		// unconfirmed by the authorities. It is never active — an event
		// plus a fresh heartbeat alone must not look like live work.
		state = A2ExecutionIssueMismatch
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

	// Working is the single fail-closed conclusion. It requires ALL of:
	// an active state, an actively-executing event kind, a current
	// nonterminal tenant-bound task, exact dispatch/receipt evidence for
	// that task, and a fresh session-matched heartbeat. A ledger event plus
	// a heartbeat alone must NEVER count as working.
	pane.Working = state == A2ExecutionActive &&
		a2WorkingKind(anchor.EventType) &&
		a2TaskNonTerminal(task) &&
		a2HasExactEvidence(in) &&
		heartbeatFresh

	// Completion time comes from canonical columns only.
	if state == A2ExecutionCompleted {
		if r := a2CanonicalReceipt(in); r != nil && r.CompletedAt.Valid {
			t := r.CompletedAt.Time
			pane.CompletedAt = &t
		} else if task != nil && task.CompletedAt.Valid {
			t := task.CompletedAt.Time
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
