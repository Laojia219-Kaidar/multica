package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BaseRuntimeInfo is one runtime observed on a physical execution base.
type BaseRuntimeInfo struct {
	RuntimeID   string `json:"runtime_id"`
	RuntimeName string `json:"runtime_name"`
	Status      string `json:"status"`
	RuntimeMode string `json:"runtime_mode"`
	DaemonID    string `json:"daemon_id"`
}

// RuntimeBaseOverview is the read-only projection of one observed execution base
// (a physical machine, grouped from runtime device_info machine titles plus
// agent-to-runtime bindings). It is a read model — no second source of truth
// is created, and company-owned home/fallback base assignment is a separate
// authority not computed here.
type RuntimeBaseOverview struct {
	MachineTitle      string            `json:"machine_title"`
	RuntimeRegistered int               `json:"runtime_registered"`
	RuntimeOnline     int               `json:"runtime_online"`
	RuntimeOffline    int               `json:"runtime_offline"`
	DaemonCount       int               `json:"daemon_count"`
	Employees         int               `json:"employees"`
	LoadRunning       int               `json:"load_running"`
	Runtimes          []BaseRuntimeInfo `json:"runtimes"`
}

// ListRuntimeBases returns the observed execution bases in the workspace, grouped
// from runtime device_info machine titles plus agent-to-runtime bindings.
// Read-only.
func (h *Handler) ListRuntimeBases(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return
	}
	agents, err := h.Queries.ListAgents(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agents")
		return
	}

	writeJSON(w, http.StatusOK, buildBaseOverviews(runtimes, agents))
}

// buildBaseOverviews is a pure projection shared by the handler and its tests.
// It groups runtimes by observed machine title and attaches resident agents
// (employees) plus running load from agent status.
func buildBaseOverviews(runtimes []db.AgentRuntime, agents []db.Agent) []RuntimeBaseOverview {
	machineByRuntime := make(map[string]string, len(runtimes))
	for _, runtime := range runtimes {
		machineByRuntime[uuidToString(runtime.ID)] = machineTitleForBases(runtime.DeviceInfo)
	}

	type aggregate struct {
		registered int
		online     int
		offline    int
		daemons    map[string]struct{}
		employees  int
		running    int
		runtimes   []BaseRuntimeInfo
	}
	order := make([]string, 0, len(runtimes))
	aggregates := make(map[string]*aggregate, len(runtimes))
	for _, runtime := range runtimes {
		machine := machineByRuntime[uuidToString(runtime.ID)]
		entry := aggregates[machine]
		if entry == nil {
			entry = &aggregate{daemons: make(map[string]struct{})}
			aggregates[machine] = entry
			order = append(order, machine)
		}
		entry.registered++
		switch runtime.Status {
		case "online":
			entry.online++
		default:
			entry.offline++
		}
		if runtime.DaemonID.Valid {
			entry.daemons[runtime.DaemonID.String] = struct{}{}
		}
		entry.runtimes = append(entry.runtimes, BaseRuntimeInfo{
			RuntimeID:   uuidToString(runtime.ID),
			RuntimeName: runtime.Name,
			Status:      runtime.Status,
			RuntimeMode: runtime.RuntimeMode,
			DaemonID:    daemonIDValue(runtime),
		})
	}

	for _, agent := range agents {
		machine, ok := machineByRuntime[uuidToString(agent.RuntimeID)]
		if !ok {
			continue
		}
		entry := aggregates[machine]
		if entry == nil {
			continue
		}
		entry.employees++
		if agent.Status == "working" {
			entry.running++
		}
	}

	result := make([]RuntimeBaseOverview, 0, len(order))
	for _, machine := range order {
		entry := aggregates[machine]
		result = append(result, RuntimeBaseOverview{
			MachineTitle:      machine,
			RuntimeRegistered: entry.registered,
			RuntimeOnline:     entry.online,
			RuntimeOffline:    entry.offline,
			DaemonCount:       len(entry.daemons),
			Employees:         entry.employees,
			LoadRunning:       entry.running,
			Runtimes:          entry.runtimes,
		})
	}
	return result
}

