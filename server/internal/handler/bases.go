package handler

import (
	"net/http"
	"strings"
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
	for _, a := range agents {
		m := runtimeMachine[uuidToString(a.RuntimeID)]
		if b := bases[m]; b != nil {
			b.employees++
		}
	}

	resp := make([]BaseOverview, 0, len(order))
	for _, m := range order {
		b := bases[m]
		resp = append(resp, BaseOverview{
			MachineTitle:      b.machine,
			RuntimeOnline:     b.online,
			RuntimeRegistered: b.registered,
			Employees:         b.employees,
		})
	}
	writeJSON(w, http.StatusOK, resp)
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
