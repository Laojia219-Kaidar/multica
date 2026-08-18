package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProjectLifecycleControlService implements the Slice 2 owner control
// operations (continue / pause_dispatch / resume) over the existing
// project/issue/task truth. Every write is preview-first, fail-closed on
// missing lead or duplicate authority, and idempotent.
type ProjectLifecycleControlService struct {
	Queries   *db.Queries
	Tasks     *TaskService
	TxStarter TxStarter
}

func NewProjectLifecycleControlService(q *db.Queries, tasks *TaskService) *ProjectLifecycleControlService {
	var txStarter TxStarter
	if tasks != nil {
		txStarter = tasks.TxStarter
	}
	return &ProjectLifecycleControlService{Queries: q, Tasks: tasks, TxStarter: txStarter}
}

// ControlAction is the closed set of Slice 2 project control operations.
type ControlAction string

const (
	ActionContinue                 ControlAction = "continue"
	ActionPauseDispatch            ControlAction = "pause_dispatch"
	ActionResume                   ControlAction = "resume"
	ActionClose                    ControlAction = "close"
	ActionStopCurrent              ControlAction = "stop_current"
	ActionSupersede                ControlAction = "supersede"
	ActionGenerateClosurePackage   ControlAction = "generate_closure_package"
	ActionRepairTerminalProjection ControlAction = "repair_terminal_projection"
)

// ControlReceipt is the structured, idempotent operation receipt. It records
// what the operation changed; replayed (idempotent) commits report the same
// result without duplicating side effects.
type ControlReceipt struct {
	Action         string   `json:"action"`
	ProjectID      string   `json:"project_id"`
	Applied        bool     `json:"applied"`
	Replayed       bool     `json:"replayed"`
	IdempotencyKey string   `json:"idempotency_key"`
	BeforeStatus   string   `json:"before_status"`
	AfterStatus    string   `json:"after_status"`
	TaskID         *string  `json:"task_id,omitempty"`
	IssueID        *string  `json:"issue_id,omitempty"`
	RecoveryOf     *string  `json:"recovery_of,omitempty"`
	Blockers       []string `json:"blockers,omitempty"`
}

var (
	ErrProjectLifecycleLeadRequired        = errors.New("accountable lead required")
	ErrProjectLifecycleNoFrontier          = errors.New("no ready frontier issue")
	ErrProjectLifecycleIdempotencyRequired = errors.New("idempotency key required")
	ErrProjectLifecycleTransactionRequired = errors.New("project lifecycle transaction starter required")
)

// validateProjectControl checks the fail-closed gates shared by every action.
func validateProjectControl(proj db.Project, action ControlAction) []string {
	var blockers []string
	// Terminal projects must never be re-opened (Quinn invariant 3).
	if proj.Status == "completed" || proj.Status == "cancelled" {
		blockers = append(blockers, "PROJECT_TERMINAL")
		return blockers
	}
	// Lead gate applies to continue/resume (and Slice 4 close), NOT pause:
	// stopping new dispatch must not be blocked by a missing lead (Gauss #5).
	if (action == ActionContinue || action == ActionResume || action == ActionClose) && (!proj.LeadID.Valid || !proj.LeadType.Valid) {
		blockers = append(blockers, "ACCOUNTABLE_LEAD_REQUIRED")
	}
	if dupOf := frozenSupersessions[util.UUIDToString(proj.ID)]; dupOf != "" {
		blockers = append(blockers, "DUPLICATE_AUTHORITY_OWNER_DECISION")
	}
	if action == ActionContinue && proj.Status == "paused" {
		blockers = append(blockers, "PROJECT_PAUSED_RESUME_FIRST")
	}
	if action == ActionResume && proj.Status != "paused" {
		blockers = append(blockers, "PROJECT_NOT_PAUSED")
	}
	return blockers
}

// ContinuePreview describes the planned effect of continue without writing.
type ContinuePreview struct {
	ProjectID     string   `json:"project_id"`
	Health        string   `json:"health"`
	LeadType      *string  `json:"lead_type"`
	LeadID        *string  `json:"lead_id"`
	TargetIssueID string   `json:"target_issue_id"`
	TargetIssueNo int32    `json:"target_issue_number"`
	TargetAgentID *string  `json:"target_agent_id"`
	Blockers      []string `json:"blockers"`
}

// PreviewContinue returns the ready frontier issue that continue would
// dispatch, without enqueuing anything.
func (s *ProjectLifecycleControlService) PreviewContinue(ctx context.Context, workspaceID, projectID pgtype.UUID) (*ContinuePreview, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, ErrProjectLifecycleNotFound
	}
	preview := &ContinuePreview{
		ProjectID: util.UUIDToString(proj.ID),
		Health:    string(HealthActiveWithFrontier),
		LeadType:  textOrNil(proj.LeadType),
		LeadID:    uuidOrNil(proj.LeadID),
	}
	preview.Blockers = validateProjectControl(proj, ActionContinue)
	if len(preview.Blockers) > 0 {
		return preview, nil
	}
	issue, err := s.frontierIssue(ctx, workspaceID, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectLifecycleNoFrontier) {
			return preview, nil
		}
		return nil, err
	}
	preview.TargetIssueID = util.UUIDToString(issue.ID)
	preview.TargetIssueNo = issue.Number
	if issue.AssigneeID.Valid {
		v := util.UUIDToString(issue.AssigneeID)
		preview.TargetAgentID = &v
	}
	return preview, nil
}

