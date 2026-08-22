package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Runtime registration inventory (read-only).
//
// GET /api/runtimes/inventory[?employee=<agent-uuid|exact-agent-name>]
//
// Projects the existing Employee -> Agent -> Runtime -> RuntimeProfile ->
// Provider/Model -> online/registration_error chain for every digital
// employee agent (kind = 'user', not archived) in the caller's workspace.
// "Employee" here is the HiveCrew digital-employee agent itself — the same
// vocabulary /api/bases uses when it counts bound agents as Employees.
// The inventory never creates, copies or repairs anything; missing links are
// REPORTED (missing_agent / missing_runtime / missing_profile), not fixed.
//
// Security contract (HIV-792):
//   - Only the allowlisted fields below are emitted. runtime_config,
//     custom_args, custom_env, mcp_config, raw runtime metadata, tokens,
//     credentials and local sensitive paths are never echoed.
//   - A registration_error exposes only a fixed, server-owned reason code.
//     Daemon error text is never returned because it may contain credentials
//     or local paths in forms that a heuristic sanitizer cannot recognize.
//   - runtime.status "offline" means the runtime daemon is not connected. It
//     says NOTHING about Provider availability and is never re-interpreted.
//   - Every lookup is workspace-scoped; an employee reference never crosses
//     workspace boundaries.

// Link states.
const (
	runtimeInventoryStateOK                = "ok"
	runtimeInventoryStateMissingAgent      = "missing_agent"
	runtimeInventoryStateMissingRuntime    = "missing_runtime"
	runtimeInventoryStateMissingProfile    = "missing_profile"
	runtimeInventoryStateProfileBuiltin    = "builtin"
	runtimeInventoryStateProfileDisabled   = "disabled"
	runtimeInventoryStateUnknown           = "unknown"
	runtimeInventoryRegistrationOnline     = "online"
	runtimeInventoryRegistrationOffline    = "offline"
	runtimeInventoryRegistrationErrorState = "registration_error"
	runtimeInventoryRegistrationErrorCode  = "runtime_profile_registration_error"
	// runtimeInventoryFieldLimit caps echoed free-text fields (names,
	// daemon ids) so a hostile or accidental long value cannot balloon the
	// response.
	runtimeInventoryFieldLimit = 120
)

// runtimeInventoryStore is the narrow read surface the inventory walks. It
// is satisfied by *db.Queries in production and faked in tests.
type runtimeInventoryStore interface {
	ListAgents(ctx context.Context, workspaceID pgtype.UUID) ([]db.Agent, error)
	GetAgentInWorkspace(ctx context.Context, arg db.GetAgentInWorkspaceParams) (db.Agent, error)
	GetAgentRuntimeForWorkspace(ctx context.Context, arg db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error)
	GetRuntimeProfileForWorkspace(ctx context.Context, arg db.GetRuntimeProfileForWorkspaceParams) (db.RuntimeProfile, error)
}

// runtimeInventoryStoreFor is the production seam binding the walk to the
// handler's concrete query layer. Tests swap it for a fake store.
var runtimeInventoryStoreFor = func(h *Handler) runtimeInventoryStore {
	return h.Queries
}

type RuntimeInventoryResponse struct {
	Count     int                     `json:"count"`
	Employees []RuntimeInventoryEntry `json:"employees"`
}

type RuntimeInventoryEntry struct {
	Employee     RuntimeInventoryEmployee     `json:"employee"`
	Agent        RuntimeInventoryAgent        `json:"agent"`
	Runtime      RuntimeInventoryRuntime      `json:"runtime"`
	Profile      RuntimeInventoryProfile      `json:"profile"`
	Provider     string                       `json:"provider"`
	Model        string                       `json:"model"`
	Registration RuntimeInventoryRegistration `json:"registration"`
}

// RuntimeInventoryEmployee identifies the digital employee whose chain this
// row projects. The identity is the local agent; a reference that matches no
// agent at all is a 404, not a row.
type RuntimeInventoryEmployee struct {
	EmployeeID string `json:"employee_id"`
	Name       string `json:"name"`
}

// RuntimeInventoryAgent is the local agent link. state is "ok" or
// "missing_agent" (identity resolved to an archived agent row — the employee
// exists but has no active agent).
type RuntimeInventoryAgent struct {
	State       string `json:"state"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	RuntimeMode string `json:"runtime_mode,omitempty"`
	Status      string `json:"status,omitempty"`
}

// RuntimeInventoryRuntime is the bound runtime registration. state is "ok"
// or "missing_runtime" (no runtime_id bound, or the referenced row is gone).
// Status carries the raw runtime status ("online"/"offline").
type RuntimeInventoryRuntime struct {
	State    string `json:"state"`
	ID       string `json:"id,omitempty"`
	DaemonID string `json:"daemon_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Status   string `json:"status,omitempty"`
}

