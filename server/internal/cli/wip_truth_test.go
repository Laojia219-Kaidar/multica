package cli

import (
	"sort"
	"testing"
	"time"
)

// Fixed reference instants for freshness tests.
var (
	testNow      = time.Date(2026, 8, 13, 4, 0, 0, 0, time.UTC)
	testObserved = testNow.Add(-3 * time.Minute) // 3 min ago, within 5 min threshold
)

// ---------------------------------------------------------------------------
// sortedDedupNonEmpty
// ---------------------------------------------------------------------------

func TestSortedDedupNonEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, []string{}},
		{"empty", []string{}, []string{}},
		{"sorted_unique", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"unsorted", []string{"c", "a", "b"}, []string{"a", "b", "c"}},
		{"duplicates", []string{"b", "a", "b", "c", "a"}, []string{"a", "b", "c"}},
		{"single", []string{"x"}, []string{"x"}},
		{"all_same", []string{"z", "z", "z"}, []string{"z"}},
		{"empty_strings_filtered", []string{"", "a", "", "b", ""}, []string{"a", "b"}},
		{"only_empty", []string{"", "", ""}, []string{}},
		{"mixed_with_empties_and_dups", []string{"", "c", "a", "", "c", "b", "a"}, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortedDedupNonEmpty(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d; got %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ComputeWIPTruthCore — table-driven
// ---------------------------------------------------------------------------

func TestComputeWIPTruthCore(t *testing.T) {
	defaultCfg := CoreConfig{Now: testNow, FreshnessThreshold: 5 * time.Minute}

	tests := []struct {
		name string

		runtimeIDs    []string
		tasks         []TaskRow
		workers       []WorkerInput
		daemonRunning bool
		daemonActive  int
		cfg           CoreConfig

		// Expected assertions (nil = skip check).
		wantRuntimeIDs      []string
		wantQueued          *int
		wantBacklog         *int
		wantClaimed         *int
		wantDispatched      *int
		wantRunning         *int
		wantWaiting         *int
		wantScopedRows      *int
		wantUnknownRows     *int
		wantReconciled      *bool
		wantFresh           *int
		wantStale           *int
		wantKnownNonWorking *int
		wantUnknownWorkers  *int
		wantProjected       *int
		wantProjAvailable   *bool
		wantDivergence      *int
		wantProjDiv         *int
		wantReasonsContain  []string
		wantReasonsAbsent   []string
	}{
		// --- Scope ---
		{
			name:               "empty_runtime_scope_fail_closed",
			runtimeIDs:         nil,
			daemonRunning:      true,
			daemonActive:       3,
			cfg:                defaultCfg,
			wantRuntimeIDs:     []string{},
			wantReconciled:     boolPtr(true),
			wantReasonsContain: []string{ReasonNoRuntimeIDsScoped},
		},
		{
			name:               "empty_slice_runtime_scope_fail_closed",
			runtimeIDs:         []string{},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantReasonsContain: []string{ReasonNoRuntimeIDsScoped},
		},
		{
			name:           "empty_strings_filtered_from_scope",
			runtimeIDs:     []string{"", "rt-1", "", ""},
			daemonRunning:  true,
			daemonActive:   0,
			cfg:            defaultCfg,
			wantRuntimeIDs: []string{"rt-1"},
		},
		{
			name:           "runtime_ids_sorted_dedup",
			runtimeIDs:     []string{"rt-c", "rt-a", "rt-b", "rt-a", "rt-c"},
			daemonRunning:  true,
			daemonActive:   0,
			cfg:            defaultCfg,
			wantRuntimeIDs: []string{"rt-a", "rt-b", "rt-c"},
		},
		{
			name:               "only_empty_strings_in_scope_fail_closed",
			runtimeIDs:         []string{"", "", ""},
			daemonRunning:      true,
			daemonActive:       2,
			cfg:                defaultCfg,
			wantReasonsContain: []string{ReasonNoRuntimeIDsScoped},
		},

		// --- Histogram ---
		{
			name:       "queued_separate_from_claimed",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusQueued, RuntimeID: "rt-1"},
				{ID: "t2", Status: StatusQueued, RuntimeID: "rt-1"},
				{ID: "t3", Status: StatusQueued, RuntimeID: "rt-1"},
			},
			daemonRunning:   true,
			daemonActive:    0,
			cfg:             defaultCfg,
			wantQueued:      intPtr(3),
			wantClaimed:     intPtr(0),
			wantScopedRows:  intPtr(3),
			wantUnknownRows: intPtr(0),
			wantReconciled:  boolPtr(true),
		},
		{
			name:       "claimed_includes_dispatched_running_waiting",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusDispatched, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t2", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a2"},
				{ID: "t3", Status: StatusWaitingLocalDirectory, RuntimeID: "rt-1", AgentID: "a3"},
				{ID: "t4", Status: StatusQueued, RuntimeID: "rt-1"},
				{ID: "t5", Status: StatusBacklog, RuntimeID: "rt-1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
				{AgentID: "a2", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
				{AgentID: "a3", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
			},
			daemonRunning:     true,
			daemonActive:      3,
			cfg:               defaultCfg,
			wantQueued:        intPtr(1),
			wantBacklog:       intPtr(1),
			wantClaimed:       intPtr(3),
			wantDispatched:    intPtr(1),
			wantRunning:       intPtr(1),
			wantWaiting:       intPtr(1),
			wantScopedRows:    intPtr(5),
			wantReconciled:    boolPtr(true),
			wantFresh:         intPtr(3),
			wantProjected:     intPtr(3),
			wantProjAvailable: boolPtr(true),
			wantDivergence:    intPtr(0),
			wantProjDiv:       intPtr(0),
		},

		// --- Foreign runtime ---
		{
			name:       "foreign_runtime_ignored",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t2", Status: StatusRunning, RuntimeID: "rt-OTHER", AgentID: "a2"},
				{ID: "t3", Status: StatusRunning, RuntimeID: "rt-FOREIGN", AgentID: "a3"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
			},
			daemonRunning:  true,
			daemonActive:   1,
			cfg:            defaultCfg,
			wantRunning:    intPtr(1),
			wantClaimed:    intPtr(1),
			wantScopedRows: intPtr(1),
			wantReconciled: boolPtr(true),
			wantDivergence: intPtr(0),
		},

		// --- Missing data ---
		{
			name:       "missing_runtime_id_unknown",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: ""},
				{ID: "t2", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantRunning:        intPtr(1),
			wantScopedRows:     intPtr(2),
			wantUnknownRows:    intPtr(1),
			wantReconciled:     boolPtr(true),
			wantReasonsContain: []string{ReasonMissingRuntimeID},
		},
		{
			name:       "missing_agent_id_on_active_task_unknown",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t2", Status: StatusRunning, RuntimeID: "rt-1"}, // no agent
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantRunning:        intPtr(1),
			wantClaimed:        intPtr(1),
			wantScopedRows:     intPtr(2),
			wantUnknownRows:    intPtr(1),
			wantReconciled:     boolPtr(true),
			wantReasonsContain: []string{ReasonMissingAgentID},
		},
		{
			name:       "active_task_missing_agent_not_counted_in_subcounter",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusDispatched, RuntimeID: "rt-1"},            // no agent
				{ID: "t2", Status: StatusRunning, RuntimeID: "rt-1"},               // no agent
				{ID: "t3", Status: StatusWaitingLocalDirectory, RuntimeID: "rt-1"}, // no agent
			},
			daemonRunning:      true,
			daemonActive:       0,
			cfg:                defaultCfg,
			wantDispatched:     intPtr(0),
			wantRunning:        intPtr(0),
			wantWaiting:        intPtr(0),
			wantClaimed:        intPtr(0),
			wantScopedRows:     intPtr(3),
			wantUnknownRows:    intPtr(3),
			wantReconciled:     boolPtr(true),
			wantReasonsContain: []string{ReasonMissingAgentID},
		},

		// --- Unknown status ---
		{
			name:       "unknown_task_status_stable_code",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: "bogus_status", RuntimeID: "rt-1"},
				{ID: "t2", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantRunning:        intPtr(1),
			wantScopedRows:     intPtr(2),
			wantUnknownRows:    intPtr(1),
			wantReconciled:     boolPtr(true),
			wantReasonsContain: []string{ReasonUnknownTaskStatus},
			// The raw status value must NOT appear in any reason string.
			wantReasonsAbsent: []string{"bogus_status"},
		},

		// --- Duplicate detection ---
		{
			name:       "duplicate_task_id_unknown",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a2"}, // dup ID
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantClaimed:        intPtr(1),
			wantScopedRows:     intPtr(2),
			wantUnknownRows:    intPtr(1),
			wantReconciled:     boolPtr(true),
			wantReasonsContain: []string{ReasonDuplicateTaskID},
		},
		{
			name:       "duplicate_agent_id_on_active_task_unknown",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t2", Status: StatusDispatched, RuntimeID: "rt-1", AgentID: "a1"}, // dup agent
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantClaimed:        intPtr(1),
			wantScopedRows:     intPtr(2),
			wantUnknownRows:    intPtr(1),
			wantReconciled:     boolPtr(true),
			wantReasonsContain: []string{ReasonDuplicateAgentID},
		},
		{
			name:       "duplicate_agent_across_runtimes_unknown",
			runtimeIDs: []string{"rt-1", "rt-2"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t2", Status: StatusRunning, RuntimeID: "rt-2", AgentID: "a1"}, // same agent, different rt
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantClaimed:        intPtr(1),
			wantRunning:        intPtr(1),
			wantScopedRows:     intPtr(2),
			wantUnknownRows:    intPtr(1),
			wantReconciled:     boolPtr(true),
			wantReasonsContain: []string{ReasonDuplicateAgentID},
		},
		{
			name:       "empty_task_id_not_flagged_as_duplicate",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "", Status: StatusQueued, RuntimeID: "rt-1"},
				{ID: "", Status: StatusQueued, RuntimeID: "rt-1"},
			},
			daemonRunning:     true,
			daemonActive:      0,
			cfg:               defaultCfg,
			wantQueued:        intPtr(2),
			wantScopedRows:    intPtr(2),
			wantUnknownRows:   intPtr(0),
			wantReconciled:    boolPtr(true),
			wantReasonsAbsent: []string{ReasonDuplicateTaskID},
		},

		// --- Reconciliation ---
		{
			name:       "reconciliation_holds_complex_mixture",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusQueued, RuntimeID: "rt-1"},
				{ID: "t2", Status: StatusBacklog, RuntimeID: "rt-1"},
				{ID: "t3", Status: StatusDispatched, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t4", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a2"},
				{ID: "t5", Status: StatusWaitingLocalDirectory, RuntimeID: "rt-1", AgentID: "a3"},
				{ID: "t6", Status: "mystery", RuntimeID: "rt-1"},
				{ID: "t7", Status: StatusRunning, RuntimeID: "rt-FOREIGN", AgentID: "a4"}, // foreign
				{ID: "t8", Status: StatusRunning, RuntimeID: ""},                          // missing runtime
			},
			daemonRunning:  true,
			daemonActive:   3,
			cfg:            defaultCfg,
			wantQueued:     intPtr(1),
			wantBacklog:    intPtr(1),
			wantClaimed:    intPtr(3),
			wantDispatched: intPtr(1),
			wantRunning:    intPtr(1),
			wantWaiting:    intPtr(1),
			// Scoped: t1-t6 (6 scoped) + t8 (1, missing runtime counts as scoped-unknown) = 7.
			// t7 is foreign, ignored.
			wantScopedRows:  intPtr(7),
			wantUnknownRows: intPtr(2), // t6 (unknown status) + t8 (missing runtime)
			wantReconciled:  boolPtr(true),
		},

		// --- Worker freshness ---
		{
			name:       "worker_fresh_matched",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t2", Status: StatusDispatched, RuntimeID: "rt-1", AgentID: "a2"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
				{AgentID: "a2", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
			},
			daemonRunning:     true,
			daemonActive:      2,
			cfg:               defaultCfg,
			wantFresh:         intPtr(2),
			wantStale:         intPtr(0),
			wantProjected:     intPtr(2),
			wantProjAvailable: boolPtr(true),
		},
		{
			name:       "worker_stale_over_threshold",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testNow.Add(-10 * time.Minute)}, // 10 min, stale
			},
			daemonRunning:     true,
			daemonActive:      1,
			cfg:               defaultCfg,
			wantFresh:         intPtr(0),
			wantStale:         intPtr(1),
			wantProjected:     intPtr(0),
			wantProjAvailable: boolPtr(true),
		},
		{
			name:       "worker_unbacked_no_matching_claim",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
				{AgentID: "aC", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved}, // no claim
			},
			daemonRunning: true,
			daemonActive:  1,
			cfg:           defaultCfg,
			wantFresh:     intPtr(1),
			wantStale:     intPtr(1),
		},
		{
			name:       "worker_unbacked_different_runtime",
			runtimeIDs: []string{"rt-1", "rt-2"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-2", Status: AgentStatusWorking, ObservedAt: testObserved}, // wrong runtime
			},
			daemonRunning: true,
			daemonActive:  1,
			cfg:           defaultCfg,
			wantFresh:     intPtr(0),
			wantStale:     intPtr(1),
		},
		{
			name:       "freshness_boundary_exact_threshold_is_fresh",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testNow.Add(-5 * time.Minute)}, // exactly 5 min
			},
			daemonRunning: true,
			daemonActive:  1,
			cfg:           CoreConfig{Now: testNow, FreshnessThreshold: 5 * time.Minute},
			wantFresh:     intPtr(1),
			wantStale:     intPtr(0),
		},
		{
			name:       "freshness_boundary_just_over_is_stale",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testNow.Add(-5*time.Minute - time.Nanosecond)}, // 5m + 1ns
			},
			daemonRunning: true,
			daemonActive:  1,
			cfg:           CoreConfig{Now: testNow, FreshnessThreshold: 5 * time.Minute},
			wantFresh:     intPtr(0),
			wantStale:     intPtr(1),
		},
		{
			name:       "freshness_future_observation_is_stale",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testNow.Add(+1 * time.Minute)}, // future
			},
			daemonRunning: true,
			daemonActive:  1,
			cfg:           defaultCfg,
			wantFresh:     intPtr(0),
			wantStale:     intPtr(1),
		},

		// --- Worker UNKNOWN states ---
		{
			name:       "known_non_working_statuses_handled_explicitly",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusIdle, ObservedAt: testObserved},
				{AgentID: "a2", RuntimeID: "rt-1", Status: AgentStatusArchived, ObservedAt: testObserved},
				{AgentID: "a3", RuntimeID: "rt-1", Status: AgentStatusOffline, ObservedAt: testObserved},
				{AgentID: "a4", RuntimeID: "rt-1", Status: AgentStatusUnstable, ObservedAt: testObserved},
			},
			daemonRunning:       true,
			daemonActive:        1,
			cfg:                 defaultCfg,
			wantKnownNonWorking: intPtr(4),
			wantUnknownWorkers:  intPtr(0),
			wantFresh:           intPtr(0),
			wantStale:           intPtr(0),
			// Known non-working statuses are an explicit counted bucket — never
			// UnknownWorkers and never UNKNOWN_AGENT_STATUS.
			wantReasonsAbsent: []string{ReasonUnknownAgentStatus, "idle", "archived", "offline", "unstable"},
		},
		{
			name:       "truly_unknown_agent_status_unknown",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: "", ObservedAt: testObserved},
				{AgentID: "a2", RuntimeID: "rt-1", Status: "bogus_status", ObservedAt: testObserved},
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantUnknownWorkers: intPtr(2),
			wantFresh:          intPtr(0),
			wantStale:          intPtr(0),
			wantReasonsContain: []string{ReasonUnknownAgentStatus},
			// Raw status values must NOT appear in reasons.
			wantReasonsAbsent: []string{"bogus_status"},
		},
		{
			name:       "worker_empty_agent_id_unknown",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantUnknownWorkers: intPtr(1),
			wantFresh:          intPtr(0),
			wantStale:          intPtr(0),
			wantReasonsContain: []string{ReasonMissingAgentID},
		},
		{
			name:       "worker_duplicate_agent_id_unknown_not_counted_twice",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved}, // dup in projection
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantUnknownWorkers: intPtr(1),
			wantFresh:          intPtr(1), // first occurrence only — never twice
			wantStale:          intPtr(0),
			wantProjected:      intPtr(1),
			wantReasonsContain: []string{ReasonDuplicateAgentID},
		},
		{
			name:       "worker_duplicate_agent_id_scoped_to_projection",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusIdle, ObservedAt: testObserved}, // dup, known non-working
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantUnknownWorkers: intPtr(1),
			wantFresh:          intPtr(1),
			wantStale:          intPtr(0),
			wantReasonsContain: []string{ReasonDuplicateAgentID},
		},
		{
			name:       "working_missing_runtime_unknown",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "", Status: AgentStatusWorking, ObservedAt: testObserved},
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantUnknownWorkers: intPtr(1),
			wantReasonsContain: []string{ReasonWorkingMissingRuntime},
		},
		{
			name:       "working_missing_observed_at_unknown",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking}, // no ObservedAt
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantUnknownWorkers: intPtr(1),
			wantReasonsContain: []string{ReasonWorkingMissingObservedAt},
		},

		// --- Worker reconciliation invariant: fresh + stale/unbacked + known-nonworking + unknown == projection rows ---
		{
			name:       "worker_buckets_partition_projection_rows",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t2", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a2"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},                   // fresh
				{AgentID: "a2", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testNow.Add(-10 * time.Minute)}, // stale
				{AgentID: "a3", RuntimeID: "rt-1", Status: AgentStatusIdle, ObservedAt: testObserved},                      // known non-working
				{AgentID: "a4", RuntimeID: "rt-1", Status: AgentStatusArchived, ObservedAt: testObserved},                  // known non-working
				{AgentID: "a5", RuntimeID: "rt-1", Status: "bogus_status", ObservedAt: testObserved},                       // truly unknown
			},
			daemonRunning:       true,
			daemonActive:        2,
			cfg:                 defaultCfg,
			wantFresh:           intPtr(1),
			wantStale:           intPtr(1),
			wantKnownNonWorking: intPtr(2),
			wantUnknownWorkers:  intPtr(1),
			wantReasonsContain:  []string{ReasonUnknownAgentStatus},
			wantReasonsAbsent:   []string{ReasonReconciliationMismatch},
		},

		// --- Projection absent ---
		{
			name:       "projection_absent_no_workers",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			daemonRunning:      true,
			daemonActive:       1,
			cfg:                defaultCfg,
			wantProjAvailable:  boolPtr(false),
			wantProjected:      intPtr(0),
			wantReasonsContain: []string{ReasonAgentProjectionAbsent},
		},
		{
			name:       "projection_absent_projected_count_not_fabricated",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusDispatched, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t2", Status: StatusDispatched, RuntimeID: "rt-1", AgentID: "a2"},
				{ID: "t3", Status: StatusDispatched, RuntimeID: "rt-1", AgentID: "a3"},
			},
			daemonRunning:      true,
			daemonActive:       3, // daemon says 3, claimed=3, but no projection
			cfg:                defaultCfg,
			wantClaimed:        intPtr(3),
			wantProjAvailable:  boolPtr(false),
			wantProjected:      intPtr(0), // NOT min(3,3)=3 — fabricated value forbidden
			wantReasonsContain: []string{ReasonAgentProjectionAbsent},
		},

		// --- Daemon divergence independent of projection ---
		{
			name:       "daemon_divergence_independent_of_projection",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			daemonRunning:      true,
			daemonActive:       5, // daemon 5, claimed 1
			cfg:                defaultCfg,
			wantClaimed:        intPtr(1),
			wantProjAvailable:  boolPtr(false),
			wantProjected:      intPtr(0),
			wantDivergence:     intPtr(4), // |5 - 1|, still computed
			wantReasonsContain: []string{ReasonAgentProjectionAbsent},
		},
		{
			name:       "daemon_not_running_divergence_unknown",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
			},
			daemonRunning:      false,
			daemonActive:       0,
			cfg:                defaultCfg,
			wantRunning:        intPtr(1),
			wantDivergence:     intPtr(0), // daemon not running → 0
			wantReasonsContain: []string{ReasonDaemonNotRunning},
		},
		{
			name:       "projection_divergence_when_fresh_workers_below_claimed",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a1"},
				{ID: "t2", Status: StatusRunning, RuntimeID: "rt-1", AgentID: "a2"},
			},
			workers: []WorkerInput{
				{AgentID: "a1", RuntimeID: "rt-1", Status: AgentStatusWorking, ObservedAt: testObserved},
				// a2 is stale (not provided as working worker)
			},
			daemonRunning: true,
			daemonActive:  2,
			cfg:           defaultCfg,
			wantClaimed:   intPtr(2),
			wantFresh:     intPtr(1),
			wantProjected: intPtr(1),
			wantProjDiv:   intPtr(1), // |1 - 2|
		},

		// --- Reasons stable/sorted ---
		{
			name:       "reasons_sorted_stable_codes",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: "zebra_status", RuntimeID: "rt-1"},
				{ID: "t2", Status: "alpha_status", RuntimeID: "rt-1"},
				{ID: "t3", Status: StatusRunning, RuntimeID: ""},
			},
			daemonRunning: false,
			daemonActive:  0,
			cfg:           defaultCfg,
			wantReasonsContain: []string{
				ReasonDaemonNotRunning,
				ReasonMissingRuntimeID,
				ReasonUnknownTaskStatus,
			},
			// Raw statuses must not leak into reasons.
			wantReasonsAbsent: []string{"zebra", "alpha"},
		},
		{
			name:       "multiple_distinct_reasons_deduped",
			runtimeIDs: []string{"rt-1"},
			tasks: []TaskRow{
				{ID: "t1", Status: "bogus", RuntimeID: "rt-1"},
				{ID: "t2", Status: "bogus", RuntimeID: "rt-1"},
				{ID: "t3", Status: "bogus", RuntimeID: "rt-1"},
			},
			daemonRunning:      true,
			daemonActive:       0,
			cfg:                defaultCfg,
			wantReasonsContain: []string{ReasonUnknownTaskStatus},
			wantUnknownRows:    intPtr(3),
			wantReconciled:     boolPtr(true),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if cfg.Now.IsZero() && cfg.FreshnessThreshold == 0 {
				cfg = defaultCfg
			}

			r := ComputeWIPTruthCore(tt.runtimeIDs, tt.tasks, tt.workers, tt.daemonRunning, tt.daemonActive, cfg)

			// Runtime IDs.
			if tt.wantRuntimeIDs != nil {
				if len(r.RuntimeIDs) != len(tt.wantRuntimeIDs) {
					t.Fatalf("runtime_ids len = %d, want %d; got %v", len(r.RuntimeIDs), len(tt.wantRuntimeIDs), r.RuntimeIDs)
				}
				for i, id := range r.RuntimeIDs {
					if id != tt.wantRuntimeIDs[i] {
						t.Errorf("runtime_ids[%d] = %q, want %q", i, id, tt.wantRuntimeIDs[i])
					}
				}
			}

			// Histogram.
			checkInt(t, "queued", r.Server.Queued, tt.wantQueued)
			checkInt(t, "backlog", r.Server.Backlog, tt.wantBacklog)
			checkInt(t, "claimed", r.Server.Claimed, tt.wantClaimed)
			checkInt(t, "dispatched", r.Server.Dispatched, tt.wantDispatched)
			checkInt(t, "running", r.Server.Running, tt.wantRunning)
			checkInt(t, "waiting_local_directory", r.Server.WaitingLocalDirectory, tt.wantWaiting)

			// Reconciliation.
			checkInt(t, "scoped_rows", r.ScopedRows, tt.wantScopedRows)
			checkInt(t, "unknown_rows", r.UnknownRows, tt.wantUnknownRows)
			checkBool(t, "reconciled", r.Reconciled, tt.wantReconciled)

			// Workers.
			checkInt(t, "fresh", r.FreshMatchedWorkers, tt.wantFresh)
			checkInt(t, "stale", r.StaleOrUnbackedWorkers, tt.wantStale)
			checkInt(t, "known_non_working_workers", r.KnownNonWorkingWorkers, tt.wantKnownNonWorking)
			checkInt(t, "unknown_workers", r.UnknownWorkers, tt.wantUnknownWorkers)

			// Worker total invariant: fresh + stale/unbacked + known-nonworking + unknown == projection rows.
			workerTotal := r.FreshMatchedWorkers + r.StaleOrUnbackedWorkers + r.KnownNonWorkingWorkers + r.UnknownWorkers
			if workerTotal != len(tt.workers) {
				t.Errorf("worker total = %d, want %d (fresh=%d stale=%d known_non_working=%d unknown=%d)",
					workerTotal, len(tt.workers), r.FreshMatchedWorkers, r.StaleOrUnbackedWorkers, r.KnownNonWorkingWorkers, r.UnknownWorkers)
			}

			// Projection.
			checkBool(t, "projection_available", r.ProjectionAvailable, tt.wantProjAvailable)
			checkInt(t, "projected", r.ProjectedWorkingCount, tt.wantProjected)
			checkInt(t, "daemon_server_divergence", r.DaemonServerDivergence, tt.wantDivergence)
			checkInt(t, "projection_divergence", r.ProjectionDivergence, tt.wantProjDiv)

			// Projection-fabrication guard: when projection unavailable, projected must be 0.
			if !r.ProjectionAvailable && r.ProjectedWorkingCount != 0 {
				t.Errorf("projected_working_count = %d but projection unavailable — must be 0", r.ProjectedWorkingCount)
			}

			// Reasons.
			for _, want := range tt.wantReasonsContain {
				if !containsReason(r.UnknownReasons, want) {
					t.Errorf("missing reason %q in %v", want, r.UnknownReasons)
				}
			}
			for _, absent := range tt.wantReasonsAbsent {
				for _, got := range r.UnknownReasons {
					if contains(got, absent) {
						t.Errorf("unexpected reason containing %q: got %q in %v", absent, got, r.UnknownReasons)
					}
				}
			}

			// Reasons must be sorted.
			if !sort.StringsAreSorted(r.UnknownReasons) {
				t.Errorf("unknown_reasons not sorted: %v", r.UnknownReasons)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// reconcileHistogram — pure reconciliation helper
// ---------------------------------------------------------------------------

func TestReconcileHistogram(t *testing.T) {
	tests := []struct {
		name       string
		scoped     int
		queued     int
		backlog    int
		claimed    int
		unknown    int
		wantOK     bool
		wantReason string
	}{
		{"matching_zero", 0, 0, 0, 0, 0, true, ""},
		{"matching_all_buckets", 7, 1, 1, 3, 2, true, ""},
		{"matching_queued_only", 3, 3, 0, 0, 0, true, ""},
		{"matching_unknown_only", 2, 0, 0, 0, 2, true, ""},
		{"mismatch_under", 5, 1, 1, 1, 1, false, ReasonReconciliationMismatch},
		{"mismatch_over", 3, 1, 1, 1, 1, false, ReasonReconciliationMismatch},
		{"mismatch_zero_scoped_nonzero_buckets", 0, 1, 0, 0, 0, false, ReasonReconciliationMismatch},
		{"mismatch_negative_unknown", 3, 1, 1, 1, -1, false, ReasonReconciliationMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOK, gotReason := reconcileHistogram(tt.scoped, tt.queued, tt.backlog, tt.claimed, tt.unknown)
			if gotOK != tt.wantOK {
				t.Errorf("reconcileHistogram(%d,%d,%d,%d,%d) ok = %v, want %v",
					tt.scoped, tt.queued, tt.backlog, tt.claimed, tt.unknown, gotOK, tt.wantOK)
			}
			if gotReason != tt.wantReason {
				t.Errorf("reconcileHistogram(%d,%d,%d,%d,%d) reason = %q, want %q",
					tt.scoped, tt.queued, tt.backlog, tt.claimed, tt.unknown, gotReason, tt.wantReason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ComputeWIPTruth — compatibility wrapper
// ---------------------------------------------------------------------------

func TestComputeWIPTruth_Compat_RunningHealthy(t *testing.T) {
	snap := DaemonHealthSnapshot{
		Status:          "running",
		DaemonID:        "d-1",
		CLIVersion:      "1.2.3",
		ActiveTaskCount: 2,
		Workspaces: []struct {
			ID       string   `json:"id"`
			Runtimes []string `json:"runtimes"`
		}{
			{ID: "ws-1", Runtimes: []string{"rt-1", "rt-2"}},
		},
	}
	tasks := []ServerPendingTask{
		{ID: "t1", Status: "dispatched", RuntimeID: "rt-1"},
		{ID: "t2", Status: "running", RuntimeID: "rt-1"},
		{ID: "t3", Status: "queued", RuntimeID: "rt-1"},
	}

	r := ComputeWIPTruth(snap, tasks, testNow)

	if r.ObservedAt != testNow.UTC().Format(time.RFC3339) {
		t.Errorf("observed_at = %q, want %q", r.ObservedAt, testNow.UTC().Format(time.RFC3339))
	}
	if r.Daemon.Status != "running" {
		t.Errorf("daemon status = %q, want running", r.Daemon.Status)
	}
	if r.Daemon.ID != "d-1" {
		t.Errorf("daemon id = %q, want d-1", r.Daemon.ID)
	}
	if r.Daemon.Version != "1.2.3" {
		t.Errorf("daemon version = %q, want 1.2.3", r.Daemon.Version)
	}
	if r.Daemon.ActiveCount != 2 {
		t.Errorf("active_count = %d, want 2", r.Daemon.ActiveCount)
	}
	// Queued counted normally (no agent needed).
	if r.Server.Queued != 1 {
		t.Errorf("queued = %d, want 1", r.Server.Queued)
	}
	// Legacy input has no agent_id → active tasks are UNKNOWN.
	if r.Server.Claimed != 0 {
		t.Errorf("claimed = %d, want 0 (agent projection absent → honest UNKNOWN)", r.Server.Claimed)
	}
	if r.Server.Dispatched != 0 {
		t.Errorf("dispatched = %d, want 0 (no agent → unknown)", r.Server.Dispatched)
	}
	if r.Server.Running != 0 {
		t.Errorf("running = %d, want 0 (no agent → unknown)", r.Server.Running)
	}
	// Scoped: t1, t2 (active, unknown) + t3 (queued). Runtime rt-2 has no tasks.
	if r.ScopedRows != 3 {
		t.Errorf("scoped_rows = %d, want 3", r.ScopedRows)
	}
	if r.UnknownRows != 2 {
		t.Errorf("unknown_rows = %d, want 2 (t1, t2 missing agent)", r.UnknownRows)
	}
	if !r.Reconciled {
		t.Errorf("reconciled = false, want true (1 queued + 0 claimed + 2 unknown = 3 scoped)")
	}
	// Projection absent.
	if r.ProjectionAvailable {
		t.Errorf("projection_available = true, want false (no agent projection)")
	}
	if r.ProjectedWorkingCount != 0 {
		t.Errorf("projected = %d, want 0 (not fabricated)", r.ProjectedWorkingCount)
	}
	if r.FreshMatchedWorkers != 0 {
		t.Errorf("fresh = %d, want 0", r.FreshMatchedWorkers)
	}
	// Divergence still computed: |2 - 0| = 2.
	if r.DaemonServerDivergence != 2 {
		t.Errorf("divergence = %d, want 2", r.DaemonServerDivergence)
	}
	// Must contain agent projection absent and missing agent reasons.
	if !containsReason(r.UnknownReasons, ReasonAgentProjectionAbsent) {
		t.Errorf("missing %q in %v", ReasonAgentProjectionAbsent, r.UnknownReasons)
	}
	if !containsReason(r.UnknownReasons, ReasonMissingAgentID) {
		t.Errorf("missing %q in %v", ReasonMissingAgentID, r.UnknownReasons)
	}
	if !sort.StringsAreSorted(r.UnknownReasons) {
		t.Errorf("reasons not sorted: %v", r.UnknownReasons)
	}
}

func TestComputeWIPTruth_Compat_QueuedNotClaimed(t *testing.T) {
	snap := DaemonHealthSnapshot{
		Status:          "running",
		DaemonID:        "d-1",
		CLIVersion:      "1.0.0",
		ActiveTaskCount: 0,
		Workspaces: []struct {
			ID       string   `json:"id"`
			Runtimes []string `json:"runtimes"`
		}{
			{ID: "ws-1", Runtimes: []string{"rt-1"}},
		},
	}
	tasks := []ServerPendingTask{
		{ID: "t1", Status: "queued", RuntimeID: "rt-1"},
		{ID: "t2", Status: "queued", RuntimeID: "rt-1"},
		{ID: "t3", Status: "queued", RuntimeID: "rt-1"},
	}

	r := ComputeWIPTruth(snap, tasks, testNow)

	if r.Server.Queued != 3 {
		t.Errorf("queued = %d, want 3", r.Server.Queued)
	}
	if r.Server.Claimed != 0 {
		t.Errorf("claimed = %d, want 0", r.Server.Claimed)
	}
	if r.UnknownRows != 0 {
		t.Errorf("unknown_rows = %d, want 0", r.UnknownRows)
	}
	if !r.Reconciled {
		t.Errorf("reconciled = false, want true")
	}
	// Queued tasks don't need agent, but projection is still absent.
	if !containsReason(r.UnknownReasons, ReasonAgentProjectionAbsent) {
		t.Errorf("missing %q in %v", ReasonAgentProjectionAbsent, r.UnknownReasons)
	}
}

func TestComputeWIPTruth_Compat_DaemonNotRunning(t *testing.T) {
	snap := DaemonHealthSnapshot{
		Status:     "starting",
		DaemonID:   "d-1",
		CLIVersion: "1.0.0",
		Workspaces: []struct {
			ID       string   `json:"id"`
			Runtimes []string `json:"runtimes"`
		}{
			{ID: "ws-1", Runtimes: []string{"rt-1"}},
		},
	}

	r := ComputeWIPTruth(snap, nil, testNow)

	if r.Daemon.Status != "starting" {
		t.Errorf("status = %q, want starting", r.Daemon.Status)
	}
	if r.Daemon.ActiveCount != 0 {
		t.Errorf("active_count = %d, want 0 (untrusted)", r.Daemon.ActiveCount)
	}
	if !containsReason(r.UnknownReasons, ReasonDaemonNotRunning) {
		t.Errorf("missing %q in %v", ReasonDaemonNotRunning, r.UnknownReasons)
	}
	if !containsReason(r.UnknownReasons, ReasonAgentProjectionAbsent) {
		t.Errorf("missing %q in %v", ReasonAgentProjectionAbsent, r.UnknownReasons)
	}
}

func TestComputeWIPTruth_Compat_EmptyStatus_UNKNOWN(t *testing.T) {
	snap := DaemonHealthSnapshot{}

	r := ComputeWIPTruth(snap, nil, testNow)

	if r.Daemon.Status != "UNKNOWN" {
		t.Errorf("status = %q, want UNKNOWN", r.Daemon.Status)
	}
	if !containsReason(r.UnknownReasons, ReasonDaemonStatusMissing) {
		t.Errorf("missing %q in %v", ReasonDaemonStatusMissing, r.UnknownReasons)
	}
	if !containsReason(r.UnknownReasons, ReasonAgentProjectionAbsent) {
		t.Errorf("missing %q in %v", ReasonAgentProjectionAbsent, r.UnknownReasons)
	}
}

func TestComputeWIPTruth_Compat_NoRuntimes(t *testing.T) {
	snap := DaemonHealthSnapshot{
		Status:     "running",
		DaemonID:   "d-1",
		CLIVersion: "1.0.0",
	}

	r := ComputeWIPTruth(snap, nil, testNow)

	if !containsReason(r.UnknownReasons, ReasonNoRuntimeIDsScoped) {
		t.Errorf("missing %q in %v", ReasonNoRuntimeIDsScoped, r.UnknownReasons)
	}
}

func TestComputeWIPTruth_Compat_ForeignRuntimeIgnored(t *testing.T) {
	snap := DaemonHealthSnapshot{
		Status:          "running",
		DaemonID:        "d-1",
		CLIVersion:      "1.0.0",
		ActiveTaskCount: 0,
		Workspaces: []struct {
			ID       string   `json:"id"`
			Runtimes []string `json:"runtimes"`
		}{
			{ID: "ws-1", Runtimes: []string{"rt-1"}},
		},
	}
	tasks := []ServerPendingTask{
		{ID: "t1", Status: "running", RuntimeID: "rt-1"},
		{ID: "t2", Status: "running", RuntimeID: "rt-OTHER"},
	}

	r := ComputeWIPTruth(snap, tasks, testNow)

	// rt-1 running task: no agent_id in legacy input → UNKNOWN (not counted as running).
	if r.Server.Running != 0 {
		t.Errorf("running = %d, want 0 (no agent_id → unknown)", r.Server.Running)
	}
	if r.Server.Claimed != 0 {
		t.Errorf("claimed = %d, want 0", r.Server.Claimed)
	}
	// Foreign runtime completely ignored.
	if r.ScopedRows != 1 {
		t.Errorf("scoped_rows = %d, want 1 (foreign ignored)", r.ScopedRows)
	}
	if r.UnknownRows != 1 {
		t.Errorf("unknown_rows = %d, want 1", r.UnknownRows)
	}
}

func TestComputeWIPTruth_Compat_RuntimeIDsSortedDedup(t *testing.T) {
	snap := DaemonHealthSnapshot{
		Status:          "running",
		DaemonID:        "d-1",
		CLIVersion:      "1.0.0",
		ActiveTaskCount: 0,
		Workspaces: []struct {
			ID       string   `json:"id"`
			Runtimes []string `json:"runtimes"`
		}{
			{ID: "ws-1", Runtimes: []string{"rt-c", "rt-a", "", ""}},
			{ID: "ws-2", Runtimes: []string{"rt-b", "rt-a", ""}},
		},
	}

	r := ComputeWIPTruth(snap, nil, testNow)

	want := []string{"rt-a", "rt-b", "rt-c"}
	if len(r.RuntimeIDs) != len(want) {
		t.Fatalf("runtime_ids len = %d, want %d; got %v", len(r.RuntimeIDs), len(want), r.RuntimeIDs)
	}
	for i, id := range r.RuntimeIDs {
		if id != want[i] {
			t.Errorf("runtime_ids[%d] = %q, want %q", i, id, want[i])
		}
	}
}

func TestComputeWIPTruth_Compat_UnknownStatus(t *testing.T) {
	snap := DaemonHealthSnapshot{
		Status:          "running",
		DaemonID:        "d-1",
		CLIVersion:      "1.0.0",
		ActiveTaskCount: 0,
		Workspaces: []struct {
			ID       string   `json:"id"`
			Runtimes []string `json:"runtimes"`
		}{
			{ID: "ws-1", Runtimes: []string{"rt-1"}},
		},
	}
	tasks := []ServerPendingTask{
		{ID: "t1", Status: "bogus_status", RuntimeID: "rt-1"},
	}

	r := ComputeWIPTruth(snap, tasks, testNow)

	if !containsReason(r.UnknownReasons, ReasonUnknownTaskStatus) {
		t.Errorf("missing %q in %v", ReasonUnknownTaskStatus, r.UnknownReasons)
	}
	// Raw status must not leak.
	for _, reason := range r.UnknownReasons {
		if contains(reason, "bogus") {
			t.Errorf("raw status leaked into reason: %q", reason)
		}
	}
}

func TestComputeWIPTruth_Compat_ProjectionNeverFabricated(t *testing.T) {
	// Even with many claimed tasks and high daemon active count, legacy input
	// must NEVER fabricate ProjectedWorkingCount.
	snap := DaemonHealthSnapshot{
		Status:          "running",
		DaemonID:        "d-1",
		CLIVersion:      "1.0.0",
		ActiveTaskCount: 99,
		Workspaces: []struct {
			ID       string   `json:"id"`
			Runtimes []string `json:"runtimes"`
		}{
			{ID: "ws-1", Runtimes: []string{"rt-1"}},
		},
	}
	tasks := []ServerPendingTask{
		{ID: "t1", Status: "dispatched", RuntimeID: "rt-1"},
		{ID: "t2", Status: "dispatched", RuntimeID: "rt-1"},
		{ID: "t3", Status: "dispatched", RuntimeID: "rt-1"},
	}

	r := ComputeWIPTruth(snap, tasks, testNow)

	if r.ProjectionAvailable {
		t.Errorf("projection_available = true, want false")
	}
	if r.ProjectedWorkingCount != 0 {
		t.Errorf("projected = %d, want 0 — never fabricated from daemon/claimed", r.ProjectedWorkingCount)
	}
	if !containsReason(r.UnknownReasons, ReasonAgentProjectionAbsent) {
		t.Errorf("missing %q", ReasonAgentProjectionAbsent)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func checkInt(t *testing.T, label string, got int, want *int) {
	t.Helper()
	if want == nil {
		return
	}
	if got != *want {
		t.Errorf("%s = %d, want %d", label, got, *want)
	}
}

func checkBool(t *testing.T, label string, got bool, want *bool) {
	t.Helper()
	if want == nil {
		return
	}
	if got != *want {
		t.Errorf("%s = %v, want %v", label, got, *want)
	}
}

func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func containsReason(reasons []string, target string) bool {
	for _, r := range reasons {
		if r == target {
			return true
		}
	}
	return false
}
