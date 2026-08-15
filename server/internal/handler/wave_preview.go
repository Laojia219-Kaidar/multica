package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/wavescheduler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// wave_preview.go — preview-only project wave decomposition (HIV-403).
//
// GET /api/projects/{id}/wave-preview
//
// Returns the wave decomposition of a project's open Issues, using the
// existing issue_dependency table for edges. Read-only: no Task creation,
// no status mutation, no dispatch. The response includes idempotency and
// mutex keys so a future dispatch command can consume them verbatim.

func (h *Handler) GetProjectWavePreview(w http.ResponseWriter, r *http.Request) {
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

	// Workspace-exact project lookup.
	_, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
		ID: projectUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Load open issues for the project via the existing pipeline query.
	rows, err := h.Queries.ListProjectPipelineRows(ctx, db.ListProjectPipelineRowsParams{
		WorkspaceID: wsUUID,
		ProjectID:   projectUUID,
	})
	if err != nil {
		slog.Warn("wave preview: pipeline query failed", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load project issues")
		return
	}

	// Deduplicate issues (pipeline query may return one row per task).
	type issueEntry struct {
		id       string
		title    string
		status   string
		priority string
	}
	seen := make(map[string]bool)
	var issues []issueEntry
	for _, row := range rows {
		id := uuidToString(row.IssueID)
		if seen[id] {
			continue
		}
		seen[id] = true
		issues = append(issues, issueEntry{
			id:       id,
			title:    row.IssueTitle,
			status:   row.IssueStatus,
			priority: row.IssuePriority,
		})
	}

	// Load dependencies for these issues via raw SQL (no sqlc query yet).
	// Fetch rows in both directions: lines where one of our issues is the
	// dependency OR the target. Without the OR branch, a cross-project row
	// (e.g. a foreign blocker in a "blocks" edge pointing at one of our
	// issues) would be invisible and our issue would be misjudged as ready —
	// the scheduler turns such rows into explicit missing dependencies.
	type depRow struct {
		issueID     string
		dependsOnID string
		depType     string
	}
	var deps []depRow
	if len(issues) > 0 {
		issueUUIDs := make([]string, len(issues))
		for i, iss := range issues {
			issueUUIDs[i] = iss.id
		}
		depRows, err := h.DB.Query(ctx, `
			SELECT issue_id, depends_on_issue_id, type
			FROM issue_dependency
			WHERE issue_id = ANY($1) OR depends_on_issue_id = ANY($1)
		`, issueUUIDs)
		if err != nil {
			slog.Warn("wave preview: dependency query failed", "project_id", projectID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load dependencies")
			return
		}
		for depRows.Next() {
			var d depRow
			if err := depRows.Scan(&d.issueID, &d.dependsOnID, &d.depType); err != nil {
				depRows.Close()
				slog.Warn("wave preview: dependency scan failed", "error", err)
				writeError(w, http.StatusInternalServerError, "failed to read dependencies")
				return
			}
			deps = append(deps, d)
		}
		depRows.Close()
	}

	// Build scheduler input.
	wsIssues := make([]wavescheduler.Issue, 0, len(issues))
	for _, iss := range issues {
		wsIssues = append(wsIssues, wavescheduler.Issue{
			ID:       iss.id,
			Title:    iss.title,
			Status:   iss.status,
			Priority: iss.priority,
		})
	}

	wsDeps := make([]wavescheduler.Dependency, 0, len(deps))
	for _, d := range deps {
		wsDeps = append(wsDeps, wavescheduler.Dependency{
			IssueID:     d.issueID,
			DependsOnID: d.dependsOnID,
			Type:        d.depType,
		})
	}

	input := wavescheduler.ScheduleInput{
		ProjectID:    uuidToString(projectUUID),
		Issues:       wsIssues,
		Dependencies: wsDeps,
		Now:          time.Now().UTC(),
	}

	result := wavescheduler.Schedule(input)
	writeJSON(w, http.StatusOK, result)
}
