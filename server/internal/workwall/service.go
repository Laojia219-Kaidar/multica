package workwall

import (
	"bytes"
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Service assembles the workspace-wide work-wall snapshot from existing
// HiveCrew read models. It performs no writes and adds no schema.
type Service struct {
	Q              *db.Queries
	Now            func() time.Time
	StaleThreshold time.Duration
}

func NewService(q *db.Queries) *Service {
	return &Service{Q: q, Now: time.Now, StaleThreshold: defaultStaleThreshold}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) threshold() time.Duration {
	if s.StaleThreshold <= 0 {
		return defaultStaleThreshold
	}
	return s.StaleThreshold
}

// Snapshot returns one EmployeeLiveActivityV1 per user-authored agent in the
// workspace. Read-only; sources: agent, agent_runtime, agent_task_queue.
func (s *Service) Snapshot(ctx context.Context, workspaceID pgtype.UUID) ([]liveactivity.EmployeeLiveActivityV1, error) {
	agents, err := s.Q.ListAgents(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	runtimes, err := s.Q.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	tasks, err := s.Q.ListWorkspaceAgentTaskSnapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	rtByID := make(map[string]*db.AgentRuntime, len(runtimes))
	for i := range runtimes {
		rtByID[uuidStr(runtimes[i].ID)] = &runtimes[i]
	}

	activeByAgent, outcomeByAgent := partitionWorkspaceTasks(tasks)

	activityByAgent := make(map[string][]db.ActivityLog)
	for aid, t := range activeByAgent {
		if !t.IssueID.Valid {
			continue
		}
		acts, err := s.Q.ListRecentActivitiesForIssue(ctx, db.ListRecentActivitiesForIssueParams{
			IssueID: t.IssueID,
			Limit:   5,
		})
		if err != nil {
			return nil, err
		}
		activityByAgent[aid] = acts
	}

	now := s.now()
	out := make([]liveactivity.EmployeeLiveActivityV1, 0, len(agents))
	for i := range agents {
		a := agents[i]
		aid := uuidStr(a.ID)
		out = append(out, AssembleAgent(
			a,
			rtByID[uuidStr(a.RuntimeID)],
			activeByAgent[aid],
			outcomeByAgent[aid],
			activityByAgent[aid],
			now,
			s.threshold(),
		))
	}
	return out, nil
}

// activeTaskStatusRank orders an agent's concurrent active tasks by how far
// the task has progressed: running is the live truth, waiting_local_directory
// is executing but blocked, dispatched was handed to a runtime, queued has not
// started. Unknown statuses rank lowest so a stray status value can never
// outrank a known one.
func activeTaskStatusRank(status string) int {
	switch status {
	case "running":
		return 4
	case "waiting_local_directory":
		return 3
	case "dispatched":
		return 2
	case "queued":
		return 1
	default:
		return 0
	}
}

// compareTimestamptzDescNullsLast orders two optional timestamps under DESC
// NULLS LAST semantics: the later time wins, and a NULL (invalid) timestamp
// loses to any present one. Returns >0 if a wins, <0 if b wins, 0 on a tie.
func compareTimestamptzDescNullsLast(a, b pgtype.Timestamptz) int {
	if !a.Valid && !b.Valid {
		return 0
	}
	if !a.Valid {
		return -1
	}
	if !b.Valid {
		return 1
	}
	return a.Time.Compare(b.Time)
}

// compareUUIDDesc orders two UUIDs descending (byte-wise, matching the uuid
// type ordering in Postgres). Returns >0 if a wins, <0 if b wins, 0 when equal.
func compareUUIDDesc(a, b pgtype.UUID) int {
	if a.Valid != b.Valid {
		if a.Valid {
			return 1
		}
		return -1
	}
	return bytes.Compare(a.Bytes[:], b.Bytes[:])
}

// compareActiveTaskPreference returns >0 if a is preferred over b as an
// agent's Work Wall presence, <0 if b is preferred, and 0 only when both are
// the same row. Preference order: status rank (running >
// waiting_local_directory > dispatched > queued), then started_at DESC NULLS
// LAST, dispatched_at DESC NULLS LAST, created_at DESC (NOT NULL in schema; an
// invalid timestamp is treated as oldest), id DESC — so the pick is fully
// deterministic regardless of SQL result order.
func compareActiveTaskPreference(a, b *db.AgentTaskQueue) int {
	if ra, rb := activeTaskStatusRank(a.Status), activeTaskStatusRank(b.Status); ra != rb {
		return ra - rb
	}
	if c := compareTimestamptzDescNullsLast(a.StartedAt, b.StartedAt); c != 0 {
		return c
	}
	if c := compareTimestamptzDescNullsLast(a.DispatchedAt, b.DispatchedAt); c != 0 {
		return c
	}
	if c := compareTimestamptzDescNullsLast(a.CreatedAt, b.CreatedAt); c != 0 {
		return c
	}
	return compareUUIDDesc(a.ID, b.ID)
}

// selectPreferredActiveTask deterministically picks which of two concurrent
// active tasks represents an agent's work-wall presence. ListWorkspaceAgentTaskSnapshot
// returns every active row per agent with no guaranteed order, so folding this
// pairwise preference keeps the wall stable no matter how Postgres orders or
// re-orders the result set.
func selectPreferredActiveTask(current, candidate *db.AgentTaskQueue) *db.AgentTaskQueue {
	if current == nil {
		return candidate
	}
	if candidate == nil {
		return current
	}
	if compareActiveTaskPreference(candidate, current) > 0 {
		return candidate
	}
	return current
}

// partitionWorkspaceTasks splits one ListWorkspaceAgentTaskSnapshot result
// into each agent's preferred active task and its last outcome. Outcome
// routing is unchanged from before R2: completed/failed rows occupy the
// outcome slot (the query already returns at most one per agent and never
// returns cancelled rows, so a cancel cannot mask a prior outcome there).
// An active task with a NULL issue_id stays eligible here; Snapshot skips only
// its Issue activity lookup.
func partitionWorkspaceTasks(tasks []db.AgentTaskQueue) (activeByAgent, outcomeByAgent map[string]*db.AgentTaskQueue) {
	activeByAgent = make(map[string]*db.AgentTaskQueue)
	outcomeByAgent = make(map[string]*db.AgentTaskQueue)
	for i := range tasks {
		t := &tasks[i]
		aid := uuidStr(t.AgentID)
		if isActiveTaskStatus(t.Status) {
			activeByAgent[aid] = selectPreferredActiveTask(activeByAgent[aid], t)
		} else {
			outcomeByAgent[aid] = t
		}
	}
	return activeByAgent, outcomeByAgent
}
