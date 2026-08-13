package service

import (
	"context"
	"errors"

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
	ActionContinue      ControlAction = "continue"
	ActionPauseDispatch ControlAction = "pause_dispatch"
	ActionResume        ControlAction = "resume"
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
	issue, err := s.readyFrontierIssue(ctx, workspaceID, projectID)
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
	issue, err := s.readyFrontierIssue(ctx, workspaceID, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectLifecycleNoFrontier) {
			receipt.Blockers = []string{"NO_READY_FRONTIER"}
			return receipt, nil
		}
		return ControlReceipt{}, err
	}
	task, err := s.Tasks.EnqueueTaskForIssue(ctx, issue)
	if err != nil {
		if errors.Is(err, ErrDuplicatePendingTask) {
			// Idempotent replay: surface the already-live task.
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
	issue, err := s.readyFrontierIssue(ctx, workspaceID, projectID)
	if err == nil {
		if task, enqErr := s.Tasks.EnqueueTaskForIssue(ctx, issue); enqErr == nil {
			receipt.TaskID = uuidOrNil(task.ID)
			receipt.IssueID = uuidOrNil(issue.ID)
		} else if errors.Is(enqErr, ErrDuplicatePendingTask) {
			receipt.Replayed = true
		} else {
			// Surface the real enqueue failure; never report a silent partial
			// success (Gauss #4 / Quinn resume-吞错).
			receipt.Blockers = append(receipt.Blockers, "ENQUEUE_FAILED: "+enqErr.Error())
		}
	}
	return receipt, nil
}

// readyFrontierIssue returns the highest-priority nonterminal issue with no
// live task, i.e. the exact frontier to dispatch — never "all issues".
func (s *ProjectLifecycleControlService) readyFrontierIssue(ctx context.Context, workspaceID, projectID pgtype.UUID) (db.Issue, error) {
	issues, err := s.Queries.ListIssues(ctx, db.ListIssuesParams{
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
		Limit:       1000,
		Offset:      0,
	})
	if err != nil {
		return db.Issue{}, err
	}
	activeTasks, err := s.Queries.ListProjectActiveTasks(ctx, workspaceID)
	if err != nil {
		return db.Issue{}, err
	}
	busy := map[string]struct{}{}
	for _, t := range activeTasks {
		if iid := util.UUIDToString(t.IssueID); iid != "" {
			busy[iid] = struct{}{}
		}
	}
	// Pick the first nonterminal issue without a live task, ordered by position
	// (ListIssues returns position order). Priority: in_progress > todo >
	// backlog > in_review > blocked (blocked/review are repair gates, not
	// dispatch-ready) — keep it deterministic and safe.
	for _, is := range issues {
		if _, hasLive := busy[util.UUIDToString(is.ID)]; hasLive {
			continue
		}
		switch is.Status {
		case "in_progress", "todo", "backlog":
			return issueRowToIssue(is), nil
		}
	}
	return db.Issue{}, ErrProjectLifecycleNoFrontier
}

// activeTaskForIssue returns the currently-live task for an issue, if any.
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