// Continue dispatches the ready frontier issue as a fresh task. It reuses the
// existing TaskService so it never builds a second task engine, and it is
// idempotent: an already-pending task for the frontier issue returns the same
// task with Replayed=true instead of creating a duplicate.
func (s *ProjectLifecycleControlService) Continue(ctx context.Context, workspaceID, projectID pgtype.UUID, idempotencyKey string) (ControlReceipt, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return ControlReceipt{}, ErrProjectLifecycleNotFound
	}
	receipt := ControlReceipt{
		Action:         string(ActionContinue),
		ProjectID:      util.UUIDToString(proj.ID),
		IdempotencyKey: idempotencyKey,
		BeforeStatus:   proj.Status,
		AfterStatus:    proj.Status,
	}
	if prior, err := s.receiptGuard(ctx, workspaceID, projectID, ActionContinue, idempotencyKey); err != nil {
		return ControlReceipt{}, err
	} else if prior != nil {
		return receiptToControl(*prior, true), nil
	}
	finalize := func() (ControlReceipt, error) { return s.finish(ctx, workspaceID, receipt) }
	if blockers := validateProjectControl(proj, ActionContinue); len(blockers) > 0 {
		receipt.Blockers = blockers
		return finalize()
	}
	issue, err := s.frontierIssue(ctx, workspaceID, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectLifecycleNoFrontier) {
			receipt.Blockers = []string{"NO_READY_FRONTIER"}
			return finalize()
		}
		return ControlReceipt{}, err
	}
	// Idempotent replay: if the frontier issue already has a live task, return
	// it instead of creating a duplicate (contract: 已有等价 live task 幂等返回).
	if existing := s.activeTaskForIssue(ctx, issue.ID); existing != nil {
		receipt.TaskID = uuidOrNil(existing.ID)
		receipt.IssueID = uuidOrNil(issue.ID)
		receipt.Replayed = true
		return finalize()
	}
	task, err := s.Tasks.EnqueueTaskForIssue(ctx, issue)
	if err != nil {
		if errors.Is(err, ErrDuplicatePendingTask) {
			// Lost a concurrent race: surface the already-live task.
			if existing := s.activeTaskForIssue(ctx, issue.ID); existing != nil {
				receipt.TaskID = uuidOrNil(existing.ID)
				receipt.IssueID = uuidOrNil(issue.ID)
				receipt.Replayed = true
				return finalize()
			}
			receipt.Blockers = []string{"DUPLICATE_PENDING_TASK"}
			return finalize()
		}
		return ControlReceipt{}, err
	}
	receipt.TaskID = uuidOrNil(task.ID)
	receipt.IssueID = uuidOrNil(issue.ID)
	receipt.Applied = true
	return finalize()
}

// PauseDispatch stops NEW dispatch only: it flips project.status to paused and
// leaves running tasks untouched (stop-current is a separate explicit action).
func (s *ProjectLifecycleControlService) PauseDispatch(ctx context.Context, workspaceID, projectID pgtype.UUID, idempotencyKey string) (ControlReceipt, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return ControlReceipt{}, ErrProjectLifecycleNotFound
	}
	receipt := ControlReceipt{
		Action:         string(ActionPauseDispatch),
		ProjectID:      util.UUIDToString(proj.ID),
		IdempotencyKey: idempotencyKey,
		BeforeStatus:   proj.Status,
		AfterStatus:    proj.Status,
	}
	if prior, err := s.receiptGuard(ctx, workspaceID, projectID, ActionPauseDispatch, idempotencyKey); err != nil {
		return ControlReceipt{}, err
	} else if prior != nil {
		return receiptToControl(*prior, true), nil
	}
	finalize := func() (ControlReceipt, error) { return s.finish(ctx, workspaceID, receipt) }
	if blockers := validateProjectControl(proj, ActionPauseDispatch); len(blockers) > 0 {
		receipt.Blockers = blockers
		return finalize()
	}
	if proj.Status == "paused" {
		receipt.Replayed = true
		return finalize()
	}
	if err := s.setProjectStatus(ctx, proj, "paused"); err != nil {
		return ControlReceipt{}, err
	}
	receipt.Applied = true
	receipt.AfterStatus = "paused"
	return finalize()
}

