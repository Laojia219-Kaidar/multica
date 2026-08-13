package handler

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// pipeline.go — read-only project pipeline projection (HIV-367 / P0-E).
//
// This file implements GET /api/projects/{id}/pipeline: the single read-only
// projection endpoint that backs the Owner project-page status columns. It
// composes existing Project + Issue + agent_task_queue (the canonical Task/Run
// row) + comment data — no new writer, no schema change, no second pipeline
// status table (contract §1, §7).
//
// Workspace scoping is exact: the query is filtered by issue.workspace_id and
// issue.project_id at the SQL layer (see ListProjectPipelineRows), and the
// handler additionally resolves + validates the project against
// GetProjectInWorkspace before running the projection so a cross-workspace
// project UUID returns 404, never an enumeration oracle (§7, mirroring
// loadIssueForUser / GetProject).

// Non-terminal issue statuses that participate in the pipeline projection.
// done / cancelled are excluded everywhere (mirrors ListOpenIssues).
var pipelineIssueStatuses = []string{"backlog", "todo", "in_progress", "in_review", "blocked"}

// pipelineTaskStatusOrder is the canonical classification of the latest task
// row attached to an issue, used to size column headers and to mark cards.
// Values are stable wire strings the frontend can localize.
const (
	pipelineTaskClassRunning  = "running"                 // dispatched | running — a runtime has claimed the task
	pipelineTaskClassQueued   = "queued"                  // queued — not yet claimed by a runtime; distinct from running
	pipelineTaskClassWaiting  = "waiting_local_directory" // surfaced standalone: long waits are a health signal
	pipelineTaskClassFailed   = "failed"                  // last task failed (retry may or may not follow)
	pipelineTaskClassTerminal = "terminal"                // completed | cancelled — may or may not have written back a verdict
	pipelineTaskClassNoTask   = "no_task"                 // no agent_task_queue row at all
	pipelineTaskClassUnknown  = "unknown"                 // honest fallback — never silently coerce
)

// ProjectPipelineResponse is the wire shape for GET /api/projects/{id}/pipeline.
// Columns are keyed by issue status (backlog/todo/in_progress/in_review/blocked);
// each column carries both human totals and per-task-class breakdowns so a UI
// can render the contract's required "total / running / queued / waiting /
// failed / terminal-no-writeback / no-task" header (§2) in one read. Issues
// are returned per status so the board can attach the latest task + receipt
// signals to each card (§3, §4) without a second round-trip per card.
type ProjectPipelineResponse struct {
	ProjectID string                           `json:"project_id"`
	Status    string                           `json:"project_status"`
	Title     string                           `json:"project_title"`
	UpdatedAt string                           `json:"updated_at"`
	Columns   map[string]ProjectPipelineColumn `json:"columns"`
	// IssuesByStatus lets a client locate a card's pipeline payload without
	// re-querying: keys are issue ids, values are the per-issue pipeline row.
	// NOTE: this is intentionally a flat map, not a per-status array, so the
	// frontend's existing issue cache (keyed by issue id) can join directly.
	Issues map[string]ProjectPipelineIssue `json:"issues"`
	// CapabilityFlags tells the frontend which canonical actions are wired
	// (§6): any action whose server-side command is absent must render
	// "能力待接入" rather than a fake local mutation. Updated as siblings
	// (HIV-355 dispatch-preview/dispatch, HIV-357 project wave start) land.
	CapabilityFlags ProjectPipelineCapabilityFlags `json:"capability_flags"`
}

// ProjectPipelineColumn is the per-status header payload (§2).
type ProjectPipelineColumn struct {
	Status              string `json:"status"`
	Total               int64  `json:"total"`
	Running             int64  `json:"running"`
	Queued              int64  `json:"queued"`
	Waiting             int64  `json:"waiting"`
	Failed              int64  `json:"failed"`
	Terminal            int64  `json:"terminal"`
	TerminalNoWriteback int64  `json:"terminal_no_writeback"`
	NoTask              int64  `json:"no_task"`
	Unknown             int64  `json:"unknown"`
}

