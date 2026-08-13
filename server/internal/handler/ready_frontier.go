package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/readyfrontier"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ready_frontier.go — read-only "ready frontier" queue sensor (HIV-404).
//
// This file implements GET /api/issues/{id}/frontier: a read-only classification
// of one issue node — and each of its task nodes — into ready / running /
// waiting / blocked / superseded. It composes existing canonical data only
// (issue + parent/child + agent/runtime + agent_task_queue) and writes nothing:
// no new column, no second status table, no truth table (contract: "no new
// truth table").
//
// The classification itself lives in the leaf package internal/readyfrontier so
// service and handler share one decision and can never drift. This handler only
// resolves the canonical evidence (prerequisites, health, capacity, lease,
// lineage) and feeds it to the classifier.
//
// Workspace scoping is exact: loadIssueForUser resolves the issue within the
// caller's workspace, so a cross-workspace issue UUID returns 404 (never an
// enumeration oracle), mirroring GetIssue / GetProjectPipeline.

// FrontierTaskResponse is the per-task classification payload.
type FrontierTaskResponse struct {
	TaskID          string   `json:"task_id"`
	Status          string   `json:"status"`
	FrontierState   string   `json:"frontier_state"`
	FrontierReasons []string `json:"frontier_reasons,omitempty"`
}

// IssueFrontierResponse is the wire shape for GET /api/issues/{id}/frontier.
type IssueFrontierResponse struct {
	IssueID         string                 `json:"issue_id"`
	Status          string                 `json:"status"`
	ReviewState     string                 `json:"review_state,omitempty"`
	FrontierState   string                 `json:"frontier_state"`
	FrontierReasons []string               `json:"frontier_reasons,omitempty"`
	LatestTaskID    string                 `json:"latest_task_id,omitempty"`
	Tasks           []FrontierTaskResponse `json:"tasks,omitempty"`
}