// Resume reactivates a paused project and dispatches its ready frontier. It
// never revives a terminal task: it creates a fresh one on the frontier issue.
func (s *ProjectLifecycleControlService) Resume(ctx context.Context, workspaceID, projectID pgtype.UUID, idempotencyKey string) (ControlReceipt, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return ControlReceipt{}, ErrProjectLifecycleNotFound
	}
	receipt := ControlReceipt{
		Action:         string(ActionResume),
		ProjectID:      util.UUIDToString(proj.ID),
		IdempotencyKey: idempotencyKey,
		BeforeStatus:   proj.Status,
		AfterStatus:    proj.Status,
	}
	if prior, err := s.receiptGuard(ctx, workspaceID, projectID, ActionResume, idempotencyKey); err != nil {
		return ControlReceipt{}, err
	} else if prior != nil {
		return receiptToControl(*prior, true), nil
	}
	finalize := func() (ControlReceipt, error) { return s.finish(ctx, workspaceID, receipt) }
	if blockers := validateProjectControl(proj, ActionResume); len(blockers) > 0 {
		receipt.Blockers = blockers
		return finalize()
	}
	if err := s.setProjectStatus(ctx, proj, "in_progress"); err != nil {
		return ControlReceipt{}, err
	}
	receipt.Applied = true
	receipt.AfterStatus = "in_progress"
	issue, err := s.frontierIssue(ctx, workspaceID, projectID)
	if err == nil {
		// Idempotent: if the frontier already has a live task (running or
		// pending), resume reactivates the project but returns that task —
		// never a duplicate (Gauss re-review #5).
		if existing := s.activeTaskForIssue(ctx, issue.ID); existing != nil {
			receipt.TaskID = uuidOrNil(existing.ID)
			receipt.IssueID = uuidOrNil(issue.ID)
			receipt.Replayed = true
		} else if task, enqErr := s.Tasks.EnqueueTaskForIssue(ctx, issue); enqErr == nil {
			receipt.TaskID = uuidOrNil(task.ID)
			receipt.IssueID = uuidOrNil(issue.ID)
		} else if errors.Is(enqErr, ErrDuplicatePendingTask) {
			receipt.Replayed = true
			if existing := s.activeTaskForIssue(ctx, issue.ID); existing != nil {
				receipt.TaskID = uuidOrNil(existing.ID)
				receipt.IssueID = uuidOrNil(issue.ID)
			}
		} else {
			// Surface the real enqueue failure; never report a silent partial
			// success (Gauss #4 / Quinn resume-吞错).
			receipt.Blockers = append(receipt.Blockers, "ENQUEUE_FAILED: "+enqErr.Error())
		}
	}
	return finalize()
}

// StopCurrentPreview lists the live tasks stop-current would cancel.
type StopCurrentPreview struct {
	ProjectID string         `json:"project_id"`
	LiveTasks []FrontierTask `json:"live_tasks"`
	Blockers  []string       `json:"blockers"`
}

// PreviewStopCurrent returns the live tasks for a project with zero writes.
func (s *ProjectLifecycleControlService) PreviewStopCurrent(ctx context.Context, workspaceID, projectID pgtype.UUID) (*StopCurrentPreview, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, ErrProjectLifecycleNotFound
	}
	preview := &StopCurrentPreview{ProjectID: util.UUIDToString(proj.ID), LiveTasks: []FrontierTask{}}
	preview.Blockers = validateProjectControl(proj, ActionStopCurrent)
	if len(preview.Blockers) > 0 {
		return preview, nil
	}
	tasks, err := s.Queries.ListProjectActiveTasks(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if util.UUIDToString(t.ProjectID) == util.UUIDToString(projectID) {
			preview.LiveTasks = append(preview.LiveTasks, FrontierTask{
				TaskID:      util.UUIDToString(t.TaskID),
				Status:      t.TaskStatus,
				AgentID:     uuidOrNil(t.AgentID),
				IssueID:     uuidOrNil(t.IssueID),
				IssueNumber: t.IssueNumber,
				IssueTitle:  t.IssueTitle,
			})
		}
	}
	return preview, nil
}

// StopCurrent terminates every live task on the project's issues (the explicit,
// separate "stop running work" action — pause_dispatch only stops NEW dispatch).
func (s *ProjectLifecycleControlService) StopCurrent(ctx context.Context, workspaceID, projectID pgtype.UUID, idempotencyKey string) (ControlReceipt, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return ControlReceipt{}, ErrProjectLifecycleNotFound
	}
	receipt := ControlReceipt{
		Action:         string(ActionStopCurrent),
		ProjectID:      util.UUIDToString(proj.ID),
		IdempotencyKey: idempotencyKey,
		BeforeStatus:   proj.Status,
		AfterStatus:    proj.Status,
	}
	if prior, err := s.receiptGuard(ctx, workspaceID, projectID, ActionStopCurrent, idempotencyKey); err != nil {
		return ControlReceipt{}, err
	} else if prior != nil {
		return receiptToControl(*prior, true), nil
	}
	finalize := func() (ControlReceipt, error) { return s.finish(ctx, workspaceID, receipt) }
	if blockers := validateProjectControl(proj, ActionStopCurrent); len(blockers) > 0 {
		receipt.Blockers = blockers
		return finalize()
	}
	tasks, err := s.Queries.ListProjectActiveTasks(ctx, workspaceID)
	if err != nil {
		return ControlReceipt{}, err
	}
	cancelledIssues := map[string]struct{}{}
	for _, t := range tasks {
		if util.UUIDToString(t.ProjectID) != util.UUIDToString(projectID) {
			continue
		}
		iid := util.UUIDToString(t.IssueID)
		if iid == "" {
			continue
		}
		if _, seen := cancelledIssues[iid]; seen {
			continue
		}
		if err := s.Tasks.CancelTasksForIssue(ctx, t.IssueID); err != nil {
			receipt.Blockers = append(receipt.Blockers, "CANCEL_FAILED: "+err.Error())
			return finalize()
		}
		cancelledIssues[iid] = struct{}{}
	}
	receipt.Applied = true
	return finalize()
}

