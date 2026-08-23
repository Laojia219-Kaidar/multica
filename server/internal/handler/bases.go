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

// ListBases returns the observed execution bases in the workspace, aggregated
// under the formal base registry's canonical machine titles plus
// agent-to-runtime bindings. A runtime row joins its registered base when its
// device_info machine title is the exact registered title or that title
// followed by an exact " · " detail suffix (longest registered title wins).
// Rows that resolve to no registered base stay visible under their unmodified
// observed title but can never be commanded. Read-only; no second source of
// truth is created.
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
	bases, err := h.Queries.ListBases(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bases")
		return
	}

	writeJSON(w, http.StatusOK, buildCanonicalBaseOverviews(baseMachineTitles(bases), runtimes, agents))
}

// buildCanonicalBaseOverviews aggregates observed runtime rows and their
// resident agents under canonical registered machine titles, fail-closed.
// Runtime rows that resolve to no registered base are reported under their
// unmodified observed title, so an unregistered variant can never merge
// into — or inherit the drained state of — a registered base.
func buildCanonicalBaseOverviews(registeredTitles []string, runtimes []db.AgentRuntime, agents []db.Agent) []BaseOverview {
	type agg struct {
		machine    string
		online     int
		registered int
		employees  int
	}
	bases := make(map[string]*agg)
	order := make([]string, 0, len(runtimes))
	machineOfRuntime := make(map[string]string, len(runtimes))
	for _, rt := range runtimes {
		m := observedMachineKey(registeredTitles, rt.DeviceInfo)
		machineOfRuntime[uuidToString(rt.ID)] = m
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
	drained := make(map[string]int) // drained agent count per machine
	total := make(map[string]int)   // total agent count per machine
	for _, a := range agents {
		m := machineOfRuntime[uuidToString(a.RuntimeID)]
		if m == "" {
			continue
		}
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
	return resp
}

// SetBaseOperationalMode drains or resumes one formally registered execution
// base. The requested machine_title must be an exact registered base title;
// the mode is then applied to every agent bound to a runtime whose observed
// machine title resolves to that canonical title (exact title or exact
// " · " detail suffix) in the same workspace. Unregistered, prefix,
// substring, parenthesis and suffixed variants are rejected without any
// write. "resting" denies new task claims (drain); "active" re-enables them
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
	title := strings.TrimSpace(req.MachineTitle)
	if title == "" {
		writeError(w, http.StatusBadRequest, "machine_title is required")
		return
	}

	bases, err := h.Queries.ListBases(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bases")
		return
	}
	registeredTitles := baseMachineTitles(bases)
	if !registeredCanonicalTitle(registeredTitles, title) {
		writeError(w, http.StatusNotFound, "machine_title is not a registered base")
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

	updated := 0
	for _, a := range selectBaseDrainAgents(registeredTitles, runtimes, agents, workspaceID, title) {
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
		"machine_title":  title,
		"mode":           req.Mode,
		"agents_updated": updated,
	})
}

// baseMachineTitles returns the distinct, non-empty canonical machine titles
// of the formal base registry. The registry is the only authority that can
// grant a machine title command over observed runtime rows.
func baseMachineTitles(bases []db.Base) []string {
	seen := make(map[string]struct{}, len(bases))
	titles := make([]string, 0, len(bases))
	for _, b := range bases {
		title := strings.TrimSpace(b.MachineTitle)
		if title == "" {
			continue
		}
		if _, dup := seen[title]; dup {
			continue
		}
		seen[title] = struct{}{}
		titles = append(titles, title)
	}
	return titles
}

// resolveCanonicalMachine maps an observed runtime machine title to the
// formally registered base machine_title that commands it, fail-closed. An
// observed title is admitted only as an exact registered title or as a
// registered title followed by an exact " · " detail suffix
// ("HiveCosm Mac mini · 2.1.221 (Claude Code)"); when several registered
// titles match, the longest one wins. Raw prefixes, substrings, parenthesis
// variants and unregistered titles never resolve, so they can never gain
// command authority over a base.
func resolveCanonicalMachine(registeredTitles []string, observed string) (string, bool) {
	observed = strings.TrimSpace(observed)
	if observed == "" {
		return "", false
	}
	best := ""
	for _, registered := range registeredTitles {
		title := strings.TrimSpace(registered)
		if title == "" {
			continue
		}
		exact := observed == title
		suffixed := false
		if rest := strings.TrimPrefix(observed, title+" · "); rest != observed && strings.TrimSpace(rest) != "" {
			suffixed = true
		}
		if !exact && !suffixed {
			continue
		}
		if len(title) > len(best) {
			best = title
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// registeredCanonicalTitle reports whether the requested title is itself an
// exact registered base machine_title. Drain/resume admits only registered
// canonical titles — a request in "canonical · detail" form, or any prefix,
// substring or parenthesis variant, is rejected fail-closed.
func registeredCanonicalTitle(registeredTitles []string, requested string) bool {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return false
	}
	for _, registered := range registeredTitles {
		if requested == strings.TrimSpace(registered) {
			return true
		}
	}
	return false
}

// observedMachineKey resolves a runtime device_info to its canonical
// registered machine title, fail-closed. Rows no registered base commands
// are kept under their unmodified observed title — never re-parsed by
// guesswork — so they stay observable without gaining command affinity.
func observedMachineKey(registeredTitles []string, deviceInfo string) string {
	if canonical, ok := resolveCanonicalMachine(registeredTitles, deviceInfo); ok {
		return canonical
	}
	observed := strings.TrimSpace(deviceInfo)
	if observed == "" {
		return "unknown"
	}
	return observed
}

// selectBaseDrainAgents returns the agents whose runtime observes to the
// given canonical base title in the same workspace. Fail-closed on every
// axis: the canonical title must itself be registered, runtimes and agents
// outside the workspace are never selected, and runtime rows whose machine
// title resolves to no registered base (or to a different base) are never
// commanded. Selection only — the claim gate is evaluated at claim time, so
// in-flight tasks keep running untouched.
func selectBaseDrainAgents(registeredTitles []string, runtimes []db.AgentRuntime, agents []db.Agent, workspaceID, canonicalTitle string) []db.Agent {
	if !registeredCanonicalTitle(registeredTitles, canonicalTitle) {
		return nil
	}
	canonical := strings.TrimSpace(canonicalTitle)
	machineOfRuntime := make(map[string]string, len(runtimes))
	for _, rt := range runtimes {
		if uuidToString(rt.WorkspaceID) != workspaceID {
			continue
		}
		resolved, ok := resolveCanonicalMachine(registeredTitles, rt.DeviceInfo)
		if !ok {
			continue
		}
		machineOfRuntime[uuidToString(rt.ID)] = resolved
	}
	selected := make([]db.Agent, 0, len(agents))
	for _, a := range agents {
		if uuidToString(a.WorkspaceID) != workspaceID {
			continue
		}
		if machineOfRuntime[uuidToString(a.RuntimeID)] != canonical {
			continue
		}
		selected = append(selected, a)
	}
	return selected
}

// CompanyBase is the formal, company-owned base assignment (决策 A: formal
// base table + agent FK, migrated from custom_env). Distinct from the observed
// execution-base projection returned by ListBases.
type CompanyBase struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Device       string `json:"device"`
	MachineTitle string `json:"machine_title"`
	Agents       int    `json:"agents"`
}

// GetCompanyBases returns the formal company base registry with agent counts.
// Read-only governance read model; single writer is the base migration (405).
func (h *Handler) GetCompanyBases(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	bases, err := h.Queries.ListBases(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bases")
		return
	}
	counts, err := h.Queries.CountAgentsByBase(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count agents")
		return
	}
	countMap := make(map[string]int64, len(counts))
	for _, c := range counts {
		countMap[uuidToString(c.BaseID)] = c.AgentCount
	}
	resp := make([]CompanyBase, 0, len(bases))
	for _, b := range bases {
		resp = append(resp, CompanyBase{
			ID: uuidToString(b.ID), Code: b.Code, Name: b.Name,
			Device: b.Device, MachineTitle: b.MachineTitle,
			Agents: int(countMap[uuidToString(b.ID)]),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
