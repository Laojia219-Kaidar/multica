package workwall

import (
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
// workspace. Read-only; sources: agent, agent_runtime, agent_task_queue, plus
// the execution-chain reads (workspace, issue, project, runtime_profile,
// execution_receipt) that hydrate the card's Project/Issue/Run/Receipt/Profile
// identifiers. Every chain lookup stays workspace-scoped.
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

	chainReads := chainStoreFor(s.Q)
	issuePrefix, err := resolveIssuePrefix(ctx, chainReads, workspaceID)
	if err != nil {
		return nil, err
	}

	rtByID := make(map[string]*db.AgentRuntime, len(runtimes))
	for i := range runtimes {
		rtByID[uuidStr(runtimes[i].ID)] = &runtimes[i]
	}

	activeByAgent := make(map[string]*db.AgentTaskQueue)
	outcomeByAgent := make(map[string]*db.AgentTaskQueue)
	for i := range tasks {
		t := &tasks[i]
		aid := uuidStr(t.AgentID)
		if isActiveTaskStatus(t.Status) {
			activeByAgent[aid] = t
		} else {
			outcomeByAgent[aid] = t
		}
	}

	activityByAgent := make(map[string][]db.ActivityLog)
	for aid, t := range activeByAgent {
		if !t.IssueID.Valid {
			continue
		}
		acts, err := s.Q.ListActivitiesForIssue(ctx, db.ListActivitiesForIssueParams{
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
		rt := rtByID[uuidStr(a.RuntimeID)]

		// Hydrate the chain for the task the card shows: the active task
		// when working/queued, otherwise the most recent terminal task.
		shown := activeByAgent[aid]
		if shown == nil {
			shown = outcomeByAgent[aid]
		}
		chain, err := resolveExecutionChain(ctx, chainReads, workspaceID, issuePrefix, rt, shown)
		if err != nil {
			return nil, err
		}

		out = append(out, AssembleAgent(
			a,
			rt,
			activeByAgent[aid],
			outcomeByAgent[aid],
			chain,
			activityByAgent[aid],
			now,
			s.threshold(),
		))
	}
	return out, nil
}