// TerminalProjectionRepairPreview is a bounded, preview-first diagnosis of a
// terminal project whose live issue/task projection does not agree with its
// stored status. It is not a general project reopen operation.
type TerminalProjectionRepairPreview struct {
	ProjectID             string   `json:"project_id"`
	Status                string   `json:"status"`
	Finding               string   `json:"finding,omitempty"`
	NonterminalIssueCount int      `json:"nonterminal_issue_count"`
	ActiveTaskCount       int      `json:"active_task_count"`
	AfterStatus           string   `json:"after_status"`
	Blockers              []string `json:"blockers,omitempty"`
	NextAction            string   `json:"next_action,omitempty"`
}

// TerminalProjectionRepairReceipt is the narrow lifecycle receipt returned by
// repair_terminal_projection. The durable portion is stored in the existing
// project_lifecycle_receipt table; Finding and NextAction are derived
// diagnostics included for the owner-facing response.
type TerminalProjectionRepairReceipt struct {
	ControlReceipt
	Finding    string `json:"finding,omitempty"`
	NextAction string `json:"next_action,omitempty"`
}

// PreviewRepairTerminalProjection reads the current project lifecycle and
// computes the only permitted repair. It never writes project or task state.
func (s *ProjectLifecycleControlService) PreviewRepairTerminalProjection(ctx context.Context, workspaceID, projectID pgtype.UUID) (*TerminalProjectionRepairPreview, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, ErrProjectLifecycleNotFound
	}
	projector := NewProjectLifecycleProjector(s.Queries)
	snap, err := projector.GetSnapshot(ctx, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	preview := &TerminalProjectionRepairPreview{
		ProjectID: util.UUIDToString(proj.ID), Status: proj.Status,
		Finding:               snap.TerminalProjectionFinding,
		NonterminalIssueCount: snap.NonterminalIssueCount,
		ActiveTaskCount:       snap.ActiveTaskCount,
		AfterStatus:           proj.Status,
		NextAction:            snap.TerminalProjectionNextAction,
	}
	if snap.TerminalProjectionFinding == string(TerminalProjectionCompletedWithOpenWork) {
		preview.AfterStatus = "in_progress"
		return preview, nil
	}
	if snap.TerminalProjectionFinding == string(TerminalProjectionCancelledWithActive) {
		preview.Blockers = []string{"CANCELLED_NEVER_REOPEN", "ACTIVE_TASKS_PRESENT"}
		preview.NextAction = "stop active tasks or record disposition; cancelled projects are never reopened"
		return preview, nil
	}
	preview.Blockers = []string{"TERMINAL_PROJECTION_NOT_INCONSISTENT"}
	preview.NextAction = "no repair: require a completed project with nonterminal issue(s) or active task(s)"
	return preview, nil
}

// RepairTerminalProjection repairs only the stale completed projection case.
// Cancelled projects are deliberately fail-closed and receive a stop/
// disposition next action instead. Every call requires an idempotency key and
// reuses the existing lifecycle receipt ledger.
func (s *ProjectLifecycleControlService) RepairTerminalProjection(ctx context.Context, workspaceID, projectID pgtype.UUID, idempotencyKey string) (TerminalProjectionRepairReceipt, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return TerminalProjectionRepairReceipt{}, ErrProjectLifecycleIdempotencyRequired
	}
	if s.TxStarter == nil {
		return TerminalProjectionRepairReceipt{}, ErrProjectLifecycleTransactionRequired
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return TerminalProjectionRepairReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	queries := s.Queries.WithTx(tx)
	proj, err := queries.GetProjectInWorkspaceForUpdate(ctx, db.GetProjectInWorkspaceForUpdateParams{
		ID: projectID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TerminalProjectionRepairReceipt{}, ErrProjectLifecycleNotFound
		}
		return TerminalProjectionRepairReceipt{}, err
	}
	if prior, err := receiptGuardWithQueries(ctx, queries, workspaceID, projectID, ActionRepairTerminalProjection, idempotencyKey); err != nil {
		return TerminalProjectionRepairReceipt{}, err
	} else if prior != nil {
		result := replayTerminalProjectionRepair(*prior)
		if err := tx.Commit(ctx); err != nil {
			return TerminalProjectionRepairReceipt{}, err
		}
		return result, nil
	}

	// Re-read the issue/task projection through the same transaction that owns
	// the locked project row. The status change and receipt below therefore
	// cannot be observed independently, and a competing repair of this project
	// waits until this decision is committed or rolled back.
	projector := NewProjectLifecycleProjector(queries)
	snap, err := projector.GetSnapshot(ctx, workspaceID, projectID)
	if err != nil {
		return TerminalProjectionRepairReceipt{}, err
	}
	receipt := ControlReceipt{
		Action: string(ActionRepairTerminalProjection), ProjectID: util.UUIDToString(proj.ID),
		IdempotencyKey: idempotencyKey, BeforeStatus: proj.Status, AfterStatus: proj.Status,
	}
	result := terminalProjectionRepairReceipt(receipt)
	result.Finding = snap.TerminalProjectionFinding
	result.NextAction = snap.TerminalProjectionNextAction
	if snap.TerminalProjectionFinding == string(TerminalProjectionCancelledWithActive) {
		receipt.Blockers = []string{"CANCELLED_NEVER_REOPEN", "ACTIVE_TASKS_PRESENT"}
		result.Blockers = receipt.Blockers
		result.NextAction = "stop active tasks or record disposition; cancelled projects are never reopened"
	} else if snap.TerminalProjectionFinding != string(TerminalProjectionCompletedWithOpenWork) {
		receipt.Blockers = []string{"TERMINAL_PROJECTION_NOT_INCONSISTENT"}
		result.Blockers = receipt.Blockers
		result.NextAction = "no repair: require a completed project with nonterminal issue(s) or active task(s)"
	} else if err := setProjectStatusWithQueries(ctx, queries, proj, "in_progress"); err != nil {
		return TerminalProjectionRepairReceipt{}, err
	} else {
		receipt.Applied = true
		receipt.AfterStatus = "in_progress"
		result = terminalProjectionRepairReceipt(receipt)
		result.Finding = string(TerminalProjectionCompletedWithOpenWork)
		result.NextAction = "repaired stale completed projection; continue normal lifecycle reconciliation"
	}
	stored, err := finishWithQueries(ctx, queries, workspaceID, receipt)
	if err != nil {
		return TerminalProjectionRepairReceipt{}, err
	}
	result.ControlReceipt = stored
	if err := tx.Commit(ctx); err != nil {
		return TerminalProjectionRepairReceipt{}, err
	}
	return result, nil
}

