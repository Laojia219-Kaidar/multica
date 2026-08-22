// Package workwall assembles the W4 "工作现场" (work wall) snapshot from the
// HiveCrew execution projection (agent / agent_runtime / agent_task_queue).
// It is a read projection: it never writes state and never becomes a second
// source of truth.
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

// AssembleAgent maps one agent + its runtime + its active task / last outcome
// into a sanitized EmployeeLiveActivityV1. It is a pure function (no I/O) so
// the derivation is unit-testable without a database.
//
// chain carries the hydrated Project/Issue/Run/Receipt/Profile evidence for
// the task currently shown (active, or the recent terminal task when idle);
// it may be nil (no task / no evidence) and never overrides task-owned
// identifiers.
//
// Employee identity note: in HiveCrew the "digital employee" is the Agent row;
// the formal HiveCosm Employee<->Agent binding is owned by P4 / HiveCosm and is
// intentionally not synthesized here (employee_id mirrors agent_id for v0).
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

	in := liveactivity.SnapshotInput{
		WorkspaceID: uuidStr(agent.WorkspaceID),
		EmployeeID:  agentID,
		AgentID:     agentID,
		DisplayName: agent.Name,
		AvatarURL:   textStr(agent.AvatarUrl),
		ModelName:   textStr(agent.Model),
		RuntimeID:   runtimeID,
		SourceRefs:  []string{"agent://" + agentID},
	}

	if rt != nil {
		in.RuntimeProvider = rt.Provider
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
