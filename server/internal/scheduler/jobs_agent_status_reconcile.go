package scheduler

import (
	"context"
	"fmt"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// JobNameAgentStatusReconcile is the canonical name used in audit rows.
// Stable across releases — do not rename without a migration.
const JobNameAgentStatusReconcile = "agent_status_reconcile"

// AgentStatusReconcileJob returns the JobSpec that periodically converges
// agent.status to the active-task predicate (HCOPS-V3 audit R4 / B1+B5).
//
// agent.status is a derived column: task transitions call the per-agent
// RefreshAgentStatusFromTasks AFTER their transaction commits, so a crash
// between the two steps — or a bulk cancel / offline-runtime fail path that
// misses one recompute — leaves the column stale until the agent's next task
// transition. This job re-derives the whole population from committed task
// rows on every tick, bounding the drift window instead of relying on the
// next transition to self-heal.
//
// Nominal convergence window: with a healthy, unblocked scheduler, the
// 30s cadence plus the manager's default 30s TickInterval corrects a
// dirty row within about 60s of it appearing. That figure is NOMINAL,
// not a hard guarantee: the manager runs the registered jobs serially
// within each tick, so a long-running handler ahead of this job (or a
// stalled scheduler) can delay a reconcile past the nominal window
// until the scheduler is healthy again. What is guaranteed is that
// every healthy tick re-derives the full population, so drift cannot
// outlive the scheduler's own recovery. Steady state costs one indexed
// set-based UPDATE that reports 0 rows affected and writes nothing
// (the query skips already-correct rows), so the job is cheap to run
// continuously.
//
// Concurrency safety: the handler is a single UPDATE over rows the
// per-agent RefreshAgentStatusFromTasks also writes, using the same
// predicate. The two statements take their snapshots at different
// times, so an interleave CAN race transiently: if a task transition
// commits between this job's snapshot and its UPDATE (or between the
// per-agent refresh's snapshot and its write), the last writer may
// briefly persist a value derived from an older task snapshot. That is
// benign and bounded — no lock-order hazard, no torn state — because
// both writers derive from the same predicate over committed task
// rows, and any transiently stale value is healed by the next healthy
// tick (or the agent's next transition). The job never touches
// business transition points and publishes no bus events; readers
// (employee lists, Work Wall snapshot) re-read the column on their
// next poll, which is already <=5s.
func AgentStatusReconcileJob(queries *db.Queries) JobSpec {
	return JobSpec{
		Name:              JobNameAgentStatusReconcile,
		Cadence:           30 * time.Second,
		ScheduleDelay:     0,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     10 * time.Minute,
		MaxPlansPerTick:   1,
		RunTimeout:        2 * time.Minute,
		StaleTimeout:      5 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff:      []time.Duration{30 * time.Second, 2 * time.Minute},
		Scopes:            StaticScopes(ScopeGlobal),
		Handler:           makeAgentStatusReconcileHandler(queries),
	}
}

func makeAgentStatusReconcileHandler(queries *db.Queries) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		if queries == nil {
			return HandlerResult{}, fmt.Errorf("agent status reconcile: queries is required")
		}
		corrected, err := queries.ReconcileAllAgentStatuses(ctx)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("agent status reconcile: %w", err)
		}
		return HandlerResult{
			RowsAffected: corrected,
			Result: map[string]any{
				"corrected": corrected,
				// Surface the predicate so audit rows are self-describing
				// when the active-status set later changes.
				"active_statuses": []string{"dispatched", "running", "waiting_local_directory"},
			},
		}, nil
	}
}
