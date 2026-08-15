package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProjectAutoStartService implements the bounded Project start/continue
// control slice (HIV-405). It composes the existing OwnerDispatchService
// (per-issue idempotent dispatch) with a dependency-ready wave query
// (ListProjectReadyIssues) — no second scheduler, no auto-deploy.
//
// Two commands:
//   - Preview: read-only, returns which issues are ready and why others aren't.
//   - Start:   idempotent batch dispatch over the ready wave. Each issue is
//     dispatched through OwnerDispatchService.Dispatch with a derived
//     idempotency key so repeat clicks replay safely.
//
// Truthfulness contract (HIV-465 / HIV-473 repair): both Preview and Start
// apply the SAME fail-closed readiness classification on top of the wave SQL.
// An issue is reported ready only when every gate is positively satisfied:
//   - the parent prerequisite gate is met (prereq_met from the wave SQL) —
//     rows that fail it stay VISIBLE as blocked (missing_prerequisite),
//     never silently dropped from the SQL result set;
//   - status is not terminal/blocked;
//   - an assignee is present;
//   - the assignee is not archived;
//   - the runtime is bound AND its row resolves (runtime_unbound /
//     runtime_missing / runtime_lookup_failed are distinct from
//     runtime_offline — Agent.status / registry-online alone are NOT treated
//     as executable health);
//   - the agent has capacity (maxConcurrent<=0 is capacity full, matching the
//     canonical claim path's running >= max_concurrent_tasks comparison);
//
// Any lookup error fails closed with a concrete reason rather than passing
// the issue as ready. The single transaction + winner replay of the dispatch
// success path is unchanged.
type ProjectAutoStartService struct {
	Queries  *db.Queries
	Dispatch *OwnerDispatchService
}

// NewProjectAutoStartService constructs a ProjectAutoStartService.
func NewProjectAutoStartService(q *db.Queries, dispatch *OwnerDispatchService) *ProjectAutoStartService {
	return &ProjectAutoStartService{Queries: q, Dispatch: dispatch}
}

// AutoStartBlockReason is a stable, machine-readable code explaining why an
// issue in the ready wave was classified as not ready. It deliberately does
// not reuse the per-dispatch DispatchBlockReason vocabulary because the wave
// classification adds gates the single-issue dispatch preview does not
// (prerequisite, capacity, blocked status).
type AutoStartBlockReason string

const (
	AutoStartBlockNone              AutoStartBlockReason = ""
	AutoStartBlockTerminalStatus    AutoStartBlockReason = "terminal_status"
	AutoStartBlockBlockedStatus     AutoStartBlockReason = "blocked_status"
	AutoStartBlockNoAssignee        AutoStartBlockReason = "no_assignee"
	AutoStartBlockAgentArchived     AutoStartBlockReason = "agent_archived"
	AutoStartBlockMissingPrereq     AutoStartBlockReason = "missing_prerequisite"
	AutoStartBlockRuntimeUnbound    AutoStartBlockReason = "runtime_unbound"
	AutoStartBlockRuntimeMissing    AutoStartBlockReason = "runtime_missing"
	AutoStartBlockRuntimeOffline    AutoStartBlockReason = "runtime_offline"
	AutoStartBlockRuntimeLookupErr  AutoStartBlockReason = "runtime_lookup_failed"
	AutoStartBlockCapacityFull      AutoStartBlockReason = "capacity_full"
	AutoStartBlockLookupFailed      AutoStartBlockReason = "issue_lookup_failed"
	AutoStartBlockDuplicateInBatch  AutoStartBlockReason = "duplicate_in_batch"
	AutoStartBlockNotInWave         AutoStartBlockReason = "not_in_ready_wave"
	AutoStartBlockActiveTaskLookup  AutoStartBlockReason = "active_task_lookup_failed"
	AutoStartBlockTaskRowMismatch   AutoStartBlockReason = "task_row_mismatch"
	AutoStartBlockReplayEmpty       AutoStartBlockReason = "replay_empty_receipt"
	AutoStartBlockIdempotencyLookup AutoStartBlockReason = "idempotency_lookup_failed"
)

