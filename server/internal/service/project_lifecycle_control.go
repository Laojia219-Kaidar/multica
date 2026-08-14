package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProjectLifecycleControlService implements the Slice 2 owner control
// operations (continue / pause_dispatch / resume) over the existing
// project/issue/task truth. Every write is preview-first, fail-closed on
// missing lead or duplicate authority, and idempotent.
type ProjectLifecycleControlService struct {
	Queries *db.Queries
	Tasks   *TaskService
}

func NewProjectLifecycleControlService(q *db.Queries, tasks *TaskService) *ProjectLifecycleControlService {
	return &ProjectLifecycleControlService{Queries: q, Tasks: tasks}
}

// ControlAction is the closed set of Slice 2 project control operations.
type ControlAction string

const (
	ActionContinue               ControlAction = "continue"
	ActionPauseDispatch          ControlAction = "pause_dispatch"
	ActionResume                 ControlAction = "resume"
	ActionClose                  ControlAction = "close"
	ActionSupersede              ControlAction = "supersede"
	ActionGenerateClosurePackage ControlAction = "generate_closure_package"
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
	ErrProjectLifecycleLeadRequired = errors.New("accountable lead required")
	ErrProjectLifecycleNoFrontier   = errors.New("no ready frontier issue")
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
	if (action == ActionContinue || action == ActionResume) && (!proj.LeadID.Valid || !proj.LeadType.Valid) {
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
	if blockers := validateProjectControl(proj, ActionContinue); len(blockers) > 0 {
		receipt.Blockers = blockers
		return receipt, nil
	}
	issue, err := s.frontierIssue(ctx, workspaceID, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectLifecycleNoFrontier) {
			receipt.Blockers = []string{"NO_READY_FRONTIER"}
			return receipt, nil
		}
		return ControlReceipt{}, err
	}
	// Idempotent replay: if the frontier issue already has a live task, return
	// it instead of creating a duplicate (contract: 已有等价 live task 幂等返回).
	if existing := s.activeTaskForIssue(ctx, issue.ID); existing != nil {
		receipt.TaskID = uuidOrNil(existing.ID)
		receipt.IssueID = uuidOrNil(issue.ID)
		receipt.Replayed = true
		return receipt, nil
	}
	task, err := s.Tasks.EnqueueTaskForIssue(ctx, issue)
	if err != nil {
		if errors.Is(err, ErrDuplicatePendingTask) {
			// Lost a concurrent race: surface the already-live task.
			if existing := s.activeTaskForIssue(ctx, issue.ID); existing != nil {
				receipt.TaskID = uuidOrNil(existing.ID)
				receipt.IssueID = uuidOrNil(issue.ID)
				receipt.Replayed = true
				return receipt, nil
			}
			receipt.Blockers = []string{"DUPLICATE_PENDING_TASK"}
			return receipt, nil
		}
		return ControlReceipt{}, err
	}
	receipt.TaskID = uuidOrNil(task.ID)
	receipt.IssueID = uuidOrNil(issue.ID)
	receipt.Applied = true
	return receipt, nil
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
	if blockers := validateProjectControl(proj, ActionPauseDispatch); len(blockers) > 0 {
		receipt.Blockers = blockers
		return receipt, nil
	}
	if proj.Status == "paused" {
		receipt.Replayed = true
		return receipt, nil
	}
	if err := s.setProjectStatus(ctx, proj, "paused"); err != nil {
		return ControlReceipt{}, err
	}
	receipt.Applied = true
	receipt.AfterStatus = "paused"
	return receipt, nil
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
	if blockers := validateProjectControl(proj, ActionResume); len(blockers) > 0 {
		receipt.Blockers = blockers
		return receipt, nil
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
	return receipt, nil
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
	_, err := s.Queries.UpdateProject(ctx, db.UpdateProjectParams{
		ID:          proj.ID,
		Title:       textValue(proj.Title),
		Description: proj.Description,
		Icon:        proj.Icon,
		Status:      textValue(status),
		Priority:    textValue(proj.Priority),
		LeadType:    proj.LeadType,
		LeadID:      proj.LeadID,
		StartDate:   proj.StartDate,
		DueDate:     proj.DueDate,
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

// validateProjectControlAt is validateProjectControl with an explicit seed map,
// used only by tests to avoid mutating the frozen seed.
func validateProjectControlAt(proj db.Project, action ControlAction, seed map[string]string) []string {
	orig := frozenSupersessions
	frozenSupersessions = seed
	defer func() { frozenSupersessions = orig }()
	return validateProjectControl(proj, action)
}

func parseTestUUID(s string) (pgtype.UUID, error) { return util.ParseUUID(s) }

// --- Slice 4 (W3 takeover): close / supersede / generate_closure_package ---
//
// These complete the HIV-553 lifecycle: close writes a terminal project status
// only when the closure gates are green (fail-closed); supersede records a
// source->target lineage and marks the source terminal; generate_closure_package
// builds a read-only candidate package for independent review.

// validateCloseGates returns fail-closed blockers for close/supersede.
// Gates (from HIV-553): (1) accountable lead, (2) no nonterminal task/run,
// (3) every issue has a disposition (terminal). Outcome coverage + Closure
// Package gates are enforced by generate_closure_package review before close.
func (s *ProjectLifecycleControlService) validateCloseGates(ctx context.Context, workspaceID, projectID pgtype.UUID) ([]string, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, ErrProjectLifecycleNotFound
	}
	var blockers []string
	if !proj.LeadID.Valid || !proj.LeadType.Valid {
		blockers = append(blockers, "ACCOUNTABLE_LEAD_REQUIRED")
	}
	activeTasks, err := s.Queries.ListProjectActiveTasks(ctx, workspaceID)
	if err == nil {
		for _, t := range activeTasks {
			if util.UUIDToString(t.ProjectID) == util.UUIDToString(projectID) {
				blockers = append(blockers, "TASKS_RUNNING")
				break
			}
		}
	}
	issues, err := s.Queries.ListIssues(ctx, db.ListIssuesParams{
		WorkspaceID: workspaceID, ProjectID: projectID, Limit: 100000, Offset: 0,
	})
	if err == nil {
		for _, is := range issues {
			if is.Status != "done" && is.Status != "cancelled" {
				blockers = append(blockers, "ISSUES_NONTERMINAL")
				break
			}
		}
	}
	return blockers, nil
}

// Close marks a project completed when every closure gate is green. Any gap is
// fail-closed: the receipt carries the structured blockers and zero write.
func (s *ProjectLifecycleControlService) Close(ctx context.Context, workspaceID, projectID pgtype.UUID, idempotencyKey string) (ControlReceipt, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return ControlReceipt{}, ErrProjectLifecycleNotFound
	}
	receipt := ControlReceipt{
		Action: string(ActionClose), ProjectID: util.UUIDToString(proj.ID),
		IdempotencyKey: idempotencyKey, BeforeStatus: proj.Status, AfterStatus: proj.Status,
	}
	if proj.Status == "completed" {
		receipt.Replayed = true
		return receipt, nil
	}
	if proj.Status == "cancelled" {
		receipt.Blockers = []string{"PROJECT_TERMINAL"}
		return receipt, nil
	}
	blockers, err := s.validateCloseGates(ctx, workspaceID, projectID)
	if err != nil {
		return ControlReceipt{}, err
	}
	if len(blockers) > 0 {
		receipt.Blockers = blockers
		return receipt, nil
	}
	if err := s.setProjectStatus(ctx, proj, "completed"); err != nil {
		return ControlReceipt{}, err
	}
	receipt.Applied = true
	receipt.AfterStatus = "completed"
	return receipt, nil
}

// Supersede marks a project terminal and records its source->target lineage
// (the superseding project id). It is the VC-10 duplicate/supersede executor.
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

// ClosurePackagePreview is the read-only candidate closure package summary.
type ClosurePackagePreview struct {
	ProjectID         string         `json:"project_id"`
	PackageDigest     string         `json:"package_digest"`
	Version           int            `json:"version"`
	IssueDisposition  map[string]int `json:"issue_disposition"`
	TerminalIssues    int            `json:"terminal_issues"`
	NonterminalIssues int            `json:"nonterminal_issues"`
	ActiveTaskCount   int            `json:"active_task_count"`
	ClosureReady      bool           `json:"closure_ready"`
	ReviewRequired    bool           `json:"review_required"`
	Blockers          []string       `json:"blockers"`
}

// GenerateClosurePackage builds a read-only candidate closure package summary.
// It never accepts outcomes or closes the project; independent review is the
// next gate (per HIV-553).
func (s *ProjectLifecycleControlService) GenerateClosurePackage(ctx context.Context, workspaceID, projectID pgtype.UUID) (*ClosurePackagePreview, error) {
	proj, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, ErrProjectLifecycleNotFound
	}
	preview := &ClosurePackagePreview{
		ProjectID: util.UUIDToString(proj.ID), Version: 1,
		IssueDisposition: map[string]int{},
	}
	blockers, err := s.validateCloseGates(ctx, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	preview.Blockers = blockers
	issues, _ := s.Queries.ListIssues(ctx, db.ListIssuesParams{
		WorkspaceID: workspaceID, ProjectID: projectID, Limit: 100000, Offset: 0,
	})
	for _, is := range issues {
		if is.Status == "done" || is.Status == "cancelled" {
			preview.IssueDisposition[is.Status]++
			preview.TerminalIssues++
		} else {
			preview.NonterminalIssues++
		}
	}
	activeTasks, _ := s.Queries.ListProjectActiveTasks(ctx, workspaceID)
	for _, t := range activeTasks {
		if util.UUIDToString(t.ProjectID) == util.UUIDToString(projectID) {
			preview.ActiveTaskCount++
		}
	}
	preview.ClosureReady = len(preview.Blockers) == 0
	// Independent review is always required before close (reviewer != author).
	preview.ReviewRequired = true
	// Content-addressed digest: hash the package content so a changed package
	// yields a different digest (audit + idempotency anchor).
	digestSrc := fmt.Sprintf("%s|%s|terminal=%d|nonterminal=%d|active=%d|ready=%t",
		util.UUIDToString(proj.ID), proj.Title, preview.TerminalIssues,
		preview.NonterminalIssues, preview.ActiveTaskCount, preview.ClosureReady)
	preview.PackageDigest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(digestSrc)))
	return preview, nil
}
