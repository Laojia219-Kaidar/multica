// Package workwall assembles the W4 "工作现场" (work wall) snapshot from the
// HiveCrew execution projection (agent / agent_runtime / agent_task_queue).
// It is a read projection: it never writes state and never becomes a second
// source of truth. Formal Employee identity is overlaid from the CompanyOps
// directory authority (HIV-854); it is never synthesized from local rows,
// terminal hints, process names or tail text.
package workwall

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// defaultStaleThreshold mirrors runtime_sweeper.go staleThresholdSeconds (150s):
// a runtime whose last_seen_at is older than this is not trusted as "fresh".
const defaultStaleThreshold = 150 * time.Second

// recentCompletedTTL is how long after a terminal task an agent shows as
// recently_completed instead of idle.
const recentCompletedTTL = 5 * time.Minute

// activeTaskStatuses are the nonterminal agent_task_queue.status values that
// count as "currently working" (mirrors ListWorkspaceAgentTaskSnapshot).
func isActiveTaskStatus(s string) bool {
	switch s {
	case "queued", "dispatched", "running", "waiting_local_directory":
		return true
	default:
		return false
	}
}

func uuidStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return u.String()
}

func textStr(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	tt := t.Time
	return &tt
}

// runtimeState classifies a runtime into online / offline / stale / missing.
type runtimeState struct {
	online  bool
	stale   bool
	missing bool
}

func classifyRuntime(rt *db.AgentRuntime, now time.Time, threshold time.Duration) runtimeState {
	if rt == nil {
		return runtimeState{missing: true}
	}
	if rt.Status == "offline" {
		return runtimeState{online: false}
	}
	// Status is online (or unknown): trust it only with a fresh heartbeat.
	if !rt.LastSeenAt.Valid || now.Sub(rt.LastSeenAt.Time) > threshold {
		return runtimeState{online: true, stale: true}
	}
	return runtimeState{online: true}
}

// EmployeeAuthorityState classifies the formal Employee identity evidence for
// one Agent card, resolved from the CompanyOps directory seam.
type EmployeeAuthorityState int

const (
	// EmployeeAuthorityNone: no authority row names this Agent. The card stays
	// a legitimate Agent-only projection; authority itself is healthy, so the
	// runtime freshness semantics are untouched.
	EmployeeAuthorityNone EmployeeAuthorityState = iota
	// EmployeeAuthorityVerified: exactly one complete, exact and unambiguous
	// Employee↔Agent pairing exists across both directory reads.
	EmployeeAuthorityVerified
	// EmployeeAuthorityGap: the authority evidence for this card failed —
	// unavailable, malformed, incomplete, duplicated or conflicting. The card
	// is preserved as an Agent-only projection, formal fields stay cleared,
	// and the card can never look `fresh`.
	EmployeeAuthorityGap
)

// EmployeeIdentity is the formal Employee evidence overlaid on one card. Every
// field is copied verbatim from an authoritative CompanyOps row; nothing is
// inferred or defaulted.
type EmployeeIdentity struct {
	// EmployeeID is the formal DE-… employee identifier.
	EmployeeID string
	// DisplayName is the formal employee display name.
	DisplayName string
	// DepartmentID / DepartmentName / PositionID / PositionTitle are the
	// formal organization fields from the employee summary.
	DepartmentID   string
	DepartmentName string
	PositionID     string
	PositionTitle  string
	// BaseMachineTitle is the verified Base from the workforce base-runtime
	// join: the observed execution location derived from the bound runtime's
	// device info. The join carries no separate Base registry ID, so the DTO
	// BaseID stays empty rather than being fabricated.
	BaseMachineTitle string
}

// EmployeeAuthority carries the resolved Employee authority evidence for one
// Agent card. A nil *EmployeeAuthority means EmployeeAuthorityNone.
type EmployeeAuthority struct {
	State    EmployeeAuthorityState
	Identity *EmployeeIdentity // non-nil only for EmployeeAuthorityVerified
}