// ProjectPipelineIssue is the per-card payload (§3, §4). Every time- field is
// a string RFC3339 or nil. TaskID is "" when there is no task row at all.
type ProjectPipelineIssue struct {
	IssueID                     string  `json:"issue_id"`
	Status                      string  `json:"status"`
	Priority                    string  `json:"priority"`
	Title                       string  `json:"title"`
	AssigneeType                string  `json:"assignee_type,omitempty"`
	AssigneeID                  string  `json:"assignee_id,omitempty"`
	UpdatedAt                   string  `json:"updated_at"`
	TaskID                      string  `json:"task_id,omitempty"`
	TaskStatus                  string  `json:"task_status,omitempty"`
	TaskClass                   string  `json:"task_class"`
	TaskDispatchedAt            *string `json:"task_dispatched_at,omitempty"`
	TaskStartedAt               *string `json:"task_started_at,omitempty"`
	TaskCompletedAt             *string `json:"task_completed_at,omitempty"`
	TaskDurationMs              int64   `json:"task_duration_ms,omitempty"`
	FailureReason               string  `json:"failure_reason,omitempty"`
	WaitReason                  string  `json:"wait_reason,omitempty"`
	LatestReceiptCommentID      string  `json:"latest_receipt_comment_id,omitempty"`
	LatestReceiptCommentAt      *string `json:"latest_receipt_comment_at,omitempty"`
	LatestReceiptCommentSnippet string  `json:"latest_receipt_comment_snippet,omitempty"`
	// NextSystemAction is a stable, client-localizable label describing the
	// next canonical action the system will take (or "none" when the issue is
	// healthy). Empty when unknown — never silently fabricate.
	NextSystemAction string `json:"next_system_action,omitempty"`
	// ProcessingState is the explicit per-card marker required by §4:
	//   - in_progress + no open Task -> "stale_awaiting_dispatch"
	//   - in_review + no Review Task -> "review_not_started"
	//   - blocked + no recovery Task -> "blocked_unhandled"
	//   - otherwise: "active" / "terminal_pending_writeback" / "unknown"
	ProcessingState string `json:"processing_state"`
}

// ProjectPipelineCapabilityFlags surfaces which canonical actions the server
// can actually perform RIGHT NOW. Frontend uses these to gate buttons: when
// false, the UI renders "能力待接入" (§6) instead of a fake local action.
// Today all write actions on this surface are read-only composition; the
// underlying commands (dispatch-preview/dispatch per HIV-355, project wave
// start per HIV-357) live in sibling worktrees and are deliberately NOT
// assumed available here.
type ProjectPipelineCapabilityFlags struct {
	CancelTask      bool `json:"cancel_task"`      // POST /api/issues/{id}/tasks/{taskId}/cancel — exists
	RerunIssue      bool `json:"rerun_issue"`      // POST /api/issues/{id}/rerun — exists
	UpdateStatus    bool `json:"update_status"`    // PUT /api/issues/{id} (status/assignee) — exists
	DispatchPreview bool `json:"dispatch_preview"` // HIV-355 — NOT yet merged in this mainline
	Dispatch        bool `json:"dispatch"`         // HIV-355 — NOT yet merged in this mainline
	ProjectStart    bool `json:"project_start"`    // HIV-357 — NOT yet merged in this mainline
}

// receiptSnippetLimit trims the latest task-linked comment body so the
// projection payload stays bounded even on verbose agent verdicts.
const receiptSnippetLimit = 280

