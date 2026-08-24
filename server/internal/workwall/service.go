package workwall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workentry"
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

// A2 Work Wall projection slice: read-only panes from the canonical
// work_event ledger joined with Issue / Task / Assignment / Run / Receipt
// read models. No handler/router wiring in this slice; callers compose
// Service.A2Snapshot directly.

const (
	// a2DefaultEventLimit is the ledger window size when the caller passes a
	// non-positive limit.
	a2DefaultEventLimit = 200
	// a2MaxEventLimit caps the ledger window so one snapshot stays bounded.
	a2MaxEventLimit = 1000
	// a2MaxPanes caps how many distinct work_refs project per snapshot.
	a2MaxPanes = 50
)

// A2Snapshot projects one Work Wall pane per work_ref found in the newest
// window of the canonical work_event ledger. It is strictly read-only: the
// ledger stays append-only, and every pane is anchored on a stable
// source_event_id with dispatch-to-employee ownership resolved from the
// assignment dispatch receipt (task assignee as fallback). Panes whose
// session has no matching terminal_presence heartbeat surface as
// event_console — API-only routes never get a faked terminal.
func (s *Service) A2Snapshot(ctx context.Context, workspaceID pgtype.UUID, eventLimit int32) ([]A2PaneV1, error) {
	if eventLimit <= 0 {
		eventLimit = a2DefaultEventLimit
	}
	if eventLimit > a2MaxEventLimit {
		eventLimit = a2MaxEventLimit
	}
	events, err := s.Q.ListRecentWorkEvents(ctx, db.ListRecentWorkEventsParams{
		WorkspaceID: workspaceID,
		Limit:       eventLimit,
	})
	if err != nil {
		return nil, err
	}
	presenceRows, err := s.Q.ListFreshTerminalPresence(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	// The query orders heartbeat_at DESC, so the first row per session name
	// is the freshest heartbeat for that session.
	presenceBySession := make(map[string]db.TerminalPresence, len(presenceRows))
	for i := range presenceRows {
		p := presenceRows[i]
		if _, ok := presenceBySession[p.SessionName]; !ok {
			presenceBySession[p.SessionName] = p
		}
	}

	byRef := make(map[string][]db.WorkEvent, len(events))
	for i := range events {
		ref := events[i].WorkRef
		byRef[ref] = append(byRef[ref], events[i])
	}

	// Deterministic work_ref order: newest anchor event first, then ref.
	refs := make([]string, 0, len(byRef))
	anchorOf := make(map[string]db.WorkEvent, len(byRef))
	for ref, evs := range byRef {
		anchor, _ := a2AnchorEvent(evs)
		if anchor == nil {
			continue
		}
		refs = append(refs, ref)
		anchorOf[ref] = *anchor
	}
	sort.Slice(refs, func(i, j int) bool {
		c := compareA2Observation(anchorOf[refs[i]], anchorOf[refs[j]])
		if c != 0 {
			return c > 0
		}
		return refs[i] < refs[j]
	})
	if len(refs) > a2MaxPanes {
		refs = refs[:a2MaxPanes]
	}

	panes := make([]A2PaneV1, 0, len(refs))
	for _, ref := range refs {
		evs := byRef[ref]
		anchor, _ := a2AnchorEvent(evs)
		refWorkspace, refProject, _, _ := workentry.ParseWorkRef(ref)
		in := A2PaneInput{
			WorkRef:            ref,
			Events:             evs,
			Now:                s.now(),
			StaleThreshold:     s.threshold(),
			RequestWorkspaceID: uuidStr(workspaceID),
			RefWorkspaceID:     refWorkspace,
			RefProjectID:       refProject,
		}
		if err := s.attachA2Inputs(ctx, workspaceID, anchor, presenceBySession, &in); err != nil {
			return nil, err
		}
		if pane, ok := ProjectA2Pane(in); ok {
			panes = append(panes, pane)
		}
	}
	SortA2Panes(panes)
	return panes, nil
}

// attachA2Inputs resolves the Issue / Task / Assignment / Receipt / Agent
// read models for one work_ref. A missing row (pgx.ErrNoRows) is an absent
// optional input and the projection fails closed around it; any other error
// aborts the snapshot.
// attachA2Inputs resolves the Issue / Task / Assignment / Receipt / Agent
// read models for one work_ref. All reads are tenant-scoped and every
// cross-model claim is verified against this workspace/task/issue before it
// is attached; a mismatched row is dropped, never trusted. A missing row
// (pgx.ErrNoRows) is an absent optional input and the projection fails
// closed around it; any other error aborts the snapshot.
func (s *Service) attachA2Inputs(ctx context.Context, workspaceID pgtype.UUID, anchor *db.WorkEvent, presenceBySession map[string]db.TerminalPresence, in *A2PaneInput) error {
	_, _, issueIDStr, taskIDStr := workentry.ParseWorkRef(in.WorkRef)

	var issueID, taskID pgtype.UUID
	if issueIDStr != "" {
		id, err := util.ParseUUID(issueIDStr)
		if err != nil {
			return fmt.Errorf("a2 work_ref %q: %w", in.WorkRef, err)
		}
		issueID = id
	}
	if taskIDStr != "" {
		id, err := util.ParseUUID(taskIDStr)
		if err != nil {
			return fmt.Errorf("a2 work_ref %q: %w", in.WorkRef, err)
		}
		taskID = id
	}

	if issueID.Valid {
		issue, err := s.Q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          issueID,
			WorkspaceID: workspaceID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			in.Issue = &issue
		}
	}

	// B3-2/B3-3: the unscoped GetExecutionReceipt read (keyed by task_id
	// only) is gated behind BOTH a successful workspace-scoped task read AND
	// proof that the task belongs to this work_ref's issue. A foreign or
	// cross-issue task id therefore never triggers a receipt read at all.
	var task *db.AgentTaskQueue
	if taskID.Valid {
		// B2: tenant-scoped task read. GetAgentTaskInWorkspace only returns
		// the task when its owning agent lives in this workspace, so a task
		// id from another tenant can never cross-read here.
		t, err := s.Q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
			ID:          taskID,
			WorkspaceID: workspaceID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// B3-3: the task must reference exactly the work_ref's issue; a
		// cross-issue task is dropped before it can influence anything.
		if err == nil && issueID.Valid && t.IssueID.Valid &&
			uuidStr(t.IssueID) == uuidStr(issueID) {
			task = &t
			in.Task = task
		}
	}

	if task != nil {
		receipt, err := s.Q.GetExecutionReceipt(ctx, taskID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			// B2: the receipt is keyed by task_id only, so it is attached
			// only after explicitly proving it belongs to this workspace,
			// task, and (when known) issue. A foreign row is dropped.
			if uuidStr(receipt.WorkspaceID) == uuidStr(workspaceID) &&
				uuidStr(receipt.TaskID) == uuidStr(taskID) &&
				(!issueID.Valid || uuidStr(receipt.IssueID) == uuidStr(issueID)) {
				in.Receipt = &receipt
			}
		}

		// B2/B3-4: assignment ownership is recovered ONLY through this
		// task's receipt assignment_command_id with a workspace-scoped
		// lookup, and only when the recovered dispatch is precisely bound to
		// THIS task (and issue); the pure projection additionally requires
		// the dispatch command to equal the canonical receipt's command.
		// GetLatestAssignmentDispatchReceiptByIssue is deliberately never
		// used: a later re-dispatch of the issue must not re-attribute this
		// pane's work to a different employee.
		if in.Receipt != nil && in.Receipt.AssignmentCommandID.Valid {
			dispatch, err := s.Q.GetAssignmentDispatchReceipt(ctx, db.GetAssignmentDispatchReceiptParams{
				WorkspaceID: workspaceID,
				CommandID:   in.Receipt.AssignmentCommandID,
			})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil &&
				uuidStr(dispatch.InitialTaskID) == uuidStr(taskID) &&
				(!issueID.Valid || uuidStr(dispatch.IssueID) == uuidStr(issueID)) {
				in.Dispatch = &dispatch
			}
		}
	}

	// Employee identity: the task-bound dispatch wins; otherwise the task
	// assignee (already tenant-verified by GetAgentTaskInWorkspace). The
	// latest issue dispatch is never consulted.
	var agentID pgtype.UUID
	if bound := a2BoundDispatch(*in); bound != nil {
		agentID = bound.LocalAgentID
	}
	if !agentID.Valid && in.Task != nil {
		agentID = in.Task.AgentID
	}
	if agentID.Valid {
		agent, err := s.Q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          agentID,
			WorkspaceID: workspaceID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			in.Agent = &agent
		}
	}

	if anchor != nil && anchor.SessionID.Valid {
		if p, ok := presenceBySession[anchor.SessionID.String]; ok {
			in.Presence = &p
		}
	}
	return nil
}
