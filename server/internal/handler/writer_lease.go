package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type writerLeaseRequest struct {
	ResourceID      string    `json:"resource_id"`
	LeaseToken      uuid.UUID `json:"lease_token"`
	FenceGeneration int64     `json:"fence_generation"`
	TTLMS           int64     `json:"ttl_ms"`
}

// WriterLease handles acquire/renew/verify/release for one claimed task. The
// URL is relationship-scoped; key and holder are intentionally absent from
// the request schema and are recomputed from authenticated server state.
func (h *Handler) WriterLease(w http.ResponseWriter, r *http.Request) {
	if h.WriterLeaseService == nil {
		writeError(w, http.StatusServiceUnavailable, "writer lease unavailable")
		return
	}
	runtimeID, taskID := chi.URLParam(r, "runtimeId"), chi.URLParam(r, "taskId")
	runtime, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	daemonID := strings.TrimSpace(middleware.DaemonIDFromContext(r.Context()))
	if daemonID == "" {
		writeError(w, http.StatusForbidden, "daemon identity required")
		return
	}
	if !writerLeaseDaemonMatchesRuntime(runtime, daemonID) {
		writeError(w, http.StatusForbidden, "daemon identity does not match runtime")
		return
	}
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok || uuidToString(task.RuntimeID) != runtimeID || workspaceID != uuidToString(runtime.WorkspaceID) {
		if ok {
			writeError(w, http.StatusNotFound, "task not found")
		}
		return
	}
	action := chi.URLParam(r, "action")
	if !writerLeaseActionAllowed(action, task.Status) {
		writeError(w, http.StatusConflict, "writer lease operation unavailable")
		return
	}
	req, err := decodeWriterLeaseRequest(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateWriterLeaseRequest(action, req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid writer lease request")
		return
	}
	resourceID, err := util.ParseUUID(strings.TrimSpace(req.ResourceID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "resource_id is required")
		return
	}
	resource, err := h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{ID: resourceID, WorkspaceID: runtime.WorkspaceID})
	if err != nil || resource.ResourceType != "github_repo" {
		writeError(w, http.StatusNotFound, "writer lease target not found")
		return
	}
	projectID, err := taskProjectID(r.Context(), h.Queries, task)
	if err != nil || !projectID.Valid || projectID != resource.ProjectID {
		writeError(w, http.StatusNotFound, "writer lease target not found")
		return
	}
	var ref struct {
		URL               string `json:"url"`
		Ref               string `json:"ref"`
		DefaultBranchHint string `json:"default_branch_hint"`
	}
	if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil || strings.TrimSpace(ref.URL) == "" {
		writeError(w, http.StatusConflict, "writer lease target unavailable")
		return
	}
	targets, err := service.ResolveWriterLeaseTargets(service.WriterLeaseModeEnforce, uuidToString(runtime.WorkspaceID), uuidToString(projectID), daemonID, runtimeID, taskID, []service.WriterLeaseResource{{ID: uuid.UUID(resource.ID.Bytes), ResourceType: resource.ResourceType, URL: ref.URL, Ref: ref.Ref, DefaultBranchHint: ref.DefaultBranchHint}})
	if err != nil || len(targets) != 1 {
		writeError(w, http.StatusConflict, "writer lease target unavailable")
		return
	}
	target := targets[0]
	holder := service.WriterLeaseHolderID(daemonID, runtimeID, taskID)
	ttl := time.Duration(req.TTLMS) * time.Millisecond
	if ttl == 0 && (action == "acquire" || action == "renew") {
		ttl = service.DefaultLeaseDuration
	}
	var lease *service.WriteLease
	switch action {
	case "acquire":
		lease, err = h.WriterLeaseService.Acquire(r.Context(), target.MutexKey, holder, ttl)
	case "renew":
		lease, err = h.WriterLeaseService.Renew(r.Context(), target.MutexKey, req.LeaseToken, req.FenceGeneration, ttl)
	case "verify":
		lease, err = h.WriterLeaseService.VerifyHeld(r.Context(), target.MutexKey, req.LeaseToken, req.FenceGeneration)
	case "release":
		lease, err = h.WriterLeaseService.Release(r.Context(), target.MutexKey, req.LeaseToken, req.FenceGeneration)
	default:
		writeError(w, http.StatusNotFound, "writer lease operation not found")
		return
	}
	if err != nil {
		status := http.StatusConflict
		if !errors.Is(err, service.ErrLeaseBusy) && !errors.Is(err, service.ErrLeaseNotHeld) {
			status = http.StatusInternalServerError
		}
		writeError(w, status, "writer lease operation failed")
		return
	}
	writeMeasuredJSON(w, http.StatusOK, map[string]any{"lease": lease})
}

