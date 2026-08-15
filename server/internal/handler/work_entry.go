package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/workentry"
)

// workEntryErrorResponse mirrors the company-ops error shape: a stable
// machine-readable reason_code plus a human message.
type workEntryErrorResponse struct {
	Error      string `json:"error"`
	ReasonCode string `json:"reason_code"`
}

func writeWorkEntryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workentry.ErrConflict):
		writeJSON(w, http.StatusConflict, workEntryErrorResponse{Error: err.Error(), ReasonCode: "conflict"})
	case errors.Is(err, workentry.ErrClassificationRequired):
		writeJSON(w, http.StatusConflict, workEntryErrorResponse{Error: err.Error(), ReasonCode: "classification_required"})
	case errors.Is(err, workentry.ErrInvalidRequest):
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: err.Error(), ReasonCode: "invalid_request"})
	case errors.Is(err, workentry.ErrNotFound):
		writeJSON(w, http.StatusNotFound, workEntryErrorResponse{Error: err.Error(), ReasonCode: "not_found"})
	case errors.Is(err, workentry.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, workEntryErrorResponse{Error: err.Error(), ReasonCode: "writer_unavailable"})
	default:
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: err.Error(), ReasonCode: "invalid_request"})
	}
}

// requireWorkEntry fails closed with 503 when the kernel service is unwired.
func (h *Handler) requireWorkEntry(w http.ResponseWriter) bool {
	if h.WorkEntry == nil {
		writeWorkEntryError(w, workentry.ErrUnavailable)
		return false
	}
	return true
}