func replayTerminalProjectionRepair(prior db.ProjectLifecycleReceipt) TerminalProjectionRepairReceipt {
	result := terminalProjectionRepairReceipt(receiptToControl(prior, true))
	if prior.BeforeStatus == "cancelled" && hasLifecycleBlocker(result.Blockers, "CANCELLED_NEVER_REOPEN") {
		result.Finding = string(TerminalProjectionCancelledWithActive)
		result.NextAction = "stop active tasks or record disposition; cancelled projects are never reopened"
	} else if prior.BeforeStatus == "completed" && prior.AfterStatus == "in_progress" {
		result.Finding = string(TerminalProjectionCompletedWithOpenWork)
		result.NextAction = "repaired stale completed projection; continue normal lifecycle reconciliation"
	} else {
		result.NextAction = "no repair: require a completed project with nonterminal issue(s) or active task(s)"
	}
	return result
}

func hasLifecycleBlocker(blockers []string, want string) bool {
	for _, blocker := range blockers {
		if blocker == want {
			return true
		}
	}
	return false
}

func terminalProjectionRepairReceipt(r ControlReceipt) TerminalProjectionRepairReceipt {
	return TerminalProjectionRepairReceipt{ControlReceipt: r}
}

// frontierIssue returns the highest-priority dispatchable nonterminal issue,
// i.e. the exact frontier to dispatch — never "all issues". in_review/blocked
// are repair gates, not dispatch targets.
func (s *ProjectLifecycleControlService) frontierIssue(ctx context.Context, workspaceID, projectID pgtype.UUID) (db.Issue, error) {
	issues, err := s.Queries.ListIssues(ctx, db.ListIssuesParams{
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
		Limit:       1000,
		Offset:      0,
	})
	if err != nil {
		return db.Issue{}, err
	}
	// ListIssues returns position order; pick the first dispatchable issue.
	for _, is := range issues {
		switch is.Status {
		case "in_progress", "todo", "backlog":
			return issueRowToIssue(is), nil
		}
	}
	return db.Issue{}, ErrProjectLifecycleNoFrontier
}

// activeTaskForIssue returns the currently-live (nonterminal) task for an
// issue, if any. ListActiveTasksByIssue covers queued/dispatched/running/
// waiting_local_directory — the states that mean "work is live" — which is the
// regression surface pause/resume must guard.
func (s *ProjectLifecycleControlService) activeTaskForIssue(ctx context.Context, issueID pgtype.UUID) *db.AgentTaskQueue {
	tasks, err := s.Queries.ListActiveTasksByIssue(ctx, issueID)
	if err != nil || len(tasks) == 0 {
		return nil
	}
	return &tasks[0]
}

// PreviewPause returns the planned pause effect with zero writes.
func (s *ProjectLifecycleControlService) PreviewPause(ctx context.Context, workspaceID, projectID pgtype.UUID) (ControlReceipt, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return ControlReceipt{}, ErrProjectLifecycleNotFound
	}
	receipt := ControlReceipt{
		Action: string(ActionPauseDispatch), ProjectID: util.UUIDToString(proj.ID),
		BeforeStatus: proj.Status, AfterStatus: proj.Status,
	}
	receipt.Blockers = validateProjectControl(proj, ActionPauseDispatch)
	if len(receipt.Blockers) == 0 && proj.Status != "paused" {
		receipt.AfterStatus = "paused"
	}
	return receipt, nil
}

// PreviewResume returns the planned resume effect with zero writes.
func (s *ProjectLifecycleControlService) PreviewResume(ctx context.Context, workspaceID, projectID pgtype.UUID) (ControlReceipt, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return ControlReceipt{}, ErrProjectLifecycleNotFound
	}
	receipt := ControlReceipt{
		Action: string(ActionResume), ProjectID: util.UUIDToString(proj.ID),
		BeforeStatus: proj.Status, AfterStatus: proj.Status,
	}
	receipt.Blockers = validateProjectControl(proj, ActionResume)
	if len(receipt.Blockers) == 0 {
		receipt.AfterStatus = "in_progress"
	}
	return receipt, nil
}