// RuntimeInventoryProfile is the custom runtime profile link. state is
// "ok", "builtin" (built-in runtime, no profile applies), "missing_profile"
// (profile_id references a deleted profile row), "disabled" (profile row
// exists with enabled = false) or "unknown" (chain did not reach it).
type RuntimeInventoryProfile struct {
	State          string `json:"state"`
	ID             string `json:"id,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	ProtocolFamily string `json:"protocol_family,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
}

// RuntimeInventoryRegistration is the end-of-chain reachability signal.
// state is "online", "offline" (daemon not connected — NOT a Provider
// availability statement), "registration_error" (daemon-reported profile
// registration failure) or "unknown" (chain did not reach a runtime).
type RuntimeInventoryRegistration struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// GetRuntimeInventory serves the read-only registration inventory. See the
// package-level security contract above.
func (h *Handler) GetRuntimeInventory(w http.ResponseWriter, r *http.Request) {
	employeeRef := strings.TrimSpace(r.URL.Query().Get("employee"))
	if len(employeeRef) > 128 {
		writeError(w, http.StatusBadRequest, "employee reference is too long")
		return
	}

	workspaceID, ok := h.runtimeInventoryWorkspace(w, r)
	if !ok {
		return
	}

	store := runtimeInventoryStoreFor(h)
	ctx := r.Context()

	var entries []RuntimeInventoryEntry
	if employeeRef == "" {
		agents, err := store.ListAgents(ctx, workspaceID)
		if err != nil {
			slog.Error("runtime inventory: list agents failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to build runtime inventory")
			return
		}
		// ListAgents is ordered by created_at ASC, giving a stable listing.
		entries = make([]RuntimeInventoryEntry, 0, len(agents))
		for i := range agents {
			agent := agents[i]
			entry, err := buildRuntimeInventoryEntry(ctx, store, workspaceID, &agent, nil)
			if err != nil {
				slog.Error("runtime inventory: chain walk failed", "error", err)
				writeError(w, http.StatusInternalServerError, "failed to build runtime inventory")
				return
			}
			entries = append(entries, entry)
		}
	} else {
		entry, matched, err := runtimeInventoryLookupEmployee(ctx, store, workspaceID, employeeRef)
		if err != nil {
			slog.Error("runtime inventory: employee lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to build runtime inventory")
			return
		}
		if !matched {
			writeError(w, http.StatusNotFound, "employee not found")
			return
		}
		entries = []RuntimeInventoryEntry{entry}
	}

	if entries == nil {
		entries = []RuntimeInventoryEntry{}
	}
	writeJSON(w, http.StatusOK, RuntimeInventoryResponse{
		Count:     len(entries),
		Employees: entries,
	})
}

// runtimeInventoryWorkspace resolves and validates the workspace scope for
// the inventory. Every subsequent lookup is bound to this workspace.
func (h *Handler) runtimeInventoryWorkspace(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	workspace := h.resolveWorkspaceID(r)
	if workspace == "" {
		writeError(w, http.StatusBadRequest, "workspace context is required")
		return pgtype.UUID{}, false
	}
	workspaceID, err := util.ParseUUID(workspace)
	if err != nil {
		writeError(w, http.StatusBadRequest, "workspace ID is not a valid UUID")
		return pgtype.UUID{}, false
	}
	return workspaceID, true
}

// runtimeInventoryLookupEmployee resolves an employee reference (agent UUID
// or exact agent name) to one inventory row. An archived agent resolves to a
// row with agent state missing_agent — the identity exists, the active agent
// does not. matched is false when the reference matches nothing.
func runtimeInventoryLookupEmployee(ctx context.Context, store runtimeInventoryStore, workspaceID pgtype.UUID, ref string) (RuntimeInventoryEntry, bool, error) {
	if agentID, err := util.ParseUUID(ref); err == nil {
		agent, err := store.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          agentID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return RuntimeInventoryEntry{}, false, nil
			}
			return RuntimeInventoryEntry{}, false, err
		}
		if agent.ArchivedAt.Valid {
			entry, err := buildRuntimeInventoryEntry(ctx, store, workspaceID, nil, &agent)
			return entry, true, err
		}
		entry, err := buildRuntimeInventoryEntry(ctx, store, workspaceID, &agent, nil)
		return entry, true, err
	}

	// Not a UUID: exact-name lookup over active digital employees. Order
	// follows ListAgents (created_at ASC), so duplicate names resolve
	// deterministically to the oldest agent.
	agents, err := store.ListAgents(ctx, workspaceID)
	if err != nil {
		return RuntimeInventoryEntry{}, false, err
	}
	for i := range agents {
		if agents[i].Name == ref {
			agent := agents[i]
			entry, err := buildRuntimeInventoryEntry(ctx, store, workspaceID, &agent, nil)
			return entry, true, err
		}
	}
	return RuntimeInventoryEntry{}, false, nil
}