// GetIssueFrontier returns the ready-frontier classification for one issue and
// its tasks.
//
// Route: GET /api/issues/{id}/frontier
// Auth: workspace member (read access).
// Cache: private, no-store — the classification composes live task + lease
// state and must not be cached across users or served stale.
func (h *Handler) GetIssueFrontier(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Cache-Control", "private, no-store")

	var (
		issue                                  db.Issue
		prerequisiteBlocked, prerequisiteUnmet bool
		hasAssignee, agentArchived             bool
		runtimeBound, runtimeOnline            bool
		capacityKnown, capacityFree            bool
		tasks                                  []db.AgentTaskQueue
	)

	if ev := frontierEvidenceFromContext(ctx); ev != nil {
		// Hermetic whole-frontier fixture path (HIVECREW_DB_FREE_FRONTIER):
		// classify an immutable evidence snapshot directly. No
		// loadIssueForUser, resolveFrontierPrerequisites,
		// resolveFrontierHealth or ListTasksByIssue call runs —
		// GetIssueFrontier touches no DB, auth, runtime or task-store state
		// here. Capacity is the only signal derived from a (simulated)
		// query so the three fixture scenarios vary just CountRunningTasks;
		// every other signal is simulated as success. The single canonical
		// ClassifyIssue/ClassifyTask path below is shared with production.
		issue = ev.issue
		prerequisiteBlocked, prerequisiteUnmet = ev.prerequisiteBlocked, ev.prerequisiteUnmet
		hasAssignee, agentArchived = ev.hasAssignee, ev.agentArchived
		runtimeBound, runtimeOnline = ev.runtimeBound, ev.runtimeOnline
		running, countErr := ev.countFn(ctx, pgtype.UUID{})
		capacityKnown = countErr == nil
		capacityFree = capacityKnown && running < int64(ev.agentMaxConcurrent)
		tasks = ev.tasks
	} else {
		// Canonical production path (unchanged): workspace-scoped load,
		// prerequisite/health resolution, and the live task list. Absent the
		// evidence override this branch runs exactly as before — auth, load,
		// prerequisite, health and ListTasks paths are not weakened.
		issueID := chi.URLParam(r, "id")
		loaded, ok := h.loadIssueForUser(w, r, issueID)
		if !ok {
			return
		}
		issue = loaded
		prerequisiteBlocked, prerequisiteUnmet = h.resolveFrontierPrerequisites(ctx, issue)
		hasAssignee, agentArchived, runtimeBound, runtimeOnline, capacityKnown, capacityFree = h.resolveFrontierHealth(ctx, issue)
		var err error
		tasks, err = h.Queries.ListTasksByIssue(ctx, issue.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load tasks")
			return
		}
	}

	// ListTasksByIssue is ordered created_at DESC, so tasks[0] is the latest.
	var (
		hasTask             bool
		taskStatus          string
		prepareLeaseExpired bool
		latestTaskID        string
	)
	if len(tasks) > 0 {
		latest := tasks[0]
		hasTask = true
		taskStatus = latest.Status
		latestTaskID = uuidToString(latest.ID)
		prepareLeaseExpired = prepareLeaseExpiredAt(latest.PrepareLeaseExpiresAt, time.Now())
	}

	reviewState := ""
	if issue.ReviewState.Valid {
		reviewState = issue.ReviewState.String
	}

	cls := readyfrontier.ClassifyIssue(readyfrontier.IssueInput{
		Status:              issue.Status,
		ReviewState:         reviewState,
		PrerequisiteBlocked: prerequisiteBlocked,
		PrerequisiteUnmet:   prerequisiteUnmet,
		HasAssignee:         hasAssignee,
		AgentArchived:       agentArchived,
		RuntimeBound:        runtimeBound,
		RuntimeOnline:       runtimeOnline,
		CapacityKnown:       capacityKnown,
		CapacityFree:        capacityFree,
		HasTask:             hasTask,
		TaskStatus:          taskStatus,
		PrepareLeaseExpired: prepareLeaseExpired,
	})

	resp := IssueFrontierResponse{
		IssueID:         uuidToString(issue.ID),
		Status:          issue.Status,
		ReviewState:     reviewState,
		FrontierState:   string(cls.State),
		FrontierReasons: reasonsToStrings(cls.Reasons),
		LatestTaskID:    latestTaskID,
	}

	// Task-level classification, with successor lineage: a task is superseded
	// when a newer task in the same lineage (retry_of_task_id / rerun_of_task_id
	// / escalation_for_task_id) points at it.
	supersededBy := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		for _, ref := range []pgtype.UUID{t.RetryOfTaskID, t.RerunOfTaskID, t.EscalationForTaskID} {
			if ref.Valid {
				supersededBy[uuidToString(ref)] = true
			}
		}
	}
	for _, t := range tasks {
		tcls := readyfrontier.ClassifyTask(readyfrontier.TaskInput{
			Status:              t.Status,
			SupersededByNewer:   supersededBy[uuidToString(t.ID)],
			PrepareLeaseExpired: prepareLeaseExpiredAt(t.PrepareLeaseExpiresAt, time.Now()),
		})
		resp.Tasks = append(resp.Tasks, FrontierTaskResponse{
			TaskID:          uuidToString(t.ID),
			Status:          t.Status,
			FrontierState:   string(tcls.State),
			FrontierReasons: reasonsToStrings(tcls.Reasons),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// resolveFrontierPrerequisites resolves the prerequisite signals from canonical
// parent + sibling data:
//   - a blocked parent blocks the child;
//   - a parked (backlog) parent holds the child;
//   - a staged child that is not the current (lowest unfinished) stage waits.
func (h *Handler) resolveFrontierPrerequisites(ctx context.Context, issue db.Issue) (blocked, unmet bool) {
	if !issue.ParentIssueID.Valid {
		return false, false
	}
	parent, err := h.Queries.GetIssue(ctx, issue.ParentIssueID)
	if err == nil {
		switch parent.Status {
		case "blocked":
			blocked = true
		case "backlog":
			unmet = true
		}
	}
	children, err := h.Queries.ListChildIssues(ctx, issue.ParentIssueID)
	if err == nil && !issueAtStageFrontier(children, issue) {
		unmet = true
	}
	return blocked, unmet
}

// frontierCountFn is the signature for the capacity-query simulation used by
// the hermetic whole-frontier evidence seam.
type frontierCountFn func(ctx context.Context, agentID pgtype.UUID) (int64, error)

// frontierEvidenceKey carries an immutable whole-frontier evidence snapshot
// for hermetic handler-response fixtures. When a request's context carries one
// (test-only), GetIssueFrontier classifies the snapshot directly and skips
// every DB-touching resolution path — loadIssueForUser,
// resolveFrontierPrerequisites, resolveFrontierHealth and ListTasksByIssue —
// while still running the single canonical ClassifyIssue/ClassifyTask path.
// Production requests never carry this key, so the canonical
// auth/load/prerequisite/health/ListTasks paths are unchanged. The seam is
// per-request and non-global: nothing is stored on the Handler or in package
// state, and absent the key the production path is exactly as before.
type frontierEvidenceKey struct{}

// frontierEvidence is the immutable classifier-input snapshot. Capacity is
// derived from countFn at classification time, so the three fixture scenarios
// vary only that one signal (error / full / free); access, prerequisites,
// health and tasks are simulated as success.
type frontierEvidence struct {
	issue               db.Issue
	prerequisiteBlocked bool
	prerequisiteUnmet   bool
	hasAssignee         bool
	agentArchived       bool
	runtimeBound        bool
	runtimeOnline       bool
	agentMaxConcurrent  int32
	countFn             frontierCountFn
	tasks               []db.AgentTaskQueue
}

func frontierEvidenceFromContext(ctx context.Context) *frontierEvidence {
	if v, ok := ctx.Value(frontierEvidenceKey{}).(*frontierEvidence); ok {
		return v
	}
	return nil
}

// resolveFrontierHealth resolves the health + capacity signals from canonical
// agent/runtime (or squad-leader) data. Missing assignee / agent / squad rows
// fail closed to hasAssignee=false so the classifier never marks the node ready.
func (h *Handler) resolveFrontierHealth(ctx context.Context, issue db.Issue) (hasAssignee, agentArchived, runtimeBound, runtimeOnline, capacityKnown, capacityFree bool) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return false, false, false, false, false, false
	}
	var agent db.Agent
	var err error
	switch issue.AssigneeType.String {
	case "agent":
		agent, err = h.Queries.GetAgent(ctx, issue.AssigneeID)
	case "squad":
		var squad db.Squad
		squad, err = h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return false, false, false, false, false, false
		}
		agent, err = h.Queries.GetAgent(ctx, squad.LeaderID)
	default:
		// member or unrecognized assignee type: not a runnable agent.
		return false, false, false, false, false, false
	}
	if err != nil {
		return false, false, false, false, false, false
	}
	running, countErr := h.Queries.CountRunningTasks(ctx, agent.ID)
	capacityKnown = countErr == nil
	capacityFree = capacityKnown && running < int64(agent.MaxConcurrentTasks)
	return true, agent.ArchivedAt.Valid, agent.RuntimeID.Valid, h.runtimeOnline(ctx, agent.RuntimeID), capacityKnown, capacityFree
}