// ReadyIssue is one entry in the dependency-ready wave.
type ReadyIssue struct {
	IssueID        string `json:"issue_id"`
	Status         string `json:"status"`
	Priority       string `json:"priority"`
	Title          string `json:"title"`
	AssigneeType   string `json:"assignee_type"`
	AssigneeID     string `json:"assignee_id"`
	HasActiveTask  bool   `json:"has_active_task"`
	ExistingTaskID string `json:"existing_task_id,omitempty"`
}

// BlockedIssue is one entry that is not ready, with a reason.
type BlockedIssue struct {
	IssueID string `json:"issue_id"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Reason  string `json:"reason"`
}

// PreviewResult is the read-only outcome of project start preview.
type ProjectAutoStartPreviewResult struct {
	ProjectID  string         `json:"project_id"`
	ReadyCount int            `json:"ready_count"`
	Ready      []ReadyIssue   `json:"ready"`
	Blocked    []BlockedIssue `json:"blocked,omitempty"`
}

// Preview computes the dependency-ready wave for a project. Every row from the
// wave SQL is re-classified fail-closed: only issues whose parent prerequisite
// gate is met, whose status is not blocked/terminal, and whose assignee is
// concretely runnable (not archived, runtime bound/online, under capacity) are
// reported ready. The rest surface in Blocked with a concrete reason — rows
// that fail the prerequisite gate stay visible, they never vanish from the
// result set into silence.
func (s *ProjectAutoStartService) Preview(ctx context.Context, workspaceID, projectID pgtype.UUID) (*ProjectAutoStartPreviewResult, error) {
	readyRows, err := s.Queries.ListProjectReadyIssues(ctx, db.ListProjectReadyIssuesParams{
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("list ready issues: %w", err)
	}

	result := &ProjectAutoStartPreviewResult{
		ProjectID: util.UUIDToString(projectID),
	}

	activeTaskStatuses := map[string]bool{
		"queued": true, "dispatched": true, "running": true,
	}

	for _, row := range readyRows {
		issueIDStr := util.UUIDToString(row.IssueID)
		title := row.IssueTitle

		// Wave-level gate: the parent prerequisite gate must be satisfied.
		// Rows that fail it surface as blocked instead of disappearing.
		if r := prereqBlockReason(row.PrereqMet); r != AutoStartBlockNone {
			result.Blocked = append(result.Blocked, BlockedIssue{
				IssueID: issueIDStr,
				Status:  row.IssueStatus,
				Title:   title,
				Reason:  string(r),
			})
			continue
		}

		ready, reason := s.assessReadiness(ctx, autostartIssueStub(row))

		if !ready {
			result.Blocked = append(result.Blocked, BlockedIssue{
				IssueID: issueIDStr,
				Status:  row.IssueStatus,
				Title:   title,
				Reason:  string(reason),
			})
			continue
		}

		ri := ReadyIssue{
			IssueID:      issueIDStr,
			Status:       row.IssueStatus,
			Priority:     row.IssuePriority,
			Title:        title,
			AssigneeType: row.IssueAssigneeType.String,
			AssigneeID:   util.UUIDToString(row.IssueAssigneeID),
		}
		if row.TaskID.Valid && activeTaskStatuses[row.TaskStatus] {
			ri.HasActiveTask = true
			ri.ExistingTaskID = util.UUIDToString(row.TaskID)
		}
		result.Ready = append(result.Ready, ri)
	}
	result.ReadyCount = len(result.Ready)

	return result, nil
}

// StartParams carries the inputs for the idempotent batch start.
type ProjectAutoStartParams struct {
	WorkspaceID    pgtype.UUID
	ProjectID      pgtype.UUID
	IdempotencyKey string
	RequestDigest  string
	ActorUserID    pgtype.UUID
	// SelectedIssueIDs limits the dispatch to specific issues. Empty = all ready.
	SelectedIssueIDs []pgtype.UUID
}

// IssueDispatchResult is the per-issue outcome within a batch start.
type IssueDispatchResult struct {
	IssueID  string   `json:"issue_id"`
	Decision string   `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
	TaskIDs  []string `json:"task_ids,omitempty"`
	Replayed bool     `json:"replayed,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// ProjectAutoStartResult is the batch outcome.
type ProjectAutoStartResult struct {
	ProjectID      string                `json:"project_id"`
	IdempotencyKey string                `json:"idempotency_key"`
	Dispatched     int                   `json:"dispatched"`
	AlreadyActive  int                   `json:"already_active"`
	Blocked        int                   `json:"blocked"`
	Results        []IssueDispatchResult `json:"results"`
}

// Start performs the idempotent batch dispatch over the ready wave. Order of
// precedence per candidate issue:
//
//  1. Replay-first: a repeat click of an already-recorded outcome returns the
//     IDENTICAL stored winner receipt regardless of current capacity/runtime/
//     status/ready-wave state. The per-issue key is (batch key, issue id), so
//     replay is position- AND wave-membership-independent: candidates are
//     enumerated from the SELECTION (when provided) rather than only the wave,
//     so an issue that left the ready wave still replays its receipt.
//  2. Explicit duplicate-selection fail-closed (never silently folded).
//  3. Fresh dispatches go through the SAME fail-closed readiness
//     classification as Preview, then a single-tx dispatch, then an exact
//     task-row receipt assertion.
func (s *ProjectAutoStartService) Start(ctx context.Context, p ProjectAutoStartParams) (*ProjectAutoStartResult, error) {
	// 1. Compute the ready wave.
	readyRows, err := s.Queries.ListProjectReadyIssues(ctx, db.ListProjectReadyIssuesParams{
		WorkspaceID: p.WorkspaceID,
		ProjectID:   p.ProjectID,
	})
	if err != nil {
		return nil, fmt.Errorf("list ready issues: %w", err)
	}

	waveByID := make(map[string]db.ListProjectReadyIssuesRow, len(readyRows))
	for _, row := range readyRows {
		waveByID[util.UUIDToString(row.IssueID)] = row
	}

	// 2. Selection: count occurrences. A repeated issue_id in the selection is
	// an EXPLICIT fail-closed signal (HIV-465 item 1): the affected issue is
	// blocked with duplicate_in_batch and never dispatched, never silently
	// collapsed through a Set.
	selectionCounts := make(map[string]int, len(p.SelectedIssueIDs))
	for _, id := range p.SelectedIssueIDs {
		if key := util.UUIDToString(id); key != "" {
			selectionCounts[key]++
		}
	}
	duplicateSelected := autostartDuplicateSelection(p.SelectedIssueIDs)
	selectionProvided := len(selectionCounts) > 0

	// 3. Enumerate candidates. With a selection, iterate the selection (in
	// request order, deduplicated): replay-first must reach an issue even if it
	// is no longer in the ready wave. Without a selection, iterate the wave.
	candidates := make([]string, 0, len(readyRows))
	if selectionProvided {
		seen := make(map[string]bool, len(selectionCounts))
		for _, id := range p.SelectedIssueIDs {
			key := util.UUIDToString(id)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, key)
		}
	} else {
		for _, row := range readyRows {
			candidates = append(candidates, util.UUIDToString(row.IssueID))
		}
	}

	result := &ProjectAutoStartResult{
		ProjectID:      util.UUIDToString(p.ProjectID),
		IdempotencyKey: p.IdempotencyKey,
	}

	handled := make(map[string]bool, len(candidates))

	for _, issueIDStr := range candidates {
		if handled[issueIDStr] {
			continue
		}
		handled[issueIDStr] = true
		row, inWave := waveByID[issueIDStr]

		// 4. Per-issue idempotency key is derived from (batch key, issue id)
		// only — deliberately position-independent so a re-ordered wave still
		// replays the same receipt. The batch key itself carries the digest.
		perIssueKey := autostartPerIssueKey(p.IdempotencyKey, issueIDStr)

		// 5. Replay-first (HIV-465 item 3, HIV-473 item 7): a stored winner
		// receipt wins over EVERYTHING — capacity, runtime, status, wave
		// membership. Only issues without a recorded outcome are gated.
		existing, idemErr := s.Queries.GetDispatchIdempotency(ctx, db.GetDispatchIdempotencyParams{
			WorkspaceID:    p.WorkspaceID,
			IdempotencyKey: perIssueKey,
		})
		if idemErr == nil {
			if existing.RequestDigest != p.RequestDigest {
				// Same per-issue key, different request body → hard conflict.
				result.Results = append(result.Results, IssueDispatchResult{
					IssueID:  issueIDStr,
					Decision: string(DecisionBlocked),
					Reason:   "idempotency_conflict",
				})
				result.Blocked++
				continue
			}
			ids := make([]string, 0, len(existing.TaskIds))
			for _, id := range existing.TaskIds {
				ids = append(ids, util.UUIDToString(id))
			}
			if len(ids) == 0 {
				// A stored receipt pointing at no tasks is corrupt — fail closed.
				result.Results = append(result.Results, IssueDispatchResult{
					IssueID:  issueIDStr,
					Decision: string(DecisionBlocked),
					Reason:   string(AutoStartBlockReplayEmpty),
				})
				result.Blocked++
				continue
			}
			result.Results = append(result.Results, IssueDispatchResult{
				IssueID:  issueIDStr,
				Decision: string(DecisionWouldEnqueue),
				TaskIDs:  ids,
				Replayed: true,
			})
			result.Dispatched++
			continue
		} else if !errors.Is(idemErr, pgx.ErrNoRows) {
			// Idempotency lookup error → fail-closed.
			result.Results = append(result.Results, IssueDispatchResult{
				IssueID:  issueIDStr,
				Decision: string(DecisionBlocked),
				Reason:   string(AutoStartBlockIdempotencyLookup),
				Error:    "idempotency lookup failed: " + idemErr.Error(),
			})
			result.Blocked++
			continue
		}

		// 6. Explicit duplicate-selection fail-closed: the same issue id was
		// sent more than once in THIS request. Never dispatch, never fold.
		if duplicateSelected[issueIDStr] {
			result.Results = append(result.Results, IssueDispatchResult{
				IssueID:  issueIDStr,
				Decision: string(DecisionBlocked),
				Reason:   string(AutoStartBlockDuplicateInBatch),
			})
			result.Blocked++
			continue
		}

		if !inWave {
			// The issue is NOT in the current ready wave: classify it
			// truthfully from the persisted row instead of silently skipping.
			issueUUID, parseErr := util.ParseUUID(issueIDStr)
			if parseErr != nil {
				result.Results = append(result.Results, IssueDispatchResult{
					IssueID:  issueIDStr,
					Decision: string(DecisionBlocked),
					Reason:   string(AutoStartBlockLookupFailed),
					Error:    "invalid issue id: " + parseErr.Error(),
				})
				result.Blocked++
				continue
			}
			issue, getErr := s.Queries.GetIssue(ctx, issueUUID)
			if getErr != nil {
				result.Results = append(result.Results, IssueDispatchResult{
					IssueID:  issueIDStr,
					Decision: string(DecisionBlocked),
					Reason:   string(AutoStartBlockLookupFailed),
					Error:    "issue lookup failed: " + getErr.Error(),
				})
				result.Blocked++
				continue
			}
			ready, reason := s.assessReadiness(ctx, issue)
			if !ready {
				result.Results = append(result.Results, IssueDispatchResult{
					IssueID:  issueIDStr,
					Decision: string(DecisionBlocked),
					Reason:   string(reason),
				})
				result.Blocked++
				continue
			}
			// Ready by readiness but absent from the wave: the wave SQL is the
			// dispatchable enumeration — absent means not dispatchable here.
			result.Results = append(result.Results, IssueDispatchResult{
				IssueID:  issueIDStr,
				Decision: string(DecisionBlocked),
				Reason:   string(AutoStartBlockNotInWave),
			})
			result.Blocked++
			continue
		}

		// 7. Fresh dispatch over a wave row. Prerequisite gate first — the
		// row stays visible as blocked, it never vanishes (HIV-465 item 4).
		if r := prereqBlockReason(row.PrereqMet); r != AutoStartBlockNone {
			result.Results = append(result.Results, IssueDispatchResult{
				IssueID:  issueIDStr,
				Decision: string(DecisionBlocked),
				Reason:   string(r),
			})
			result.Blocked++
			continue
		}

		// 8. Load the full issue (the dispatch service needs a full db.Issue).
		issue, err := s.Queries.GetIssue(ctx, row.IssueID)
		if err != nil {
			result.Results = append(result.Results, IssueDispatchResult{
				IssueID:  issueIDStr,
				Decision: string(DecisionBlocked),
				Reason:   string(AutoStartBlockLookupFailed),
				Error:    "issue lookup failed: " + err.Error(),
			})
			result.Blocked++
			continue
		}

		// 9. Readiness gate — identical to Preview. Do not dispatch issues
		// whose assignee is not concretely runnable.
		ready, reason := s.assessReadiness(ctx, issue)
		if !ready {
			result.Results = append(result.Results, IssueDispatchResult{
				IssueID:  issueIDStr,
				Decision: string(DecisionBlocked),
				Reason:   string(reason),
			})
			result.Blocked++
			continue
		}

		// 10. Active tasks — fail-closed. A lookup error must NOT be swallowed
		// and treated as "no active task" (HIV-465 item 2); dispatching on top
		// of an unknown task state could double-enqueue.
		activeTasks, atErr := s.Queries.ListActiveTasksByIssue(ctx, issue.ID)
		if atErr != nil {
			result.Results = append(result.Results, IssueDispatchResult{
				IssueID:  issueIDStr,
				Decision: string(DecisionBlocked),
				Reason:   string(AutoStartBlockActiveTaskLookup),
				Error:    "active task lookup failed: " + atErr.Error(),
			})
			result.Blocked++
			continue
		}

		// 11. Dispatch through the existing OwnerDispatchService (single tx,
		// winner replay unchanged).
		dr, err := s.Dispatch.Dispatch(ctx, DispatchParams{
			Issue:          issue,
			WorkspaceID:    p.WorkspaceID,
			IdempotencyKey: perIssueKey,
			RequestDigest:  p.RequestDigest,
			ActiveTasks:    activeTasks,
			ActorUserID:    p.ActorUserID,
		})

		idr := IssueDispatchResult{IssueID: issueIDStr}
		if err != nil {
			if errors.Is(err, ErrIdempotencyConflict) {
				idr.Decision = string(DecisionBlocked)
				idr.Reason = "idempotency_conflict"
				result.Blocked++
			} else if errors.Is(err, ErrExpectedStateMismatch) {
				idr.Decision = string(DecisionBlocked)
				idr.Reason = "state_mismatch"
				result.Blocked++
			} else {
				idr.Decision = string(DecisionBlocked)
				idr.Error = err.Error()
				result.Blocked++
			}
			result.Results = append(result.Results, idr)
			continue
		}

		idr.Decision = string(dr.Decision)
		idr.Reason = string(dr.Reason)
		idr.TaskIDs = dr.TaskIDs
		idr.Replayed = dr.Replayed

		// 12. Post-dispatch task-row assertion (HIV-465 item 3, HIV-473 item
		// 2): a fresh would_enqueue must produce EXACTLY the reported task
		// rows — the active task set for the issue must match the receipt row
		// count AND ID set with NO extra tasks. Membership-inclusion alone is
		// not enough. A replay must return the stored (non-empty) receipt.
		if mismatchReason := s.assertDispatchReceipt(ctx, issue.ID, dr); mismatchReason != AutoStartBlockNone {
			idr.Decision = string(DecisionBlocked)
			idr.Reason = string(mismatchReason)
			result.Blocked++
			result.Results = append(result.Results, idr)
			continue
		}

		switch dr.Decision {
		case DecisionWouldEnqueue:
			result.Dispatched++
		case DecisionAlreadyActive:
			result.AlreadyActive++
		default:
			result.Blocked++
		}
		result.Results = append(result.Results, idr)
	}

	slog.Info("project auto-start completed",
		"project_id", result.ProjectID,
		"dispatched", result.Dispatched,
		"already_active", result.AlreadyActive,
		"blocked", result.Blocked,
	)

	return result, nil
}

// autostartRuntimeState is the runtime-health outcome resolved from the
// shared AgentReadiness source of truth. The states deliberately separate
// "no runtime bound" / "runtime row missing" from "runtime exists but not
// online" (HIV-473 item 5): Agent.status / registry-online alone are never
// treated as executable health.
type autostartRuntimeState int

const (
	runtimeStateOK        autostartRuntimeState = iota
	runtimeStateUnbound                         // agent has no runtime bound
	runtimeStateMissing                         // runtime row does not exist (ErrNoRows)
	runtimeStateOffline                         // runtime exists but not online
	runtimeStateLookupErr                       // runtime lookup failed for another reason
)

// assessReadiness is the shared fail-closed classification used by both
// Preview and Start. It resolves the assignee/runtime/capacity state from the
// existing single sources of truth (OwnerDispatchService.resolveTargetAgent,
// AgentReadiness, CountRunningTasks) and delegates the agent-level decision to
// the pure classifyResolvedReadiness. It does not introduce a second readiness
// authority. Any lookup error is fail-closed: the issue is blocked with a
// concrete reason rather than passed as ready.
func (s *ProjectAutoStartService) assessReadiness(ctx context.Context, issue db.Issue) (bool, AutoStartBlockReason) {
	// Status gate (pure). The wave SQL excludes done/cancelled but NOT
	// 'blocked'; an issue in blocked status must not be reported ready.
	if r := statusBlockReason(issue.Status); r != AutoStartBlockNone {
		return false, r
	}
	if issue.AssigneeType.String == "" || !issue.AssigneeID.Valid {
		return false, AutoStartBlockNoAssignee
	}

	// Resolve the runnable agent (agent or squad leader). A resolution
	// failure is fail-closed: we cannot prove the assignee can run.
	agent, err := s.Dispatch.resolveTargetAgent(ctx, issue)
	if err != nil {
		return false, AutoStartBlockNoAssignee
	}

	// Runtime health — reuse the shared AgentReadiness source of truth and
	// resolve the runtime state concretely; a lookup error is fail-closed.
	rtReady, _, rtErr := AgentReadiness(ctx, s.Queries, agent)

	var rtState autostartRuntimeState
	switch {
	case !agent.RuntimeID.Valid:
		rtState = runtimeStateUnbound
	case errors.Is(rtErr, pgx.ErrNoRows):
		rtState = runtimeStateMissing
	case rtErr != nil:
		rtState = runtimeStateLookupErr
	case !rtReady:
		rtState = runtimeStateOffline
	default:
		rtState = runtimeStateOK
	}

	// Capacity gate — reuse the shared CountRunningTasks query. A count error
	// or unresolved runtime fails closed: we cannot prove headroom.
	var running int
	if rtState == runtimeStateOK {
		rc, runErr := s.Queries.CountRunningTasks(ctx, agent.ID)
		if runErr != nil {
			rtState = runtimeStateLookupErr
		} else {
			running = int(rc)
		}
	}

	return classifyResolvedReadiness(
		agent.ArchivedAt.Valid, rtState,
		running, int(agent.MaxConcurrentTasks),
	)
}

// classifyResolvedReadiness is the pure, DB-free agent-level readiness
// decision. It encodes the fail-closed ordering after the status, prerequisite
// and assignee gates have passed and the agent has been resolved: archived →
// runtime (unbound/missing/offline/lookup-err) → capacity full → ready.
// Exhaustively unit-tested without a database.
func classifyResolvedReadiness(
	agentArchived bool,
	rtState autostartRuntimeState,
	running, maxConcurrent int,
) (bool, AutoStartBlockReason) {
	if agentArchived {
		return false, AutoStartBlockAgentArchived
	}
	switch rtState {
	case runtimeStateUnbound:
		return false, AutoStartBlockRuntimeUnbound
	case runtimeStateMissing:
		return false, AutoStartBlockRuntimeMissing
	case runtimeStateOffline:
		return false, AutoStartBlockRuntimeOffline
	case runtimeStateLookupErr:
		return false, AutoStartBlockRuntimeLookupErr
	}
	if capacityFull(running, maxConcurrent) {
		return false, AutoStartBlockCapacityFull
	}
	return true, AutoStartBlockNone
}

// assertDispatchReceipt verifies the dispatch outcome is consistent with the
// observable task rows. A replay must return the stored (non-empty) receipt; a
// fresh would_enqueue must produce task rows that are EXACTLY the reported
// active task set — identical row count and identical ID set, with no extra
// tasks (HIV-465 item 3). Returns a non-empty reason when the receipt is
// inconsistent (caller should treat the issue as blocked), or
// AutoStartBlockNone when consistent.
func (s *ProjectAutoStartService) assertDispatchReceipt(ctx context.Context, issueID pgtype.UUID, dr *DispatchResult) AutoStartBlockReason {
	if dr == nil {
		return AutoStartBlockTaskRowMismatch
	}
	if dr.Replayed {
		// Replay must surface the stored receipt — an empty receipt means the
		// idempotency row pointed at no tasks.
		if len(dr.TaskIDs) == 0 {
			return AutoStartBlockReplayEmpty
		}
		return AutoStartBlockNone
	}
	if dr.Decision != DecisionWouldEnqueue {
		// AlreadyActive / Blocked decisions carry no new task to verify.
		return AutoStartBlockNone
	}
	// Fresh enqueue: the active task set for this issue must match the receipt
	// exactly. Members-included is not enough — no extra tasks either.
	if len(dr.TaskIDs) == 0 {
		return AutoStartBlockTaskRowMismatch
	}
	active, err := s.Queries.ListActiveTasksByIssue(ctx, issueID)
	if err != nil {
		// Cannot prove the rows landed — fail closed.
		return AutoStartBlockTaskRowMismatch
	}
	if len(active) != len(dr.TaskIDs) {
		return AutoStartBlockTaskRowMismatch
	}
	activeSet := make(map[string]bool, len(active))
	for _, t := range active {
		activeSet[util.UUIDToString(t.ID)] = true
	}
	for _, id := range dr.TaskIDs {
		if !activeSet[id] {
			return AutoStartBlockTaskRowMismatch
		}
	}
	return AutoStartBlockNone
}

// autostartIssueStub builds a minimal db.Issue from a wave row, carrying only
// the fields assessReadiness needs (id, status, assignee). This avoids an
// extra GetIssue round-trip in the read-only Preview path.
func autostartIssueStub(row db.ListProjectReadyIssuesRow) db.Issue {
	return db.Issue{
		ID:           row.IssueID,
		Status:       row.IssueStatus,
		AssigneeType: row.IssueAssigneeType,
		AssigneeID:   row.IssueAssigneeID,
	}
}

// statusBlockReason returns a non-empty reason when the issue status is not
// dispatchable (terminal or blocked), or AutoStartBlockNone otherwise. Pure
// and DB-free so it is exhaustively unit-tested alongside the capacity gate.
func statusBlockReason(status string) AutoStartBlockReason {
	switch status {
	case "done", "cancelled":
		return AutoStartBlockTerminalStatus
	case "blocked":
		return AutoStartBlockBlockedStatus
	default:
		return AutoStartBlockNone
	}
}

// prereqBlockReason maps the wave SQL's parent prerequisite gate to a block
// reason. Pure / DB-free; rows that fail the gate must stay visible as
// blocked (missing_prerequisite), never vanish from the result set. An
// invalid (unreadable) flag also fails closed to blocked.
func prereqBlockReason(met pgtype.Bool) AutoStartBlockReason {
	if !met.Valid || !met.Bool {
		return AutoStartBlockMissingPrereq
	}
	return AutoStartBlockNone
}

// capacityFull reports whether the agent has reached its concurrency cap,
// mirroring the canonical claim path (running >= max_concurrent_tasks blocks
// the claim, see task.go ClaimTask). maxConcurrent <= 0 therefore means
// capacity_full — 0 allocated slots can never admit running >= 0 tasks — and
// must NOT be treated as unbounded (HIV-473 item 3). Pure / DB-free.
func capacityFull(running, maxConcurrent int) bool {
	if maxConcurrent <= 0 {
		return true
	}
	return running >= maxConcurrent
}

// autostartPerIssueKey derives a position-independent per-issue idempotency
// key from the batch key and issue id. Stable across wave re-ordering and wave
// membership changes so a repeated click replays the same receipt. Pure /
// DB-free.
func autostartPerIssueKey(batchKey, issueID string) string {
	return batchKey + ":" + issueID
}

// autostartDuplicateSelection returns the set of issue ids that appear MORE
// than once in a selection list. A repeated id is an explicit fail-closed
// signal — the affected issue must be blocked, never silently deduplicated
// into a Set (HIV-465 item 1). Pure / DB-free.
func autostartDuplicateSelection(selected []pgtype.UUID) map[string]bool {
	counts := make(map[string]int, len(selected))
	for _, id := range selected {
		if key := util.UUIDToString(id); key != "" {
			counts[key]++
		}
	}
	dups := make(map[string]bool, len(counts))
	for key, count := range counts {
		if count > 1 {
			dups[key] = true
		}
	}
	return dups
}
