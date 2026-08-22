package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// workConservingDrainBatchSize is the frozen per-tick fan-out. One tick
// dispatches at most one ready Issue; catching up belongs to the next tick,
// never to a wider batch inside this one.
const workConservingDrainBatchSize = 1

// workConservingDrainOwnerRole is the only member role that may author the
// automatic drain.
const workConservingDrainOwnerRole = "owner"

// Job-level no-write states. They reuse the drain's honest-state vocabulary so
// the Projects surface keeps showing source_gap/stalled instead of a silent
// no-op.
const (
	// workConservingDrainOwnerMissing: the bound workspace has no valid Owner.
	workConservingDrainOwnerMissing = "owner_missing"
	// workConservingDrainOwnerAmbiguous: the bound workspace has more than one
	// valid Owner. No deterministic winner is ever selected.
	workConservingDrainOwnerAmbiguous = "owner_ambiguous"
)

// WorkConservingDrainRunner is the one-call-per-tick drain seam.
// *service.WorkConservingDrainService satisfies it.
type WorkConservingDrainRunner interface {
	Drain(ctx context.Context, req service.WorkConservingDrainRequest) (service.WorkConservingDrainResult, error)
}

// WorkConservingDrainOwnerReader lists workspace members for the strict
// single-Owner resolution. *db.Queries satisfies it.
type WorkConservingDrainOwnerReader interface {
	ListMembers(ctx context.Context, workspaceID pgtype.UUID) ([]db.Member, error)
}

// WorkConservingGoalBindingReader resolves the explicit Goal binding from the
// configured source path. service.ReadWorkConservingGoalBinding satisfies it.
type WorkConservingGoalBindingReader func(path string) (service.WorkConservingGoalSourceBinding, error)

// WorkConservingDrainJob returns the scheduler-backed automatic work-conserving
// drain: one global scope, one Drain call per tick, batch 1. The job is a
// one-shot bridge from a completion event (or the 1-minute cadence) to at most
// one new Task plus receipt; the drain's own projection, authority, quota, WIP,
// write-lease and receipt checks are unchanged and unrepeated here.
//
// The spec is non-reentrant on purpose: the drain writes a Task plus receipt,
// so a stale RUNNING lease must go FAILED with stale_timeout and require manual
// repair instead of being stolen by a second concurrent writer.
func WorkConservingDrainJob(drain WorkConservingDrainRunner, owners WorkConservingDrainOwnerReader, goalPath string) JobSpec {
	return workConservingDrainJobSpec(drain, owners, service.ReadWorkConservingGoalBinding, goalPath)
}

func workConservingDrainJobSpec(drain WorkConservingDrainRunner, owners WorkConservingDrainOwnerReader, readBinding WorkConservingGoalBindingReader, goalPath string) JobSpec {
	return JobSpec{
		Name:              "work_conserving_drain",
		Cadence:           1 * time.Minute,
		ScheduleDelay:     30 * time.Second,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     10 * time.Minute,
		MaxPlansPerTick:   1,
		RunTimeout:        5 * time.Minute,
		StaleTimeout:      15 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: false,
		MaxAttempts:       1,
		Scopes:            StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
			return runWorkConservingDrainTick(ctx, drain, owners, readBinding, goalPath)
		},
	}
}

