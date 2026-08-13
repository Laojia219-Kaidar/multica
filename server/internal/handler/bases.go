package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BaseOverview is the read-only projection of one observed execution base
// (a physical machine, derived from runtime device_info). Company-owned
// home/fallback base assignment is a separate authority and is not computed
// here.
type BaseOverview struct {
	MachineTitle      string `json:"machine_title"`
	RuntimeOnline     int    `json:"runtime_online"`
	RuntimeRegistered int    `json:"runtime_registered"`
	Employees         int    `json:"employees"`
	Drained           bool   `json:"drained"`
}

// SetBaseOperationalModeRequest is the body for POST /api/bases/operational-mode.
// mode is "resting" (drain: deny new claims) or "active" (resume).
type SetBaseOperationalModeRequest struct {
	MachineTitle string `json:"machine_title"`
	Mode         string `json:"mode"`
}

// ListBases returns the observed execution bases in the workspace, grouped
// from runtime device_info machine titles plus agent-to-runtime bindings.
// Read-only; no second source of truth is created.
func (h *Handler) ListBases(w http.ResponseWriter, r *http.Request) {
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

	runtimeMachine := make(map[string]string, len(runtimes))
	for _, rt := range runtimes {
		runtimeMachine[uuidToString(rt.ID)] = machineTitle(rt.DeviceInfo)
	}

	type agg struct {
		machine    string
		online     int
		registered int
		employees  int
	}
	bases := make(map[string]*agg)
	order := make([]string, 0, len(runtimes))
	for _, rt := range runtimes {
		m := runtimeMachine[uuidToString(rt.ID)]
		b := bases[m]
		if b == nil {
			b = &agg{machine: m}
			bases[m] = b
			order = append(order, m)
		}
		b.registered++
		if rt.Status == "online" {
			b.online++
		}
	}
	drained := make(map[string]int)   // drained agent count per machine
	total := make(map[string]int)    // total agent count per machine
	for _, a := range agents {
		m := runtimeMachine[uuidToString(a.RuntimeID)]
		if b := bases[m]; b != nil {
			b.employees++
		}
		total[m]++
		if a.OperationalMode == "resting" || a.OperationalMode == "disabled" {
			drained[m]++
		}
	}
	isDrained := func(m string) bool {
		return total[m] > 0 && drained[m] == total[m]
	}

	resp := make([]BaseOverview, 0, len(order))
	for _, m := range order {
		b := bases[m]
		resp = append(resp, BaseOverview{
			MachineTitle:      b.machine,
			RuntimeOnline:     b.online,
			RuntimeRegistered: b.registered,
			Employees:         b.employees,
			Drained:           isDrained(b.machine),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetBaseOperationalMode drains or resumes one observed execution base by
// setting every agent bound to a runtime on that machine to the given claim
// mode. "resting" denies new task claims (drain); "active" re-enables them
// (resume). In-flight tasks are unaffected — the gate is evaluated at claim
// time, so draining lets running work finish while new work stays queued.
// Owner/admin only.
func (h *Handler) SetBaseOperationalMode(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	var req SetBaseOperationalModeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Mode != "resting" && req.Mode != "active" {
		writeError(w, http.StatusBadRequest, "mode must be 'resting' or 'active'")
		return
	}
	if strings.TrimSpace(req.MachineTitle) == "" {
		writeError(w, http.StatusBadRequest, "machine_title is required")
		return
	}

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

	runtimeMachine := make(map[string]string, len(runtimes))
	for _, rt := range runtimes {
		runtimeMachine[uuidToString(rt.ID)] = machineTitle(rt.DeviceInfo)
	}

	updated := 0
	for _, a := range agents {
		if runtimeMachine[uuidToString(a.RuntimeID)] != req.MachineTitle {
			continue
		}
		if _, err := h.Queries.SetAgentOperationalMode(r.Context(), db.SetAgentOperationalModeParams{
			ID:              a.ID,
			OperationalMode: req.Mode,
		}); err != nil {
			slog.Error("set agent operational mode", "agent_id", uuidToString(a.ID), "error", err)
			continue
		}
		updated++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"machine_title":  req.MachineTitle,
		"mode":           req.Mode,
		"agents_updated": updated,
	})
}

// machineTitle extracts the physical machine title from a runtime's
// device_info ("HiveCosm Mac mini · 2.1.221 (Claude Code)") — the observed
// execution location, not a company-owned base assignment.
func machineTitle(deviceInfo string) string {
	machine := strings.TrimSpace(deviceInfo)
	if i := strings.Index(machine, " · "); i >= 0 {
		machine = strings.TrimSpace(machine[:i])
	}
	if machine == "" {
		return "unknown"
	}
	return machine
}