func decodeWriterLeaseRequest(body io.Reader) (writerLeaseRequest, error) {
	var req writerLeaseRequest
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return writerLeaseRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return writerLeaseRequest{}, errors.New("trailing request data")
	}
	return req, nil
}

// writerLeaseDaemonMatchesRuntime binds lease operations to the authenticated
// machine. Unlike legacy claim fallback, enforce-mode lease transport requires
// a stored daemon_id and fails closed for NULL runtime ownership.
func writerLeaseDaemonMatchesRuntime(runtime db.AgentRuntime, daemonID string) bool {
	return daemonID != "" && runtime.DaemonID.Valid && runtime.DaemonID.String == daemonID
}

func writerLeaseTaskRuntimeMatchesDaemon(task db.AgentTaskQueue, runtime db.AgentRuntime, daemonID string) bool {
	return task.RuntimeID.Valid && runtime.ID == task.RuntimeID && writerLeaseDaemonMatchesRuntime(runtime, daemonID)
}

func validateWriterLeaseRequest(action string, req writerLeaseRequest) error {
	switch action {
	case "acquire":
		if req.LeaseToken != uuid.Nil || req.FenceGeneration != 0 {
			return errors.New("acquire does not accept lease credentials")
		}
		return validateWriterLeaseTTL(req.TTLMS)
	case "renew":
		if req.LeaseToken == uuid.Nil || req.FenceGeneration <= 0 {
			return errors.New("renew requires lease credentials")
		}
		return validateWriterLeaseTTL(req.TTLMS)
	case "verify", "release":
		if req.LeaseToken == uuid.Nil || req.FenceGeneration <= 0 || req.TTLMS != 0 {
			return errors.New("operation requires lease credentials and no ttl")
		}
		return nil
	default:
		return errors.New("unknown lease operation")
	}
}

func validateWriterLeaseTTL(ttlMS int64) error {
	if ttlMS == 0 {
		return nil
	}
	if ttlMS < int64(time.Second/time.Millisecond) || ttlMS > service.DefaultLeaseDuration.Milliseconds() {
		return errors.New("lease ttl outside safe range")
	}
	return nil
}

func writerLeaseActionAllowed(action, status string) bool {
	switch action {
	case "acquire":
		return status == "dispatched" || status == "waiting_local_directory"
	case "renew", "verify":
		return status == "dispatched" || status == "waiting_local_directory" || status == "running"
	case "release":
		// Release is used both for terminal cleanup and for best-effort cleanup
		// after a heartbeat/acquire path is interrupted while still active.
		return status == "queued" || status == "dispatched" || status == "waiting_local_directory" || status == "running" || status == "completed" || status == "failed" || status == "cancelled"
	default:
		return false
	}
}

func taskProjectID(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (pgtype.UUID, error) {
	if task.IssueID.Valid {
		issue, err := queries.GetIssue(ctx, task.IssueID)
		if err != nil {
			return pgtype.UUID{}, err
		}
		return issue.ProjectID, nil
	}
	if task.ChatSessionID.Valid {
		chat, err := queries.GetChatSession(ctx, task.ChatSessionID)
		if err != nil {
			return pgtype.UUID{}, err
		}
		return chat.ProjectID, nil
	}
	if projectID, ok, err := quickCreateProjectID(task.Context); ok {
		return projectID, err
	}
	return pgtype.UUID{}, errors.New("task has no project")
}

func quickCreateProjectID(raw []byte) (pgtype.UUID, bool, error) {
	if len(raw) == 0 {
		return pgtype.UUID{}, false, nil
	}
	var quickCreate service.QuickCreateContext
	if err := json.Unmarshal(raw, &quickCreate); err != nil || quickCreate.Type != service.QuickCreateContextType || strings.TrimSpace(quickCreate.ProjectID) == "" {
		return pgtype.UUID{}, false, nil
	}
	projectID, err := util.ParseUUID(strings.TrimSpace(quickCreate.ProjectID))
	return projectID, true, err
}