// setProjectStatus writes a new project status, preserving all other fields.
func (s *ProjectLifecycleControlService) setProjectStatus(ctx context.Context, proj db.Project, status string) error {
	return setProjectStatusWithQueries(ctx, s.Queries, proj, status)
}

func setProjectStatusWithQueries(ctx context.Context, queries *db.Queries, proj db.Project, status string) error {
	_, err := queries.UpdateProject(ctx, db.UpdateProjectParams{
		ID:                    proj.ID,
		WorkspaceID:           proj.WorkspaceID,
		Revision:              proj.Revision,
		Title:                 textValue(proj.Title),
		Description:           proj.Description,
		Icon:                  proj.Icon,
		Status:                textValue(status),
		Priority:              textValue(proj.Priority),
		LeadType:              proj.LeadType,
		LeadID:                proj.LeadID,
		StartDate:             proj.StartDate,
		DueDate:               proj.DueDate,
		RepoInheritancePolicy: pgtype.Text{String: proj.RepoInheritancePolicy, Valid: proj.RepoInheritancePolicy != ""},
	})
	return err
}

// issueRowToIssue converts a ListIssues row back to the Issue model used by
// the task service (only the fields enqueue needs).
func issueRowToIssue(r db.ListIssuesRow) db.Issue {
	return db.Issue{
		ID:           r.ID,
		WorkspaceID:  r.WorkspaceID,
		Title:        r.Title,
		Description:  r.Description,
		Status:       r.Status,
		Priority:     r.Priority,
		AssigneeType: r.AssigneeType,
		AssigneeID:   r.AssigneeID,
		CreatorType:  r.CreatorType,
		CreatorID:    r.CreatorID,
		ProjectID:    r.ProjectID,
		Number:       r.Number,
		Stage:        r.Stage,
		Metadata:     r.Metadata,
		Properties:   r.Properties,
	}
}

func textValue(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

// ErrProjectPausedDispatch blocks new task enqueue for a paused project. It is
// returned by the TaskService enqueue chokepoint so pause actually stops new
// dispatch (Gauss phase_critical #1).
var ErrProjectPausedDispatch = errors.New("project is paused: dispatch stopped")

// ErrProjectLifecycleConflict is returned when an idempotency key is replayed
// with a different payload digest (same key, different operation).
var ErrProjectLifecycleConflict = errors.New("idempotency key conflict: different payload")

// ClosurePackage is a candidate project closure package (Slice 4). It is a
// DERIVED snapshot, never a second truth table: the projector recomputes it
// from the live project/issue/task/outcome state.
type ClosurePackage struct {
	PackageID             string   `json:"package_id"`
	ProjectID             string   `json:"project_id"`
	Status                string   `json:"status"`
	LeadType              *string  `json:"lead_type"`
	LeadID                *string  `json:"lead_id"`
	TerminalIssueCount    int      `json:"terminal_issue_count"`
	NonterminalIssueCount int      `json:"nonterminal_issue_count"`
	ActiveTaskCount       int      `json:"active_task_count"`
	OutcomeConfirmed      int      `json:"outcome_confirmed"`
	OutcomeTotal          int      `json:"outcome_total"`
	DuplicateOfProjectID  *string  `json:"duplicate_of_project_id"`
	ReviewRequired        bool     `json:"review_required"`
	ClosureReady          bool     `json:"closure_ready"`
	Blockers              []string `json:"blockers"`
	Digest                string   `json:"digest"`
}

// GenerateClosurePackage computes a candidate closure package without writing
// anything. It never auto-accepts outcomes and never auto-closes the project.
func (s *ProjectLifecycleControlService) GenerateClosurePackage(ctx context.Context, workspaceID, projectID pgtype.UUID, idempotencyKey string) (*ClosurePackage, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, ErrProjectLifecycleNotFound
	}
	projector := NewProjectLifecycleProjector(s.Queries)
	snap, err := projector.GetSnapshot(ctx, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	approved, err := s.Queries.HasApprovedClosureReview(ctx, db.HasApprovedClosureReviewParams{
		WorkspaceID: workspaceID, ProjectID: projectID,
	})
	if err != nil {
		approved = false
	}
	pkg := &ClosurePackage{
		PackageID:             idempotencyKey,
		ProjectID:             util.UUIDToString(proj.ID),
		Status:                proj.Status,
		LeadType:              textOrNil(proj.LeadType),
		LeadID:                uuidOrNil(proj.LeadID),
		TerminalIssueCount:    snap.TerminalIssueCount,
		NonterminalIssueCount: snap.NonterminalIssueCount,
		ActiveTaskCount:       snap.ActiveTaskCount,
		OutcomeConfirmed:      snap.OutcomeConfirmed,
		OutcomeTotal:          snap.OutcomeTotal,
		DuplicateOfProjectID:  snap.DuplicateOfProjectID,
		ReviewRequired:        !approved, // independent review must precede close
		ClosureReady:          snap.ClosureReady && approved,
		Blockers:              snap.ClosureBlockers,
	}
	pkg.Digest = closurePackageDigest(pkg)
	return pkg, nil
}

// ReviewClosurePackage records an independent approve/reject decision on a
// candidate closure package (gate 5). The reviewer must be a member who is not
// the project lead (reviewer != implementer).
func (s *ProjectLifecycleControlService) ReviewClosurePackage(ctx context.Context, workspaceID, projectID, reviewerUserID pgtype.UUID, approve bool) (string, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return "", ErrProjectLifecycleNotFound
	}
	if proj.LeadType.Valid && proj.LeadType.String == "member" && proj.LeadID.Valid && proj.LeadID.Bytes == reviewerUserID.Bytes {
		return "", ErrProjectLifecycleReviewerIsImplementer
	}
	decision := "reject"
	if approve {
		decision = "approve"
	}
	_, err = s.Queries.InsertClosurePackageReview(ctx, db.InsertClosurePackageReviewParams{
		WorkspaceID: workspaceID, ProjectID: projectID, ReviewerUserID: reviewerUserID, Decision: decision,
	})
	if err != nil {
		return "", err
	}
	return decision, nil
}