// runWorkConservingDrainTick performs at most one Drain call. Resolution
// failures (unreadable Goal source, missing or ambiguous Owner) fail closed
// with a truthful zero-write state; only genuine runtime failures (member read
// error, whole-drain error) return an error so the execution lands FAILED.
func runWorkConservingDrainTick(ctx context.Context, drain WorkConservingDrainRunner, owners WorkConservingDrainOwnerReader, readBinding WorkConservingGoalBindingReader, goalPath string) (HandlerResult, error) {
	if drain == nil || owners == nil || readBinding == nil {
		return HandlerResult{}, fmt.Errorf("work-conserving drain job is not configured")
	}
	binding, err := readBinding(goalPath)
	if err != nil {
		return HandlerResult{Result: map[string]any{
			"state": service.WorkConservingDrainStateSourceGap, "reason": "goal_binding",
		}}, nil
	}
	workspaceID, err := workConservingBindingUUID(binding.WorkspaceID, "workspace_id")
	if err != nil {
		return HandlerResult{Result: map[string]any{
			"state": service.WorkConservingDrainStateSourceGap, "reason": "goal_binding",
		}}, nil
	}
	projectID, err := workConservingBindingUUID(binding.ProjectID, "project_id")
	if err != nil {
		return HandlerResult{Result: map[string]any{
			"state": service.WorkConservingDrainStateSourceGap, "reason": "goal_binding",
		}}, nil
	}
	actorUserID, ownerState, err := workConservingDrainOwner(ctx, owners, workspaceID)
	if err != nil {
		return HandlerResult{}, err
	}
	if ownerState != "" {
		return HandlerResult{Result: map[string]any{"state": ownerState}}, nil
	}
	result, err := drain.Drain(ctx, service.WorkConservingDrainRequest{
		WorkspaceID: workspaceID, ProjectID: projectID, ActorUserID: actorUserID,
		BatchSize: workConservingDrainBatchSize,
	})
	if err != nil {
		return HandlerResult{Result: map[string]any{
			"state": result.State, "reason_code": result.ReasonCode,
		}}, fmt.Errorf("work-conserving drain: %w", err)
	}
	outcome := map[string]any{
		"state": result.State, "reason_code": result.ReasonCode, "goal_id": result.GoalID,
		"batch_size": result.BatchSize, "dispatched": result.Dispatched,
		"conflicts": result.Conflicts, "blocked": result.Blocked,
		"source_gaps": result.SourceGaps, "already_terminal": result.AlreadyTerminal,
		"deferred_suggestions": result.DeferredSuggestions,
	}
	for _, row := range result.Results {
		if row.Outcome == service.WorkConservingDrainConflict {
			outcome["conflict_reason"] = row.Reason
			break
		}
	}
	return HandlerResult{RowsAffected: int64(result.Dispatched), Result: outcome}, nil
}

// workConservingDrainOwner resolves the exactly-one workspace Owner that
// authors the automatic drain. Only rows in the bound workspace with the owner
// role and a valid non-zero user id count. Zero or multiple valid Owners fail
// closed with a no-write state; no deterministic winner is ever selected.
func workConservingDrainOwner(ctx context.Context, owners WorkConservingDrainOwnerReader, workspaceID pgtype.UUID) (pgtype.UUID, string, error) {
	members, err := owners.ListMembers(ctx, workspaceID)
	if err != nil {
		return pgtype.UUID{}, "", fmt.Errorf("work-conserving drain: list members: %w", err)
	}
	var owner pgtype.UUID
	count := 0
	for _, member := range members {
		if member.WorkspaceID != workspaceID || member.Role != workConservingDrainOwnerRole || !member.UserID.Valid || member.UserID.Bytes == ([16]byte{}) {
			continue
		}
		count++
		if count == 1 {
			owner = member.UserID
		}
	}
	switch {
	case count == 0:
		return pgtype.UUID{}, workConservingDrainOwnerMissing, nil
	case count > 1:
		return pgtype.UUID{}, workConservingDrainOwnerAmbiguous, nil
	default:
		return owner, "", nil
	}
}

// workConservingBindingUUID parses one Goal binding identifier. The binding is
// only trusted when it scans to a valid canonical UUID; anything else keeps
// the tick in the source-gap state with zero writes.
func workConservingBindingUUID(value, field string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid || id.Bytes == ([16]byte{}) {
		return pgtype.UUID{}, fmt.Errorf("goal binding %s %q is not a canonical uuid", field, value)
	}
	return id, nil
}
