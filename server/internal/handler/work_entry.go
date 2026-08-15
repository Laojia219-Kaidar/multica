package handler

import (
	"encoding/json"
	"errors"
	"io"
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
	case errors.Is(err, workentry.ErrForbiddenProofField):
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: err.Error(), ReasonCode: "forbidden_proof_field"})
	default:
		// Internal/DB errors must not be misreported as a client 400.
		writeJSON(w, http.StatusInternalServerError, workEntryErrorResponse{Error: err.Error(), ReasonCode: "internal_error"})
	}
}

// decodeWorkRequest reads the request body, rejects caller-supplied proof
// fields (fail-closed, §8.1), then decodes into dst. Returns false when a
// response has already been written.
func decodeWorkRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "read body", ReasonCode: "invalid_request"})
		return false
	}
	if err := workentry.RejectForbiddenProofFields(body); err != nil {
		writeWorkEntryError(w, err)
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeJSON(w, http.StatusBadRequest, workEntryErrorResponse{Error: "invalid JSON body", ReasonCode: "invalid_request"})
		return false
	}
	return true
}

// requireWorkRefTenant fails closed (403) when the workspace embedded in a
// caller-supplied work_ref does not match the authenticated workspace member
// tenant. Caller-supplied work_ref is never authority for tenant isolation.
func (h *Handler) requireWorkRefTenant(w http.ResponseWriter, r *http.Request, workRef string) bool {
	ws := workentry.WorkspaceFromWorkRef(workRef)
	if ws == "" || ws != h.resolveWorkspaceID(r) {
		writeJSON(w, http.StatusForbidden, workEntryErrorResponse{Error: "work_ref workspace does not match authenticated tenant", ReasonCode: "forbidden"})
		return false
	}
	return true
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

// decodeMCPArgsLocal round-trips a JSON-decoded argument/payload map through
// JSON into a typed destination, mirroring the service-side MCP boundary
// decode so handler-level tenant checks read exactly what the service writes.
func decodeMCPArgsLocal(m map[string]any, dst any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// workMCPStringArg reads a trimmed string argument.
func workMCPStringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

// scopeSyncEntries fails closed (403) before any offline spool entry reaches
// the kernel when its tenant does not match the authenticated workspace member
// tenant. register entries carry the tenant in actor_identity.workspace_id;
// event entries carry it inside the work_ref. Offline spool payload is never
// authority for tenant isolation.
func (h *Handler) scopeSyncEntries(w http.ResponseWriter, r *http.Request, entries []workentry.SyncEntry) bool {
	for i := range entries {
		if !h.scopeSyncEntry(w, r, &entries[i]) {
			return false
		}
	}
	return true
}

// scopeSyncEntry validates one spool entry and pins the resolved tenant back
// into register payloads so the service can never persist a caller-supplied
// workspace. Undecodable payloads are left for the service's per-entry
// conflict reporting; they can never write cross-tenant.
func (h *Handler) scopeSyncEntry(w http.ResponseWriter, r *http.Request, entry *workentry.SyncEntry) bool {
	switch entry.Verb {
	case "register":
		var p struct {
			ActorIdentity workentry.WorkActorIdentityV1 `json:"actor_identity"`
		}
		if err := decodeMCPArgsLocal(entry.CanonicalPayload, &p); err != nil {
			return true
		}
		if !h.scopeWorkspace(w, r, &p.ActorIdentity.WorkspaceID) {
			return false
		}
		actor, _ := entry.CanonicalPayload["actor_identity"].(map[string]any)
		if actor == nil {
			actor = map[string]any{}
			entry.CanonicalPayload["actor_identity"] = actor
		}
		actor["workspace_id"] = p.ActorIdentity.WorkspaceID
		return true
	case "event":
		var p struct {
			Event workentry.WorkEventV1 `json:"event"`
		}
		if err := decodeMCPArgsLocal(entry.CanonicalPayload, &p); err != nil {
			return true
		}
		return h.requireWorkRefTenant(w, r, p.Event.WorkRef)
	default:
		// Unknown verbs never write; the service reports them per-entry.
		return true
	}
}

// requireMCPCallTenant fails closed (403) before a work.* write tool reaches
// the kernel when its tenant does not match the authenticated workspace member
// tenant. register/heartbeat carry the tenant in workspace_id; start/event/
// handoff/finish carry it inside the work_ref; sync entries are checked one by
// one. The resolved tenant is pinned back into workspace_id-carrying args.
func (h *Handler) requireMCPCallTenant(w http.ResponseWriter, r *http.Request, name string, args map[string]any) bool {
	switch name {
	case string(workentry.MCPWorkRegister):
		var p struct {
			ActorIdentity workentry.WorkActorIdentityV1 `json:"actor_identity"`
		}
		if err := decodeMCPArgsLocal(args, &p); err != nil {
			return true
		}
		if !h.scopeWorkspace(w, r, &p.ActorIdentity.WorkspaceID) {
			return false
		}
		actor, _ := args["actor_identity"].(map[string]any)
		if actor == nil {
			actor = map[string]any{}
			args["actor_identity"] = actor
		}
		actor["workspace_id"] = p.ActorIdentity.WorkspaceID
		return true
	case string(workentry.MCPWorkHeartbeat):
		ws := workMCPStringArg(args, "workspace_id")
		if !h.scopeWorkspace(w, r, &ws) {
			return false
		}
		args["workspace_id"] = ws
		return true
	case string(workentry.MCPWorkStart), string(workentry.MCPWorkEvent), string(workentry.MCPWorkHandoff), string(workentry.MCPWorkFinish):
		return h.requireWorkRefTenant(w, r, workMCPStringArg(args, "work_ref"))
	case string(workentry.MCPWorkSync):
		raw, ok := args["entries"]
		if !ok {
			return true
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return true
		}
		var entries []workentry.SyncEntry
		if err := json.Unmarshal(b, &entries); err != nil {
			return true
		}
		if !h.scopeSyncEntries(w, r, entries) {
			return false
		}
		args["entries"] = entries
		return true
	default:
		// Read-only tools (resolve/status/doctor) do not write.
		return true
	}
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
	if !decodeWorkRequest(w, r, &req) {
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
	if !decodeWorkRequest(w, r, &req) {
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
	if !decodeWorkRequest(w, r, &req) {
		return
	}
	if !h.scopeWorkspace(w, r, &req.WorkspaceID) {
		return
	}
	if !h.requireWorkRefTenant(w, r, req.WorkRef) {
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
	if !decodeWorkRequest(w, r, &req) {
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
	if !decodeWorkRequest(w, r, &event) {
		return
	}
	if !h.requireWorkRefTenant(w, r, event.WorkRef) {
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
	if !decodeWorkRequest(w, r, &pkg) {
		return
	}
	if !h.requireWorkRefTenant(w, r, pkg.WorkRef) {
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
	if !decodeWorkRequest(w, r, &c) {
		return
	}
	if !h.requireWorkRefTenant(w, r, c.WorkRef) {
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
	if !decodeWorkRequest(w, r, &req) {
		return
	}
	if !h.scopeSyncEntries(w, r, req.Entries) {
		return
	}
	res, err := h.WorkEntry.Sync(r.Context(), req.Entries)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryReview handles POST /api/work/review (independent verdict, PASS→approved,
// REVISE→changes_requested artifact_event). Reviewer must differ from implementer.
func (h *Handler) WorkEntryReview(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workentry.ReviewRequest
	if !decodeWorkRequest(w, r, &req) {
		return
	}
	if !h.scopeWorkspace(w, r, &req.WorkspaceID) {
		return
	}
	if !h.requireWorkRefTenant(w, r, req.WorkRef) {
		return
	}
	res, err := h.WorkEntry.Review(r.Context(), req)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryMCPTools handles GET /api/work/mcp/tools — exposes the work.* MCP
// tool manifest so HTTP/Generic carriers can discover the entry surface.
func (h *Handler) WorkEntryMCPTools(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	writeJSON(w, http.StatusOK, workentry.WorkMCPTools())
}

// workMCPCallRequest is the tools/call envelope.
type workMCPCallRequest struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// WorkEntryMCPCall handles POST /api/work/mcp/call — invokes one work.* tool.
// Every write traces to the authenticated workspace member tenant.
func (h *Handler) WorkEntryMCPCall(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workMCPCallRequest
	if !decodeWorkRequest(w, r, &req) {
		return
	}
	if !h.requireMCPCallTenant(w, r, req.Name, req.Arguments) {
		return
	}
	res, err := h.WorkEntry.CallMCPTool(r.Context(), req.Name, req.Arguments)
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

// WorkEntryParticipants handles GET /api/work/participants (VC-04 project-
// scoped participant read model). project_id is a query parameter; tenant
// isolation is enforced by the workspace-scoped receipt query plus the
// RequireWorkspaceMember middleware.
func (h *Handler) WorkEntryParticipants(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeWorkEntryError(w, workentry.ErrInvalidRequest)
		return
	}
	res, err := h.WorkEntry.ProjectParticipants(r.Context(), h.resolveWorkspaceID(r), projectID)
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntrySteward handles GET /api/work/steward (VC-10 Project Steward
// read-only diagnostics: no_owner / no_next_action / orphan / duplicate /
// stale / orphan_candidate / missing_review).
func (h *Handler) WorkEntrySteward(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	res, err := h.WorkEntry.StewardDiagnostics(r.Context(), h.resolveWorkspaceID(r))
	if err != nil {
		writeWorkEntryError(w, err)
		return
	}
	if res == nil {
		res = []workentry.StewardDiagnostic{}
	}
	writeJSON(w, http.StatusOK, res)
}

// WorkEntryAttach handles POST /api/work/attach.
func (h *Handler) WorkEntryAttach(w http.ResponseWriter, r *http.Request) {
	if !h.requireWorkEntry(w) {
		return
	}
	var req workentry.AttachRequest
	if !decodeWorkRequest(w, r, &req) {
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
	if !decodeWorkRequest(w, r, &req) {
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