// AssembleAgent is the frozen v0 assembler entry point kept for existing
// callers: it assembles an Agent-only card with no Employee authority
// evidence (authority nil). employee_id stays empty — it no longer mirrors
// agent_id; the formal identity is applied only by AssembleAgentCard with
// verified authority evidence.
func AssembleAgent(
	agent db.Agent,
	rt *db.AgentRuntime,
	activeTask *db.AgentTaskQueue,
	lastOutcome *db.AgentTaskQueue,
	chain *ExecutionChain,
	activities []db.ActivityLog,
	now time.Time,
	staleThreshold time.Duration,
) liveactivity.EmployeeLiveActivityV1 {
	return AssembleAgentCard(agent, rt, activeTask, lastOutcome, chain, nil, activities, now, staleThreshold)
}

// AssembleAgentCard maps one agent + its runtime + its active task / last
// outcome into a sanitized EmployeeLiveActivityV1. It is a pure function
// (no I/O) so the derivation is unit-testable without a database.
//
// chain carries the hydrated Project/Issue/Run/Receipt/Profile evidence for
// the task currently shown (active, or the recent terminal task when idle);
// it may be nil (no task / no evidence) and never overrides task-owned
// identifiers.
//
// authority carries the CompanyOps Employee directory evidence (HIV-854).
// Verified evidence overlays ONLY the formal Employee fields (employee id,
// name, department, position, verified Base) and appends one employee://
// source ref; it never overrides Agent-owned identifiers or the execution
// chain. A gap degrades the generic freshness state (never `fresh`) but
// leaves presence, chain and events untouched.
func AssembleAgentCard(
	agent db.Agent,
	rt *db.AgentRuntime,
	activeTask *db.AgentTaskQueue,
	lastOutcome *db.AgentTaskQueue,
	chain *ExecutionChain,
	authority *EmployeeAuthority,
	activities []db.ActivityLog,
	now time.Time,
	staleThreshold time.Duration,
) liveactivity.EmployeeLiveActivityV1 {
	if staleThreshold <= 0 {
		staleThreshold = defaultStaleThreshold
	}
	rs := classifyRuntime(rt, now, staleThreshold)

	agentID := uuidStr(agent.ID)
	runtimeID := ""
	if rt != nil {
		runtimeID = uuidStr(rt.ID)
	} else {
		runtimeID = uuidStr(agent.RuntimeID)
	}

	// Formal Employee identity starts cleared. It is filled only from
	// verified CompanyOps authority evidence below — never mirrored from the
	// Agent row and never guessed from terminal hints or process names.
	in := liveactivity.SnapshotInput{
		WorkspaceID: uuidStr(agent.WorkspaceID),
		AgentID:     agentID,
		DisplayName: agent.Name,
		AvatarURL:   textStr(agent.AvatarUrl),
		ModelName:   textStr(agent.Model),
		RuntimeID:   runtimeID,
		SourceRefs:  []string{"agent://" + agentID},
	}

	if rt != nil {
		in.RuntimeCarrier = rt.Provider
		in.LastHeartbeatAt = tsPtr(rt.LastSeenAt)
		in.SourceRefs = append(in.SourceRefs, "runtime://"+runtimeID)
	}

	der := liveactivity.Inputs{RuntimeOnline: rs.online}
	switch {
	case rs.missing, rs.stale:
		in.FreshnessState = liveactivity.FreshnessStale
		if rs.missing {
			in.FreshnessState = liveactivity.FreshnessMissing
		}
	default:
		in.FreshnessState = liveactivity.FreshnessFresh
	}

	if activeTask != nil {
		der.HasOpenTask = true
		in.TaskID = uuidStr(activeTask.ID)
		in.IssueID = uuidStr(activeTask.IssueID)
		in.QueuedAt = tsPtr(activeTask.DispatchedAt)
		in.StartedAt = tsPtr(activeTask.StartedAt)
		in.SourceRefs = append(in.SourceRefs, "task://"+in.TaskID)
	} else if chain != nil && chain.TaskID != "" {
		// Recently completed/failed: the card still traces the exact task
		// that produced the outcome (presence derivation is untouched;
		// IssueID comes from the chain overlay below when evidence exists).
		in.TaskID = chain.TaskID
		in.SourceRefs = append(in.SourceRefs, "task://"+in.TaskID)
	}

	if activeTask != nil {
		switch activeTask.Status {
		case "queued", "dispatched":
			der.TaskQueued = true
			der.RunStarted = false
		case "running":
			der.RunStarted = true
			der.RunExecuting = true
			der.HeartbeatFresh = rs.online && !rs.stale
		case "waiting_local_directory":
			der.RunStarted = true
			der.RunExecuting = true
			der.HeartbeatFresh = rs.online && !rs.stale
			der.WaitingReason = textStr(activeTask.WaitReason)
			if der.WaitingReason == "" {
				der.WaitingReason = "waiting for local directory"
			}
		}
	}

	if activeTask == nil && lastOutcome != nil && lastOutcome.CompletedAt.Valid {
		if since := now.Sub(lastOutcome.CompletedAt.Time); since >= 0 && since <= recentCompletedTTL {
			der.RecentlyCompleted = true
		}
		in.LastEventAt = tsPtr(lastOutcome.CompletedAt)
	}

	// Execution-chain projection (HIV-797): overlay the hydrated
	// Project/Issue/Run/Receipt/Profile evidence. Chain fields are empty when
	// the authoritative row is absent; they never fabricate identifiers.
	if chain != nil {
		if chain.IssueID != "" {
			in.IssueID = chain.IssueID
		}
		in.IssueIdentifier = chain.IssueIdentifier
		in.IssueTitle = chain.IssueTitle
		in.ProjectID = chain.ProjectID
		in.ProjectTitle = chain.ProjectTitle
		in.RunID = chain.RunID
		in.RuntimeProfileID = chain.RuntimeProfileID
		in.RuntimeProfileName = chain.RuntimeProfileName
		in.ExecutionReceiptRef = chain.ExecutionReceiptRef
		in.ExecutionReceiptStatus = chain.ExecutionReceiptStatus
		// Execution-runtime projection (HIV-940): the Task's original
		// runtime/carrier/profile. Empty when the Task has no runtime_id or
		// the runtime row is missing. Never substituted from the current
		// Agent runtime binding.
		in.ExecutionRuntimeID = chain.ExecutionRuntimeID
		in.ExecutionRuntimeCarrier = chain.ExecutionRuntimeCarrier
		in.ExecutionProfileID = chain.ExecutionProfileID
		in.ExecutionProfileName = chain.ExecutionProfileName
		if chain.IssueID != "" {
			in.SourceRefs = append(in.SourceRefs, "issue://"+chain.IssueID)
		}
		if chain.ProjectID != "" {
			in.SourceRefs = append(in.SourceRefs, "project://"+chain.ProjectID)
		}
		if chain.RuntimeProfileID != "" {
			in.SourceRefs = append(in.SourceRefs, "profile://"+chain.RuntimeProfileID)
		}
		if chain.ExecutionReceiptRef != "" {
			in.SourceRefs = append(in.SourceRefs, chain.ExecutionReceiptRef)
		}
		if chain.ExecutionRuntimeID != "" {
			in.SourceRefs = append(in.SourceRefs, "exec-runtime://"+chain.ExecutionRuntimeID)
		}
		if chain.ExecutionProfileID != "" {
			in.SourceRefs = append(in.SourceRefs, "exec-profile://"+chain.ExecutionProfileID)
		}
	}

	// Employee authority overlay (HIV-854). Verified evidence overlays only
	// the formal Employee fields; every failed-evidence state keeps the card
	// as an Agent-only projection with the formal fields cleared. A gap can
	// never leave the card looking `fresh`: the generic degraded freshness
	// state (conflict, already localized on the wall) replaces `fresh`, while
	// stricter runtime classifications (stale, missing) stay as they are.
	if authority != nil {
		switch authority.State {
		case EmployeeAuthorityVerified:
			if identity := authority.Identity; identity != nil {
				in.EmployeeID = identity.EmployeeID
				in.DisplayName = identity.DisplayName
				in.DepartmentID = identity.DepartmentID
				in.DepartmentName = identity.DepartmentName
				in.PositionName = identity.PositionTitle
				in.BaseName = identity.BaseMachineTitle
				in.SourceRefs = append(in.SourceRefs, "employee://"+identity.EmployeeID)
			}
		case EmployeeAuthorityGap:
			if in.FreshnessState == liveactivity.FreshnessFresh {
				in.FreshnessState = liveactivity.FreshnessConflict
			}
		}
	}

	if len(activities) > 0 {
		in.RecentEvents = RecentEventsFromActivities(activities, 5)
		if len(in.RecentEvents) > 0 {
			in.ActivityKind = in.RecentEvents[0].Kind
			in.ActivityNotes = in.RecentEvents[0].SafeSummary
		}
	}

	in.Derivation = der
	return liveactivity.BuildDTO(in, now)
}