// ErrProjectLifecycleReviewerIsImplementer rejects a self-review.
var ErrProjectLifecycleReviewerIsImplementer = errors.New("closure reviewer must differ from the project lead")

// PreviewClose returns the closure gates and blockers with zero writes.
func (s *ProjectLifecycleControlService) PreviewClose(ctx context.Context, workspaceID, projectID pgtype.UUID) (*ClosurePackage, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, ErrProjectLifecycleNotFound
	}
	if blockers := validateProjectControl(proj, ActionClose); len(blockers) > 0 {
		return &ClosurePackage{ProjectID: util.UUIDToString(proj.ID), Status: proj.Status, Blockers: blockers, ReviewRequired: true}, nil
	}
	return s.GenerateClosurePackage(ctx, workspaceID, projectID, "")
}

// Close performs the project closure commit only when every gate is green
// (fail-closed). It writes project.status = completed and returns the receipt.
func (s *ProjectLifecycleControlService) Close(ctx context.Context, workspaceID, projectID pgtype.UUID, idempotencyKey string) (ControlReceipt, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return ControlReceipt{}, ErrProjectLifecycleNotFound
	}
	receipt := ControlReceipt{
		Action:         string(ActionClose),
		ProjectID:      util.UUIDToString(proj.ID),
		IdempotencyKey: idempotencyKey,
		BeforeStatus:   proj.Status,
		AfterStatus:    proj.Status,
	}
	if prior, err := s.receiptGuard(ctx, workspaceID, projectID, ActionClose, idempotencyKey); err != nil {
		return ControlReceipt{}, err
	} else if prior != nil {
		return receiptToControl(*prior, true), nil
	}
	finalize := func() (ControlReceipt, error) { return s.finish(ctx, workspaceID, receipt) }
	// Idempotent re-close of an already-completed project (Gauss/Quinn F2).
	if proj.Status == "completed" {
		receipt.Replayed = true
		return finalize()
	}
	if blockers := validateProjectControl(proj, ActionClose); len(blockers) > 0 {
		receipt.Blockers = blockers
		return finalize()
	}
	pkg, err := s.GenerateClosurePackage(ctx, workspaceID, projectID, idempotencyKey)
	if err != nil {
		return ControlReceipt{}, err
	}
	if len(pkg.Blockers) > 0 {
		receipt.Blockers = pkg.Blockers
		return finalize()
	}
	if pkg.ReviewRequired {
		// Hard fail-closed stub: the independent package-review record
		// mechanism is Slice 3 review-cell integration (W3). Until a reviewer
		// records approval, close refuses (Gauss P1 / red matrix C8 deferred).
		receipt.Blockers = []string{"CLOSURE_PACKAGE_REVIEW_REQUIRED"}
		return finalize()
	}
	if err := s.setProjectStatus(ctx, proj, "completed"); err != nil {
		return ControlReceipt{}, err
	}
	receipt.Applied = true
	receipt.AfterStatus = "completed"
	return finalize()
}

// Supersede marks a project terminal and records its source->target lineage
// (the superseding project id). It is the VC-10 duplicate/supersede executor.
// The source project is cancelled; the target keeps running. No receipt-guard
// replay here: a superseded project is PROJECT_TERMINAL on retry anyway.
func (s *ProjectLifecycleControlService) Supersede(ctx context.Context, workspaceID, projectID, targetProjectID pgtype.UUID, idempotencyKey string) (ControlReceipt, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return ControlReceipt{}, ErrProjectLifecycleNotFound
	}
	receipt := ControlReceipt{
		Action: string(ActionSupersede), ProjectID: util.UUIDToString(proj.ID),
		IdempotencyKey: idempotencyKey, BeforeStatus: proj.Status, AfterStatus: proj.Status,
	}
	if proj.Status == "cancelled" {
		receipt.Replayed = true
		return receipt, nil
	}
	if proj.Status == "completed" {
		receipt.Blockers = []string{"PROJECT_TERMINAL"}
		return receipt, nil
	}
	if !targetProjectID.Valid {
		receipt.Blockers = []string{"SUPERSEDE_TARGET_REQUIRED"}
		return receipt, nil
	}
	// Self-supersede is nonsensical.
	if util.UUIDToString(targetProjectID) == util.UUIDToString(proj.ID) {
		receipt.Blockers = []string{"SUPERSEDE_SELF_TARGET"}
		return receipt, nil
	}
	if err := s.setProjectStatus(ctx, proj, "cancelled"); err != nil {
		return ControlReceipt{}, err
	}
	receipt.Applied = true
	receipt.AfterStatus = "cancelled"
	return receipt, nil
}