// scopeActorIdentity pins the body workspace_id to the middleware-resolved
// tenant (fail-closed tenant isolation) and fills it when absent.
func (h *Handler) scopeWorkspace(w http.ResponseWriter, r *http.Request, bodyWorkspaceID *string) bool {
	resolved := h.resolveWorkspaceID(r)
	if strings.TrimSpace(resolved) == "" {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "workspace_id is required", ReasonCode: "invalid_request"})
		return false
	}
	if strings.TrimSpace(*bodyWorkspaceID) == "" {
		*bodyWorkspaceID = resolved
		return true
	}
	if *bodyWorkspaceID != resolved {
		writeJSON(w, http.StatusForbidden, workEntryErrorResponse{Error: "workspace_id does not match the authenticated tenant", ReasonCode: "forbidden"})
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// resolve / register
// ---------------------------------------------------------------------------

type workEntryResolveRequest struct {
	Actor     workentry.WorkActorIdentityV1 `json:"actor_identity"`
	Intent    workentry.WorkIntentV1        `json:"intent"`
	ProjectID string                        `json:"project_id,omitempty"`
	IssueID   string                        `json:"issue_id,omitempty"`
}

// WorkEntryResolve handles POST /api/work/resolve (read-only).
func (h *Handler) WorkEntryResolve(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workEntryResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	if !h.scopeWorkspace(w, r, &req.Actor.WorkspaceID) {
		return
	}
	res, err := h.WorkEntry.ResolvePreview(r.Context(), workentry.ResolveRequest{
		Actor: req.Actor, Intent: req.Intent, ProjectID: req.ProjectID, IssueID: req.IssueID,
	})
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type workEntryRegisterRequest struct {
	Actor         workentry.WorkActorIdentityV1 `json:"actor_identity"`
	Intent        workentry.WorkIntentV1        `json:"intent"`
	ProjectID     string                        `json:"project_id,omitempty"`
	IssueID       string                        `json:"issue_id,omitempty"`
	ConfirmCreate bool                          `json:"confirm_create"`
}

// WorkEntryRegister handles POST /api/work/register (idempotent).
func (h *Handler) WorkEntryRegister(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workEntryRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	if !h.scopeWorkspace(w, r, &req.Actor.WorkspaceID) {
		return
	}
	receipt, err := h.WorkEntry.Register(r.Context(), workentry.RegisterRequest{
		ResolveRequest: workentry.ResolveRequest{Actor: req.Actor, Intent: req.Intent, ProjectID: req.ProjectID, IssueID: req.IssueID},
		ConfirmCreate:  req.ConfirmCreate,
	})
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

// ---------------------------------------------------------------------------
// start / status / heartbeat / event
// ---------------------------------------------------------------------------

type workEntryStartRequest struct {
	WorkRef     string `json:"work_ref"`
	SessionID   string `json:"session_id"`
	RunID       string `json:"run_id"`
	ActorID     string `json:"actor_id"`
	WorkspaceID string `json:"workspace_id"`
}

// WorkEntryStart handles POST /api/work/start.
func (h *Handler) WorkEntryStart(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workEntryStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	if !h.scopeWorkspace(w, r, &req.WorkspaceID) {
		return
	}
	res, err := h.WorkEntry.Start(r.Context(), workentry.StartRequest(req))
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryStatus handles GET /api/work/status?work_ref=... (read-only).
func (h *Handler) WorkEntryStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	res, err := h.WorkEntry.Status(r.Context(), workentry.StatusRequest{
		WorkRef:     r.URL.Query().Get("work_ref"),
		WorkspaceID: h.resolveWorkspaceID(r),
	})
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryHeartbeat handles POST /api/work/heartbeat.
func (h *Handler) WorkEntryHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workentry.HeartbeatRecord
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	if !h.scopeWorkspace(w, r, &req.WorkspaceID) {
		return
	}
	res, err := h.WorkEntry.Heartbeat(r.Context(), req)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryEvent handles POST /api/work/event (append-only, idempotent).
func (h *Handler) WorkEntryEvent(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var event workentry.WorkEventV1
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	res, err := h.WorkEntry.Event(r.Context(), event)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------------------
// handoff / finish
// ---------------------------------------------------------------------------

// WorkEntryHandoff handles POST /api/work/handoff (candidate + evidence).
func (h *Handler) WorkEntryHandoff(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var pkg workentry.WorkHandoffV1
	if err := json.NewDecoder(r.Body).Decode(&pkg); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	res, err := h.WorkEntry.Handoff(r.Context(), pkg)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryFinish handles POST /api/work/finish (routes to review).
func (h *Handler) WorkEntryFinish(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var c workentry.WorkCompletionV1
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	res, err := h.WorkEntry.Finish(r.Context(), c)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------------------
// reconcile / attach / ignore / replay / sync
// ---------------------------------------------------------------------------

type workEntrySyncRequest struct {
	Entries []workentry.SyncEntry `json:"entries"`
}

// WorkEntrySync handles POST /api/work/sync (offline spool replay).
func (h *Handler) WorkEntrySync(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workEntrySyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	res, err := h.WorkEntry.Sync(r.Context(), req.Entries)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryReconcile handles GET /api/work/reconcile (read-only diagnostic).
func (h *Handler) WorkEntryReconcile(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	res, err := h.WorkEntry.Reconcile(r.Context(), h.resolveWorkspaceID(r))
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	if res == nil {
		res = []workentry.InboxItem{}
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryAttach handles POST /api/work/attach.
func (h *Handler) WorkEntryAttach(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workentry.AttachRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	if !h.scopeWorkspace(w, r, &req.WorkspaceID) {
		return
	}
	res, err := h.WorkEntry.Attach(r.Context(), req)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryIgnore handles POST /api/work/ignore.
func (h *Handler) WorkEntryIgnore(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workentry.IgnoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return
	}
	if !h.scopeWorkspace(w, r, &req.WorkspaceID) {
		return
	}
	res, err := h.WorkEntry.Ignore(r.Context(), req)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryReplay handles GET /api/work/replay?key=...&kind=...&work_ref=...
func (h *Handler) WorkEntryReplay(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	res, err := h.WorkEntry.Replay(r.Context(), workentry.ReplayRequest{
		WorkspaceID:    h.resolveWorkspaceID(r),
		IdempotencyKey: r.URL.Query().Get("key"),
		Kind:           r.URL.Query().Get("kind"),
		WorkRef:        r.URL.Query().Get("work_ref"),
	})
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