// GetProjectPipeline returns the read-only pipeline projection for a project.
//
// Route: GET /api/projects/{id}/pipeline
// Auth: workspace member (read access); write actions gate on Owner/admin at
// the action endpoint, not here.
// Cache: private, no-store — the projection composes live task state and must
// not be cached across users or served stale from an intermediary.
func (h *Handler) GetProjectPipeline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Cache-Control", "private, no-store")

	projectID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	projectUUID, ok := parseUUIDOrBadRequest(w, projectID, "project id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	// Workspace-exact project lookup — a cross-workspace project UUID returns
	// 404, matching GetProject / loadIssueForUser behavior.
	project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
		ID: projectUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	rows, err := h.Queries.ListProjectPipelineRows(ctx, db.ListProjectPipelineRowsParams{
		WorkspaceID: wsUUID,
		ProjectID:   projectUUID,
	})
	if err != nil {
		slog.Warn("pipeline projection query failed", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load pipeline")
		return
	}

	columns := make(map[string]ProjectPipelineColumn, len(pipelineIssueStatuses))
	for _, s := range pipelineIssueStatuses {
		columns[s] = ProjectPipelineColumn{Status: s}
	}
	issues := make(map[string]ProjectPipelineIssue, len(rows))

	for _, row := range rows {
		issueID := uuidToString(row.IssueID)
		taskClass, durationMs := classifyPipelineTask(row, wsUUID)
		processingState := computeProcessingState(row.IssueStatus, taskClass)
		nextAction := computeNextSystemAction(row.IssueStatus, taskClass)

		col := columns[row.IssueStatus]
		col.Total++
		bumpColumnTaskClass(&col, taskClass)
		// terminal_no_writeback: terminal task with no task-linked comment.
		if taskClass == pipelineTaskClassTerminal && !row.LatestReceiptCommentID.Valid {
			col.TerminalNoWriteback++
		}
		columns[row.IssueStatus] = col

		issue := ProjectPipelineIssue{
			IssueID:          issueID,
			Status:           row.IssueStatus,
			Priority:         row.IssuePriority,
			Title:            row.IssueTitle,
			UpdatedAt:        timestampToString(row.IssueUpdatedAt),
			TaskClass:        taskClass,
			TaskDurationMs:   durationMs,
			ProcessingState:  processingState,
			NextSystemAction: nextAction,
		}
		if row.IssueAssigneeType.Valid {
			issue.AssigneeType = row.IssueAssigneeType.String
		}
		if row.IssueAssigneeID.Valid {
			issue.AssigneeID = uuidToString(row.IssueAssigneeID)
		}
		if row.TaskID.Valid {
			issue.TaskID = uuidToString(row.TaskID)
			issue.TaskStatus = row.TaskStatus
			issue.TaskDispatchedAt = timestampToPtr(row.TaskDispatchedAt)
			issue.TaskStartedAt = timestampToPtr(row.TaskStartedAt)
			issue.TaskCompletedAt = timestampToPtr(row.TaskCompletedAt)
		}
		if row.TaskFailureReason.Valid && row.TaskFailureReason.String != "" {
			issue.FailureReason = row.TaskFailureReason.String
		}
		if row.TaskWaitReason.Valid && row.TaskWaitReason.String != "" {
			issue.WaitReason = row.TaskWaitReason.String
		}
		if row.LatestReceiptCommentID.Valid {
			issue.LatestReceiptCommentID = uuidToString(row.LatestReceiptCommentID)
			issue.LatestReceiptCommentAt = timestampToPtr(row.LatestReceiptCommentAt)
			issue.LatestReceiptCommentSnippet = truncateForSnippet(row.LatestReceiptCommentContent)
		}
		issues[issueID] = issue
	}

	resp := ProjectPipelineResponse{
		ProjectID: uuidToString(project.ID),
		Status:    project.Status,
		Title:     project.Title,
		UpdatedAt: timestampToString(project.UpdatedAt),
		Columns:   columns,
		Issues:    issues,
		CapabilityFlags: ProjectPipelineCapabilityFlags{
			// Existing canonical commands — wired today.
			CancelTask:   true,
			RerunIssue:   true,
			UpdateStatus: true,
			// Sibling commands not yet merged into this mainline — fail honest.
			DispatchPreview: false,
			Dispatch:        false,
			ProjectStart:    false,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// classifyPipelineTask maps a pipeline row's latest task to a stable class
// string and returns the run duration in milliseconds (0 when not derivable).
// Duration mirrors the frontend computation: running uses (now - started_at);
// terminal uses (completed_at - started_at); otherwise 0. Server-time is
// acceptable here because the value is only used for display heuristics.
func classifyPipelineTask(row db.ListProjectPipelineRowsRow, wsUUID pgtype.UUID) (string, int64) {
	if !row.TaskID.Valid {
		return pipelineTaskClassNoTask, 0
	}
	switch row.TaskStatus {
	case "queued":
		return pipelineTaskClassQueued, computeDurationMs(row.TaskStartedAt, row.TaskCompletedAt)
	case "dispatched", "running":
		return pipelineTaskClassRunning, computeDurationMs(row.TaskStartedAt, row.TaskCompletedAt)
	case "waiting_local_directory":
		return pipelineTaskClassWaiting, computeDurationMs(row.TaskStartedAt, row.TaskCompletedAt)
	case "failed":
		return pipelineTaskClassFailed, computeDurationMs(row.TaskStartedAt, row.TaskCompletedAt)
	case "completed", "cancelled":
		return pipelineTaskClassTerminal, computeDurationMs(row.TaskStartedAt, row.TaskCompletedAt)
	default:
		return pipelineTaskClassUnknown, 0
	}
}

// computeDurationMs returns completed-started when both are present; for an
// in-flight task (started set, completed missing), the caller should treat 0
// as "duration not yet meaningful" and the frontend computes a live ticking
// value from started_at. We intentionally do not call time.Now() server-side
// for in-flight rows: that would make responses non-reproducible across the
// 5s polling window and complicate 304-style caching later.
func computeDurationMs(started, completed pgtype.Timestamptz) int64 {
	if !started.Valid || !completed.Valid {
		return 0
	}
	delta := completed.Time.Sub(started.Time).Milliseconds()
	if delta < 0 {
		return 0
	}
	return delta
}

// bumpColumnTaskClass increments the right counter on the column struct.
// Unknown statuses fall through to col.Unknown so the projection stays
// accurate even if a new task status lands before this code is updated.
func bumpColumnTaskClass(col *ProjectPipelineColumn, class string) {
	switch class {
	case pipelineTaskClassRunning:
		col.Running++
	case pipelineTaskClassQueued:
		col.Queued++
	case pipelineTaskClassWaiting:
		col.Waiting++
	case pipelineTaskClassFailed:
		col.Failed++
	case pipelineTaskClassTerminal:
		col.Terminal++
	case pipelineTaskClassNoTask:
		col.NoTask++
	default:
		col.Unknown++
	}
}

// computeProcessingState implements the explicit per-card marker contract §4.
// These labels are stable wire strings; the frontend localizes them.
func computeProcessingState(issueStatus, taskClass string) string {
	switch issueStatus {
	case "in_progress":
		if taskClass == pipelineTaskClassNoTask {
			return "stale_awaiting_dispatch"
		}
		if taskClass == pipelineTaskClassFailed || taskClass == pipelineTaskClassTerminal {
			return "active" // last run finished; the issue itself is still in_progress
		}
		return "active"
	case "in_review":
		if taskClass == pipelineTaskClassNoTask {
			return "review_not_started"
		}
		return "active"
	case "blocked":
		if taskClass == pipelineTaskClassNoTask {
			return "blocked_unhandled"
		}
		return "active"
	case "backlog", "todo":
		return "active"
	default:
		return "unknown"
	}
}

// computeNextSystemAction returns a stable, client-localizable action label.
// Empty (omitted on the wire) when there is no canonical next action — never
// silently fabricate one. Today these are deterministic labels derived from
// (issue_status, task_class); when sibling commands land they can be enriched
// with preview URLs.
func computeNextSystemAction(issueStatus, taskClass string) string {
	switch {
	case issueStatus == "in_progress" && taskClass == pipelineTaskClassNoTask:
		return "dispatch"
	case issueStatus == "in_review" && taskClass == pipelineTaskClassNoTask:
		return "open_review_task"
	case issueStatus == "blocked" && taskClass == pipelineTaskClassNoTask:
		return "dispatch_recovery"
	case taskClass == pipelineTaskClassFailed:
		return "retry_or_diagnose"
	case taskClass == pipelineTaskClassWaiting:
		return "resolve_wait"
	default:
		return ""
	}
}

// truncateForSnippet clips a comment body to receiptSnippetLimit unicode
// runes. Pure display heuristic; the full body stays on the comment endpoint.
func truncateForSnippet(s string) string {
	if len([]rune(s)) <= receiptSnippetLimit {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:receiptSnippetLimit])) + "…"
}