// buildRuntimeInventoryEntry walks one Employee -> ... -> registration chain.
// agent is nil when the employee identity resolved only to an archived agent
// row (archivedAgent then carries that row for identity fields).
func buildRuntimeInventoryEntry(ctx context.Context, store runtimeInventoryStore, workspaceID pgtype.UUID, agent, archivedAgent *db.Agent) (RuntimeInventoryEntry, error) {
	entry := RuntimeInventoryEntry{
		Agent:        RuntimeInventoryAgent{State: runtimeInventoryStateUnknown},
		Runtime:      RuntimeInventoryRuntime{State: runtimeInventoryStateUnknown},
		Profile:      RuntimeInventoryProfile{State: runtimeInventoryStateUnknown},
		Registration: RuntimeInventoryRegistration{State: runtimeInventoryStateUnknown},
	}

	if agent == nil {
		if archivedAgent == nil {
			return entry, nil
		}
		entry.Employee = RuntimeInventoryEmployee{
			EmployeeID: capInventoryString(uuidToString(archivedAgent.ID)),
			Name:       capInventoryString(archivedAgent.Name),
		}
		entry.Agent.State = runtimeInventoryStateMissingAgent
		return entry, nil
	}

	entry.Employee = RuntimeInventoryEmployee{
		EmployeeID: capInventoryString(uuidToString(agent.ID)),
		Name:       capInventoryString(agent.Name),
	}
	entry.Agent = RuntimeInventoryAgent{
		State:       runtimeInventoryStateOK,
		ID:          capInventoryString(uuidToString(agent.ID)),
		Name:        capInventoryString(agent.Name),
		RuntimeMode: capInventoryString(agent.RuntimeMode),
		Status:      capInventoryString(agent.Status),
	}
	if agent.Model.Valid {
		entry.Model = capInventoryString(agent.Model.String)
	}

	if !agent.RuntimeID.Valid {
		entry.Runtime.State = runtimeInventoryStateMissingRuntime
		return entry, nil
	}
	runtime, err := store.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID:          agent.RuntimeID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		// A dangling runtime_id (row deleted) is a missing link to
		// report; any other storage failure aborts the inventory.
		if errors.Is(err, pgx.ErrNoRows) {
			entry.Runtime.State = runtimeInventoryStateMissingRuntime
			return entry, nil
		}
		return entry, err
	}
	entry.Runtime = RuntimeInventoryRuntime{
		State:    runtimeInventoryStateOK,
		ID:       capInventoryString(uuidToString(runtime.ID)),
		DaemonID: capInventoryString(runtime.DaemonID.String),
		Name:     capInventoryString(runtime.Name),
		Status:   capInventoryString(runtime.Status),
	}
	entry.Provider = capInventoryString(runtime.Provider)

	if !runtime.ProfileID.Valid {
		entry.Profile.State = runtimeInventoryStateProfileBuiltin
	} else {
		profile, err := store.GetRuntimeProfileForWorkspace(ctx, db.GetRuntimeProfileForWorkspaceParams{
			ID:          runtime.ProfileID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return entry, err
			}
			entry.Profile.State = runtimeInventoryStateMissingProfile
		} else {
			entry.Profile.State = runtimeInventoryStateOK
			if !profile.Enabled {
				entry.Profile.State = runtimeInventoryStateProfileDisabled
			}
			entry.Profile.ID = capInventoryString(uuidToString(profile.ID))
			entry.Profile.DisplayName = capInventoryString(profile.DisplayName)
			entry.Profile.ProtocolFamily = capInventoryString(profile.ProtocolFamily)
			enabled := profile.Enabled
			entry.Profile.Enabled = &enabled
		}
	}

	entry.Registration.State = normalizeRuntimeRegistrationState(runtime.Status)
	if failed, parseErr := registrationErrorFromRuntimeMetadata(runtime.Metadata); parseErr == nil && failed {
		entry.Registration.State = runtimeInventoryRegistrationErrorState
		entry.Registration.Reason = runtimeInventoryRegistrationErrorCode
	}
	return entry, nil
}

func normalizeRuntimeRegistrationState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case runtimeInventoryRegistrationOnline:
		return runtimeInventoryRegistrationOnline
	case runtimeInventoryRegistrationOffline:
		return runtimeInventoryRegistrationOffline
	default:
		return runtimeInventoryStateUnknown
	}
}

// registrationErrorFromRuntimeMetadata reads only the daemon-recorded
// profile registration failure flag. The metadata document and its free-text
// failure reason are never echoed or returned to the caller.
func registrationErrorFromRuntimeMetadata(metadata []byte) (bool, error) {
	if len(metadata) == 0 {
		return false, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &doc); err != nil {
		return false, err
	}
	failedRaw, ok := doc["runtime_profile_registration_error"]
	if !ok {
		return false, nil
	}
	var failed bool
	if err := json.Unmarshal(failedRaw, &failed); err != nil {
		return false, err
	}
	return failed, nil
}

// capInventoryString bounds echoed free-text fields.
func capInventoryString(value string) string {
	return capRunes(strings.TrimSpace(value), runtimeInventoryFieldLimit)
}

func capRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}