// runtimeOnline reports whether the bound runtime's status is 'online'. A
// missing runtime row or lookup failure fails closed to false.
func (h *Handler) runtimeOnline(ctx context.Context, runtimeID pgtype.UUID) bool {
	if !runtimeID.Valid {
		return false
	}
	rt, err := h.Queries.GetAgentRuntime(ctx, runtimeID)
	if err != nil {
		return false
	}
	return rt.Status == "online"
}

// issueAtStageFrontier reports whether an issue is in the current (lowest
// unfinished) stage of its sibling set, i.e. whether the stage barrier is open
// to it. An unstaged sibling set is a single implicit stage and every child is
// frontier; an unstaged child in a staged set carries no stage barrier and is
// also frontier (matches migration 123's NULL-stage exclusion).
func issueAtStageFrontier(children []db.Issue, issue db.Issue) bool {
	staged := false
	for _, c := range children {
		if c.Stage.Valid {
			staged = true
			break
		}
	}
	if !staged || !issue.Stage.Valid {
		return true
	}
	lowest := int32(-1)
	for _, c := range children {
		if !c.Stage.Valid || isTerminalChildStatus(c.Status) {
			continue
		}
		if lowest == -1 || c.Stage.Int32 < lowest {
			lowest = c.Stage.Int32
		}
	}
	if lowest == -1 {
		return true
	}
	return issue.Stage.Int32 == lowest
}

// prepareLeaseExpiredAt reports whether a task's prepare lease expired before
// the given time. A NULL lease is not expired.
func prepareLeaseExpiredAt(expires pgtype.Timestamptz, now time.Time) bool {
	return expires.Valid && expires.Time.Before(now)
}

// reasonsToStrings converts classifier reasons to their stable wire strings.
func reasonsToStrings(reasons []readyfrontier.Reason) []string {
	if len(reasons) == 0 {
		return nil
	}
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = string(r)
	}
	return out
}