// payloadDigest fingerprints an operation for idempotent replay detection.
func payloadDigest(action ControlAction, projectID pgtype.UUID) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s", action, util.UUIDToString(projectID))))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// receiptGuard checks idempotency before an action. It returns a prior receipt
// (replay) when the same key + digest was already applied, an error on conflict
// (same key, different digest), or (nil, nil) to proceed.
func (s *ProjectLifecycleControlService) receiptGuard(ctx context.Context, workspaceID, projectID pgtype.UUID, action ControlAction, idempotencyKey string) (*db.ProjectLifecycleReceipt, error) {
	return receiptGuardWithQueries(ctx, s.Queries, workspaceID, projectID, action, idempotencyKey)
}

func receiptGuardWithQueries(ctx context.Context, queries *db.Queries, workspaceID, projectID pgtype.UUID, action ControlAction, idempotencyKey string) (*db.ProjectLifecycleReceipt, error) {
	if idempotencyKey == "" {
		return nil, nil
	}
	digest := payloadDigest(action, projectID)
	existing, err := queries.GetProjectLifecycleReceipt(ctx, db.GetProjectLifecycleReceiptParams{
		WorkspaceID: workspaceID, IdempotencyKey: idempotencyKey,
	})
	if err == nil {
		if existing.PayloadDigest == digest {
			return &existing, nil
		}
		return nil, ErrProjectLifecycleConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return nil, nil
}

// storeReceipt persists the append-only operation receipt.
func (s *ProjectLifecycleControlService) storeReceipt(ctx context.Context, workspaceID pgtype.UUID, r ControlReceipt) error {
	return storeReceiptWithQueries(ctx, s.Queries, workspaceID, r)
}

func storeReceiptWithQueries(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID, r ControlReceipt) error {
	if r.IdempotencyKey == "" {
		return nil
	}
	projID := util.MustParseUUID(r.ProjectID)
	var taskID, issueID pgtype.UUID
	if r.TaskID != nil {
		taskID = util.MustParseUUID(*r.TaskID)
	}
	if r.IssueID != nil {
		issueID = util.MustParseUUID(*r.IssueID)
	}
	blockersJSON, _ := json.Marshal(r.Blockers)
	_, err := queries.InsertProjectLifecycleReceipt(ctx, db.InsertProjectLifecycleReceiptParams{
		WorkspaceID:    workspaceID,
		ProjectID:      projID,
		Action:         r.Action,
		IdempotencyKey: r.IdempotencyKey,
		PayloadDigest:  payloadDigest(ControlAction(r.Action), projID),
		BeforeStatus:   r.BeforeStatus,
		AfterStatus:    r.AfterStatus,
		TaskID:         taskID,
		IssueID:        issueID,
		Blockers:       blockersJSON,
		Applied:        r.Applied,
		Replayed:       r.Replayed,
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "project_lifecycle_receipt_idem_uidx" {
		return ErrProjectLifecycleConflict
	}
	return err
}

// receiptToControl converts a stored receipt row to the wire shape. On replay
// (replayed=true) this call applied nothing new: Applied=false, Replayed=true,
// while before/after/task/issue reflect the original stored effect.
func receiptToControl(r db.ProjectLifecycleReceipt, replayed bool) ControlReceipt {
	applied := r.Applied
	if replayed {
		applied = false
	}
	var blockers []string
	if len(r.Blockers) > 0 {
		_ = json.Unmarshal(r.Blockers, &blockers)
	}
	return ControlReceipt{
		Action:         r.Action,
		ProjectID:      util.UUIDToString(r.ProjectID),
		Applied:        applied,
		Replayed:       r.Replayed || replayed,
		IdempotencyKey: r.IdempotencyKey,
		BeforeStatus:   r.BeforeStatus,
		AfterStatus:    r.AfterStatus,
		TaskID:         uuidOrNil(r.TaskID),
		IssueID:        uuidOrNil(r.IssueID),
		Blockers:       blockers,
	}
}

// finish stores the receipt and returns it (append-only idempotency).
func (s *ProjectLifecycleControlService) finish(ctx context.Context, workspaceID pgtype.UUID, r ControlReceipt) (ControlReceipt, error) {
	return finishWithQueries(ctx, s.Queries, workspaceID, r)
}

func finishWithQueries(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID, r ControlReceipt) (ControlReceipt, error) {
	if err := storeReceiptWithQueries(ctx, queries, workspaceID, r); err != nil {
		return ControlReceipt{}, err
	}
	return r, nil
}

// closurePackageDigest returns a deterministic sha256 over the package's
// gate-relevant fields (provenance fingerprint, not a second truth).
func closurePackageDigest(p *ClosurePackage) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%d|%d|%d|%d|%d|%v|%v|%v",
		p.ProjectID, p.Status, ptrOrEmpty(p.LeadType), ptrOrEmpty(p.LeadID),
		p.TerminalIssueCount, p.NonterminalIssueCount, p.ActiveTaskCount,
		p.OutcomeConfirmed, p.OutcomeTotal, p.ReviewRequired,
		strings.Join(p.Blockers, ","), ptrOrEmpty(p.DuplicateOfProjectID))))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ptrOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
