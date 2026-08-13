package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// project_autostart_unit_test.go — HIV-465 / HIV-473 repair pure unit tests.
//
// These tests cover the fail-closed readiness classification, the duplicate-
// selection gate, the prerequisite gate and the idempotency helpers with NO
// database dependency, so they run in every environment (including this lane,
// which is forbidden from connecting to any Postgres). The DB-backed
// integration scenarios in project_autostart_test.go / handler tests are
// marked DB_UNVERIFIED and are independently re-verified by the isolated DB
// gate.

// TestStatusBlockReason verifies the status gate excludes done/cancelled AND
// blocked (the wave SQL only excludes done/cancelled, so the Go layer must
// additionally catch 'blocked').
func TestStatusBlockReason(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   AutoStartBlockReason
	}{
		{"done is terminal", "done", AutoStartBlockTerminalStatus},
		{"cancelled is terminal", "cancelled", AutoStartBlockTerminalStatus},
		{"blocked is blocked", "blocked", AutoStartBlockBlockedStatus},
		{"todo is ok", "todo", AutoStartBlockNone},
		{"in_progress is ok", "in_progress", AutoStartBlockNone},
		{"backlog is ok", "backlog", AutoStartBlockNone},
		{"in_review is ok", "in_review", AutoStartBlockNone},
		{"empty is ok (assignee gate handles it)", "", AutoStartBlockNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusBlockReason(tc.status); got != tc.want {
				t.Fatalf("statusBlockReason(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestCapacityFull verifies the capacity gate boundary and the canonical
// claim-semantics for maxConcurrent <= 0: the claim path blocks when
// running >= max_concurrent_tasks, so 0 (and negative) slots is ALWAYS
// capacity full — never unbounded (HIV-473 item 3). These are the corrected
// assertions; the old draft wrongly treated 0 as unbounded.
func TestCapacityFull(t *testing.T) {
	tests := []struct {
		name          string
		running       int
		maxConcurrent int
		want          bool
	}{
		{"under cap", 0, 1, false},
		{"under cap 2", 2, 6, false},
		{"at cap", 6, 6, true},
		{"over cap", 7, 6, true},
		{"at cap one", 1, 1, true},
		{"zero cap is always full", 0, 0, true},
		{"zero cap full even idle", 99, 0, true},
		{"negative cap is always full", 99, -1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := capacityFull(tc.running, tc.maxConcurrent); got != tc.want {
				t.Fatalf("capacityFull(running=%d, max=%d) = %v, want %v",
					tc.running, tc.maxConcurrent, got, tc.want)
			}
		})
	}
}

// TestPrereqBlockReason verifies the prerequisite gate: a row whose parent
// gate is NOT satisfied must surface as blocked (missing_prerequisite), never
// vanish from the SQL result set (HIV-465 item 4). Invalid flags fail closed.
func TestPrereqBlockReason(t *testing.T) {
	if got := prereqBlockReason(pgtype.Bool{Valid: true, Bool: false}); got != AutoStartBlockMissingPrereq {
		t.Fatalf("prereqBlockReason(false) = %q, want %q", got, AutoStartBlockMissingPrereq)
	}
	if got := prereqBlockReason(pgtype.Bool{Valid: true, Bool: true}); got != AutoStartBlockNone {
		t.Fatalf("prereqBlockReason(true) = %q, want %q", got, AutoStartBlockNone)
	}
	if got := prereqBlockReason(pgtype.Bool{}); got != AutoStartBlockMissingPrereq {
		t.Fatalf("prereqBlockReason(invalid) must fail closed, got %q", got)
	}
}

// TestClassifyResolvedReadiness_FailClosed covers every negative gate plus the
// positive case and — critically — the fail-closed rule that a runtime lookup
// error blocks rather than passes. Runtime states are separated: unbound
// (no runtime bound) and missing (runtime row gone) are distinct from offline
// (runtime exists but not online) (HIV-473 item 5).
func TestClassifyResolvedReadiness_FailClosed(t *testing.T) {
	tests := []struct {
		name          string
		agentArchived bool
		rtState       autostartRuntimeState
		running       int
		maxConcurrent int
		wantReady     bool
		wantReason    AutoStartBlockReason
	}{
		{
			name:          "archived blocks even if runtime looks ready",
			agentArchived: true, rtState: runtimeStateOK,
			wantReady: false, wantReason: AutoStartBlockAgentArchived,
		},
		{
			name:          "runtime unbound blocks (no runtime bound, not offline)",
			agentArchived: false, rtState: runtimeStateUnbound,
			wantReady: false, wantReason: AutoStartBlockRuntimeUnbound,
		},
		{
			name:          "runtime missing blocks (row gone, not offline)",
			agentArchived: false, rtState: runtimeStateMissing,
			wantReady: false, wantReason: AutoStartBlockRuntimeMissing,
		},
		{
			name:          "runtime offline blocks",
			agentArchived: false, rtState: runtimeStateOffline,
			wantReady: false, wantReason: AutoStartBlockRuntimeOffline,
		},
		{
			name:          "runtime lookup error is fail-closed (not ready)",
			agentArchived: false, rtState: runtimeStateLookupErr,
			wantReady: false, wantReason: AutoStartBlockRuntimeLookupErr,
		},
		{
			name:          "capacity full blocks",
			agentArchived: false, rtState: runtimeStateOK,
			running: 3, maxConcurrent: 3,
			wantReady: false, wantReason: AutoStartBlockCapacityFull,
		},
		{
			name:          "zero capacity is capacity full, not ready",
			agentArchived: false, rtState: runtimeStateOK,
			running: 0, maxConcurrent: 0,
			wantReady: false, wantReason: AutoStartBlockCapacityFull,
		},
		{
			name:          "all green is ready",
			agentArchived: false, rtState: runtimeStateOK,
			running: 1, maxConcurrent: 3,
			wantReady: true, wantReason: AutoStartBlockNone,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ready, reason := classifyResolvedReadiness(
				tc.agentArchived, tc.rtState,
				tc.running, tc.maxConcurrent,
			)
			if ready != tc.wantReady {
				t.Fatalf("ready = %v, want %v (reason=%q)", ready, tc.wantReady, reason)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestClassifyResolvedReadiness_Ordering verifies the gate ordering: archived
// is reported before runtime issues, and a runtime lookup error is reported
// before capacity. This guards against a re-ordering that would mask the real
// blocker from the owner.
func TestClassifyResolvedReadiness_Ordering(t *testing.T) {
	// Archived + runtime offline → archived wins.
	if _, r := classifyResolvedReadiness(true, runtimeStateOffline, 0, 1); r != AutoStartBlockAgentArchived {
		t.Fatalf("archived should dominate runtime offline, got %q", r)
	}
	// Lookup-failed + capacity full → lookup-failed wins (we can't trust the
	// count anyway).
	if _, r := classifyResolvedReadiness(false, runtimeStateLookupErr, 99, 1); r != AutoStartBlockRuntimeLookupErr {
		t.Fatalf("runtime lookup error should dominate capacity, got %q", r)
	}
	// Unbound + capacity full → unbound wins.
	if _, r := classifyResolvedReadiness(false, runtimeStateUnbound, 99, 1); r != AutoStartBlockRuntimeUnbound {
		t.Fatalf("runtime unbound should dominate capacity, got %q", r)
	}
}

// TestAutostartDuplicateSelection verifies duplicate selection IDs are
// detected explicitly so the batch can fail closed instead of silently
// collapsing them into a Set (HIV-465 item 1).
func TestAutostartDuplicateSelection(t *testing.T) {
	u := func(s string) pgtype.UUID { return mustUUID(t, s) }

	none := autostartDuplicateSelection([]pgtype.UUID{u("10000000-0000-0000-0000-000000000001"), u("10000000-0000-0000-0000-000000000002")})
	if len(none) != 0 {
		t.Fatalf("no duplicates expected, got %v", none)
	}

	one := autostartDuplicateSelection([]pgtype.UUID{
		u("10000000-0000-0000-0000-000000000001"),
		u("10000000-0000-0000-0000-000000000001"),
	})
	if !one["10000000-0000-0000-0000-000000000001"] {
		t.Fatalf("duplicate id must be detected, got %v", one)
	}
	if len(one) != 1 {
		t.Fatalf("exactly one duplicate expected, got %v", one)
	}

	empty := autostartDuplicateSelection(nil)
	if len(empty) != 0 {
		t.Fatalf("nil selection must yield no duplicates, got %v", empty)
	}
}

// TestAutostartPerIssueKey_StableAndDeterministic verifies the per-issue
// idempotency key is derived from (batch key, issue id) only — position
// independent — so a re-ordered wave replays the same receipt (HIV-465 item 3:
// 重复 replay receipt 一致).
func TestAutostartPerIssueKey_StableAndDeterministic(t *testing.T) {
	const batch = "batch-1"
	const issue = "11111111-1111-1111-1111-111111111111"

	k1 := autostartPerIssueKey(batch, issue)
	k2 := autostartPerIssueKey(batch, issue)
	if k1 != k2 {
		t.Fatalf("same inputs must yield same key: %q vs %q", k1, k2)
	}
	// Different batch → different key.
	if autostartPerIssueKey("batch-2", issue) == k1 {
		t.Fatal("different batch key must produce a different per-issue key")
	}
	// Different issue → different key.
	if autostartPerIssueKey(batch, "22222222-2222-2222-2222-222222222222") == k1 {
		t.Fatal("different issue id must produce a different per-issue key")
	}
	// The key must encode the issue id (not a row index), so it is stable
	// regardless of where the issue sits in the wave.
	if autostartPerIssueKey(batch, issue) != batch+":"+issue {
		t.Fatalf("per-issue key shape changed; expected %q", batch+":"+issue)
	}
}

// TestAutostartPerIssueKey_NoIndexComponent guards against regressing to the
// old position-dependent scheme (which made replay depend on wave ordering).
func TestAutostartPerIssueKey_NoIndexComponent(t *testing.T) {
	// If anyone reintroduces an index suffix, this assertion catches it: the
	// key for a given (batch, issue) must be a single canonical value.
	want := "b:i"
	if got := autostartPerIssueKey("b", "i"); got != want {
		t.Fatalf("per-issue key must be batch:issue with no index, got %q", got)
	}
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	u, err := util.ParseUUID(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}