// machineTitleForBases extracts the observed machine title from a runtime
// device_info string ("HiveCosm Mac mini · 2.1.221 (Claude Code)").
func machineTitleForBases(deviceInfo string) string {
	machine := strings.TrimSpace(deviceInfo)
	if index := strings.Index(machine, " · "); index >= 0 {
		machine = strings.TrimSpace(machine[:index])
	}
	if machine == "" {
		return "unknown"
	}
	return machine
}

func daemonIDValue(runtime db.AgentRuntime) string {
	if !runtime.DaemonID.Valid {
		return ""
	}
	return runtime.DaemonID.String
}

// migrateRuntimeAgentsRequest re-points every agent (and their queued/historic
// tasks) from a faulted source runtime onto a healthy target runtime. Agent
// IDs never change, so the Employee -> Agent identity binding is preserved
// across the migration.
type migrateRuntimeAgentsRequest struct {
	TargetRuntimeID string `json:"target_runtime_id"`
}

type migrateRuntimeAgentsResponse struct {
	SourceRuntimeID string `json:"source_runtime_id"`
	TargetRuntimeID string `json:"target_runtime_id"`
	AgentsMigrated  int64  `json:"agents_migrated"`
	TasksMigrated   int64  `json:"tasks_migrated"`
}

// MigrateRuntimeAgents migrates a faulted runtime's resident agents onto a
// healthy runtime in the same workspace without changing any agent identity.
// Owner/admin only.
func (h *Handler) MigrateRuntimeAgents(w http.ResponseWriter, r *http.Request) {
	sourceUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "runtimeId"), "runtime_id")
	if !ok {
		return
	}
	source, err := h.Queries.GetAgentRuntime(r.Context(), sourceUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "source runtime not found")
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(source.WorkspaceID), "source runtime not found", "owner", "admin"); !ok {
		return
	}

	var request migrateRuntimeAgentsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	targetUUID, ok := parseUUIDOrBadRequest(w, request.TargetRuntimeID, "target_runtime_id")
	if !ok {
		return
	}
	target, err := h.Queries.GetAgentRuntime(r.Context(), targetUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "target runtime not found")
		return
	}

	if err := validateRuntimeMigration(source, target); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	agentsMigrated, err := h.Queries.ReassignAgentsToRuntime(r.Context(), db.ReassignAgentsToRuntimeParams{
		NewRuntimeID: targetUUID,
		OldRuntimeID: sourceUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to migrate agents")
		return
	}
	tasksMigrated, err := h.Queries.ReassignTasksToRuntime(r.Context(), db.ReassignTasksToRuntimeParams{
		NewRuntimeID: targetUUID,
		OldRuntimeID: sourceUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to migrate tasks")
		return
	}

	writeJSON(w, http.StatusOK, migrateRuntimeAgentsResponse{
		SourceRuntimeID: uuidToString(sourceUUID),
		TargetRuntimeID: uuidToString(targetUUID),
		AgentsMigrated:  agentsMigrated,
		TasksMigrated:   tasksMigrated,
	})
}

// validateRuntimeMigration enforces the fault-migration invariant: a healthy
// online target in the same workspace, and a non-online (faulted) source.
// Agent identity is intentionally out of scope — migration re-points runtimes,
// never agent IDs.
func validateRuntimeMigration(source, target db.AgentRuntime) error {
	if uuidToString(source.WorkspaceID) != uuidToString(target.WorkspaceID) {
		return errors.New("source and target runtimes must be in the same workspace")
	}
	if uuidToString(source.ID) == uuidToString(target.ID) {
		return errors.New("source and target runtimes must differ")
	}
	if target.Status != "online" {
		return errors.New("target runtime must be online")
	}
	if source.Status == "online" {
		return errors.New("source runtime is healthy and has nothing to migrate")
	}
	return nil
}
