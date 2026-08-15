// Package liveactivity implements the W4 LIVE-WORKSITE read model.
//
// EmployeeLiveActivityV1 is the single public wire contract for the "工作现场"
// real-time work wall. This package only DERIVES presence/work_stage from
// already-authoritative facts (runtime heartbeat, task/run state, activity).
// It is a read projection: it never writes Task/Run/Project state and never
// becomes a second source of truth.
//
// Fail-closed rules (from HIVECREW-LIVE-WORKSITE-V1):
//   - Issue in_progress/in_review does NOT prove working or reviewing.
//   - Agent online does NOT prove working.
//   - reviewing/repairing require exact Task/Workflow evidence.
//   - stale heartbeat / source conflict / insufficient evidence -> unknown.
package liveactivity

// PresenceState is the closed presence enum for the work wall.
type PresenceState string

const (
	PresenceOffline           PresenceState = "offline"
	PresenceIdle              PresenceState = "idle"
	PresenceQueued            PresenceState = "queued"
	PresenceWorking           PresenceState = "working"
	PresenceWaiting           PresenceState = "waiting"
	PresenceBlocked           PresenceState = "blocked"
	PresenceRecentlyCompleted PresenceState = "recently_completed"
	PresenceUnknown           PresenceState = "unknown"
)

// WorkStage is the closed work-stage enum for the work wall.
type WorkStage string

const (
	StagePlanning    WorkStage = "planning"
	StageResearch    WorkStage = "research"
	StageCoding      WorkStage = "coding"
	StageTesting     WorkStage = "testing"
	StageReviewing   WorkStage = "reviewing"
	StageRepairing   WorkStage = "repairing"
	StageIntegrating WorkStage = "integrating"
	StageOperating   WorkStage = "operating"
	StageReporting   WorkStage = "reporting"
	StageNone        WorkStage = "none"
	StageUnknown     WorkStage = "unknown"
)

// Inputs is the per-employee snapshot consumed by the derivation. Every field
// must come from an authoritative source (runtime heartbeat, task/run rows,
// activity log); the caller, not this package, is responsible for provenance.
type Inputs struct {
	RuntimeOnline     bool
	HasOpenTask       bool
	TaskQueued        bool // queued/dispatched, run not started yet
	RunStarted        bool
	RunExecuting      bool
	HeartbeatFresh    bool
	WaitingReason     string
	BlockedReason     string
	RecentlyCompleted bool // task terminal within recent TTL
	StageHint         WorkStage
	ReviewEvidence    bool // exact Task/Workflow evidence of a review
	RepairEvidence    bool // exact Task/Workflow evidence of a repair
}

// DerivePresence returns the presence state using a fixed priority order.
//
// Priority (higher wins):
//  1. offline        — runtime is not online
//  2. recently_completed — task terminal within recent TTL (before idle)
//  3. idle           — online with no open task
//  4. blocked        — explicit blocker on run/workflow
//  5. waiting        — explicit waiting reason on run
//  6. queued         — task queued/dispatched, run not started
//  7. working        — run executing with fresh heartbeat
//  8. unknown        — stale heartbeat, source conflict, or insufficient evidence
func DerivePresence(in Inputs) PresenceState {
	switch {
	case !in.RuntimeOnline:
		return PresenceOffline
	case in.RecentlyCompleted:
		return PresenceRecentlyCompleted
	case !in.HasOpenTask:
		return PresenceIdle
	case in.BlockedReason != "":
		return PresenceBlocked
	case in.WaitingReason != "":
		return PresenceWaiting
	case in.TaskQueued && !in.RunStarted:
		return PresenceQueued
	case in.RunExecuting && in.HeartbeatFresh:
		return PresenceWorking
	default:
		return PresenceUnknown
	}
}

// DeriveWorkStage returns the work stage. reviewing and repairing are
// fail-closed: they require exact evidence and are never inferred from an
// in_review Issue or a generic stage hint.
func DeriveWorkStage(in Inputs, p PresenceState) WorkStage {
	switch {
	case in.ReviewEvidence:
		return StageReviewing
	case in.RepairEvidence:
		return StageRepairing
	case in.StageHint != "" && isValidStage(in.StageHint):
		return in.StageHint
	}

	switch p {
	case PresenceWorking, PresenceWaiting, PresenceBlocked, PresenceQueued:
		return StageUnknown
	default:
		return StageNone
	}
}

func isValidStage(s WorkStage) bool {
	switch s {
	case StagePlanning, StageResearch, StageCoding, StageTesting,
		StageIntegrating, StageOperating, StageReporting:
		return true
	default:
		return false
	}
}
