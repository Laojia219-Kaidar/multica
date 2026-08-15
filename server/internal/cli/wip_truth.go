package cli

import (
	"sort"
	"time"
)

// ---------------------------------------------------------------------------
// M1 CORE R7 — duplicate Agent and reachable reconciliation repair
//
// ComputeWIPTruthCore is the internal pure core. ComputeWIPTruth is the
// backward-compatible wrapper that maps the old limited input
// (DaemonHealthSnapshot + []ServerPendingTask) honestly to the core, adding
// AGENT_PROJECTION_ABSENT because the legacy input carries no agent_id.
//
// R7 fail-closed guarantees (every rule below is covered by table-driven
// tests in wip_truth_test.go):
//
//   - Runtime scope: empty strings filtered, sorted, deduped; empty scope →
//     fail-closed early return with NO_RUNTIME_IDS_SCOPED.
//   - Histogram: every scoped task row falls into exactly one bucket
//     (queued | backlog | claimed | unknown). Foreign non-empty runtime →
//     silently ignored. Reconciliation check: bucket sum must equal scoped
//     total via the pure reconcileHistogram helper, else
//     RECONCILIATION_MISMATCH.
//   - Active task (dispatched / running / waiting_local_directory) missing
//     runtime_id or agent_id → UNKNOWN row. Duplicate scoped task ID →
//     UNKNOWN row. Duplicate agent ID on active tasks → UNKNOWN row. Unknown
//     task status → UNKNOWN row.
//   - Worker freshness: only Status=="working" + matching claimed task (same
//     agent_id + same runtime_id) + within freshness threshold → fresh.
//     Empty or duplicate AgentID in the projection → stable UNKNOWN reason
//     (duplicate detection scoped to the agent projection, so a single agent
//     can never be counted fresh/projected twice). Known non-working statuses
//     (idle/archived/offline/unstable) are an explicit counted bucket
//     (KnownNonWorkingWorkers): never fresh, never stale, never UnknownWorkers
//     and never UNKNOWN_AGENT_STATUS; truly unknown status, working without
//     runtime/observed_at, or absent projection entirely → stable UNKNOWN
//     reason. Worker reconciliation invariant: fresh + stale/unbacked +
//     known-nonworking + unknown == projection rows.
//   - ProjectedWorkingCount is derived ONLY from the agent projection (fresh
//     matched workers). When projection is absent (no workers) it is 0 and
//     ProjectionAvailable is false — never fabricated from daemon/claimed.
//   - Daemon-server divergence is computed independently of projection.
//   - Reason codes are stable enum strings (never concatenated with raw
//     state values).
//   - ComputeWIPTruth wrapper preserves the old exported types/signature so
//     cmd/multica keeps compiling. Legacy input without agent projection
//     produces honest UNKNOWN.
// ---------------------------------------------------------------------------

// Reason codes — stable enum values. Never concatenated with raw state.
const (
	ReasonNoRuntimeIDsScoped       = "NO_RUNTIME_IDS_SCOPED"
	ReasonMissingRuntimeID         = "MISSING_RUNTIME_ID"
	ReasonMissingAgentID           = "MISSING_AGENT_ID"
	ReasonDuplicateTaskID          = "DUPLICATE_TASK_ID"
	ReasonDuplicateAgentID         = "DUPLICATE_AGENT_ID"
	ReasonUnknownTaskStatus        = "UNKNOWN_TASK_STATUS"
	ReasonUnknownAgentStatus       = "UNKNOWN_AGENT_STATUS"
	ReasonWorkingMissingRuntime    = "WORKING_MISSING_RUNTIME"
	ReasonWorkingMissingObservedAt = "WORKING_MISSING_OBSERVED_AT"
	ReasonDaemonNotRunning         = "DAEMON_NOT_RUNNING"
	ReasonDaemonStatusMissing      = "DAEMON_STATUS_MISSING"
	ReasonAgentProjectionAbsent    = "AGENT_PROJECTION_ABSENT"
	ReasonReconciliationMismatch   = "RECONCILIATION_MISMATCH"
)

// Task status values recognised by the core engine.
const (
	StatusQueued                = "queued"
	StatusBacklog               = "backlog"
	StatusDispatched            = "dispatched"
	StatusRunning               = "running"
	StatusWaitingLocalDirectory = "waiting_local_directory"
)

// Agent status values recognised by the core engine. Only "working" can be
// fresh. idle/archived/offline/unstable are known non-working statuses in the
// projection vocabulary and are handled explicitly (never reported as
// unknown); any status outside this set is truly unknown.
const (
	AgentStatusIdle     = "idle"
	AgentStatusWorking  = "working"
	AgentStatusArchived = "archived"
	AgentStatusOffline  = "offline"
	AgentStatusUnstable = "unstable"
)

// DefaultFreshnessThreshold is the default maximum age for a worker
// observation to still be considered fresh.
const DefaultFreshnessThreshold = 5 * time.Minute

// DefaultCoreConfig returns a CoreConfig initialised with the given now and
// the default freshness threshold.
func DefaultCoreConfig(now time.Time) CoreConfig {
	return CoreConfig{Now: now, FreshnessThreshold: DefaultFreshnessThreshold}
}

// CoreConfig carries engine-level parameters that are not derivable from the
// daemon snapshot or task list. Now is the freshness reference instant;
// FreshnessThreshold is the maximum acceptable age of a worker observation.
type CoreConfig struct {
	Now                time.Time
	FreshnessThreshold time.Duration
}

// TaskRow is the per-task input for the core engine.
type TaskRow struct {
	ID        string
	Status    string
	RuntimeID string
	AgentID   string
}

// WorkerInput is the per-worker (agent projection) input for the core engine.
// Status and ObservedAt are required for freshness classification.
type WorkerInput struct {
	AgentID    string
	RuntimeID  string
	Status     string
	ObservedAt time.Time
}

// WIPTruthReport is the M1 candidate-only read-only diagnostic output.
// Every field is fail-closed: missing, invalid, or unscoped data produces
// UNKNOWN rather than a misleading zero.
type WIPTruthReport struct {
	ObservedAt             string             `json:"observed_at"`
	Daemon                 WIPDaemonSummary   `json:"daemon"`
	RuntimeIDs             []string           `json:"runtime_ids"`
	Server                 WIPServerHistogram `json:"server"`
	ScopedRows             int                `json:"scoped_rows"`
	UnknownRows            int                `json:"unknown_rows"`
	Reconciled             bool               `json:"reconciled"`
	ProjectionAvailable    bool               `json:"projection_available"`
	ProjectedWorkingCount  int                `json:"projected_working_count"`
	FreshMatchedWorkers    int                `json:"fresh_matched_workers"`
	StaleOrUnbackedWorkers int                `json:"stale_or_unbacked_workers"`
	KnownNonWorkingWorkers int                `json:"known_non_working_workers"`
	UnknownWorkers         int                `json:"unknown_workers"`
	DaemonServerDivergence int                `json:"daemon_server_divergence"`
	ProjectionDivergence   int                `json:"projection_divergence"`
	UnknownReasons         []string           `json:"unknown_reasons"`
}

// WIPDaemonSummary is the daemon-scoped slice of the report.
type WIPDaemonSummary struct {
	Status      string `json:"status"`
	ID          string `json:"id"`
	Version     string `json:"version"`
	ActiveCount int    `json:"active_count"`
}

// WIPServerHistogram is the server-side task queue histogram scoped to the
// daemon's runtime IDs.
type WIPServerHistogram struct {
	Queued                int `json:"queued"`
	Backlog               int `json:"backlog"`
	Claimed               int `json:"claimed"`
	Dispatched            int `json:"dispatched"`
	Running               int `json:"running"`
	WaitingLocalDirectory int `json:"waiting_local_directory"`
}

// ServerPendingTask is the minimal server-side task shape the probe needs.
// It is populated from the daemon pending-tasks endpoint response.
type ServerPendingTask struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	RuntimeID  string `json:"runtime_id"`
	WaitReason string `json:"wait_reason,omitempty"`
}

// DaemonHealthSnapshot is the minimal daemon /health shape the probe needs.
type DaemonHealthSnapshot struct {
	Status          string `json:"status"`
	DaemonID        string `json:"daemon_id"`
	CLIVersion      string `json:"cli_version"`
	ActiveTaskCount int64  `json:"active_task_count"`
	Workspaces      []struct {
		ID       string   `json:"id"`
		Runtimes []string `json:"runtimes"`
	} `json:"workspaces"`
}

// agentRuntimeKey identifies a unique (agent, runtime) pair for claimed-task
// to worker matching.
type agentRuntimeKey struct {
	agentID   string
	runtimeID string
}

// ---------------------------------------------------------------------------
// Core engine
// ---------------------------------------------------------------------------

// ComputeWIPTruthCore is the pure truth engine. It takes pre-extracted
// runtime IDs, typed task rows, worker connections, daemon state, and a
// CoreConfig carrying the freshness reference instant + threshold. No I/O,
// no external API shapes. All UNKNOWN / fail-closed logic lives here.
func ComputeWIPTruthCore(runtimeIDs []string, tasks []TaskRow, workers []WorkerInput, daemonRunning bool, daemonActive int, cfg CoreConfig) WIPTruthReport {
	var r WIPTruthReport
	rs := newReasonSet()

	// Sorted, deduped, empty-filtered runtime scope.
	r.RuntimeIDs = sortedDedupNonEmpty(runtimeIDs)

	if len(r.RuntimeIDs) == 0 {
		rs.add(ReasonNoRuntimeIDsScoped)
		if len(workers) == 0 {
			rs.add(ReasonAgentProjectionAbsent)
		}
		r.ProjectionAvailable = len(workers) > 0
		r.UnknownReasons = rs.sorted()
		r.Reconciled = true // vacuously true: zero scoped rows, zero buckets
		return r
	}

	scope := make(map[string]struct{}, len(r.RuntimeIDs))
	for _, id := range r.RuntimeIDs {
		scope[id] = struct{}{}
	}

	// Classification state.
	seenTaskIDs := make(map[string]struct{})
	seenAgentIDs := make(map[string]struct{})
	claimedSet := make(map[agentRuntimeKey]struct{})

	var scopedCount int
	var unknownCount int

	for _, t := range tasks {
		// Missing runtime_id → UNKNOWN (before scope filter; cannot determine scope).
		if t.RuntimeID == "" {
			rs.add(ReasonMissingRuntimeID)
			scopedCount++
			unknownCount++
			continue
		}
		// Foreign non-empty runtime → silently ignored.
		if _, ok := scope[t.RuntimeID]; !ok {
			continue
		}
		// Scoped row.
		scopedCount++

		// Duplicate task ID (non-empty only).
		if t.ID != "" {
			if _, dup := seenTaskIDs[t.ID]; dup {
				rs.add(ReasonDuplicateTaskID)
				unknownCount++
				continue
			}
			seenTaskIDs[t.ID] = struct{}{}
		}

		switch t.Status {
		case StatusQueued:
			r.Server.Queued++
		case StatusBacklog:
			r.Server.Backlog++
		case StatusDispatched, StatusRunning, StatusWaitingLocalDirectory:
			// Active/claimed candidate — agent_id is required.
			if t.AgentID == "" {
				rs.add(ReasonMissingAgentID)
				unknownCount++
				continue
			}
			// An agent may claim at most one active task.
			if _, dup := seenAgentIDs[t.AgentID]; dup {
				rs.add(ReasonDuplicateAgentID)
				unknownCount++
				continue
			}
			seenAgentIDs[t.AgentID] = struct{}{}
			r.Server.Claimed++
			claimedSet[agentRuntimeKey{t.AgentID, t.RuntimeID}] = struct{}{}
			switch t.Status {
			case StatusDispatched:
				r.Server.Dispatched++
			case StatusRunning:
				r.Server.Running++
			case StatusWaitingLocalDirectory:
				r.Server.WaitingLocalDirectory++
			}
		default:
			rs.add(ReasonUnknownTaskStatus)
			unknownCount++
		}
	}

	r.ScopedRows = scopedCount
	r.UnknownRows = unknownCount

	// Reconciliation: queued + backlog + claimed + unknown == scoped total.
	// The helper returns both the reconciled flag and a stable reason code
	// (empty on match); production consumes its reason directly rather than
	// fabricating RECONCILIATION_MISMATCH separately.
	reconciled, mismatchReason := reconcileHistogram(scopedCount, r.Server.Queued, r.Server.Backlog, r.Server.Claimed, unknownCount)
	r.Reconciled = reconciled
	if mismatchReason != "" {
		rs.add(mismatchReason)
	}

	// --- Worker freshness / agent projection ---
	projectionAvailable := len(workers) > 0
	r.ProjectionAvailable = projectionAvailable
	if !projectionAvailable {
		rs.add(ReasonAgentProjectionAbsent)
	}

	// Duplicate detection is scoped to the agent projection: each AgentID may
	// appear at most once among workers, so a single agent can never be
	// counted fresh/projected twice.
	seenWorkerAgentIDs := make(map[string]struct{})

	for _, w := range workers {
		// Empty agent ID → UNKNOWN (cannot be matched to any claim).
		if w.AgentID == "" {
			rs.add(ReasonMissingAgentID)
			r.UnknownWorkers++
			continue
		}
		// Duplicate agent ID within the projection → UNKNOWN.
		if _, dup := seenWorkerAgentIDs[w.AgentID]; dup {
			rs.add(ReasonDuplicateAgentID)
			r.UnknownWorkers++
			continue
		}
		seenWorkerAgentIDs[w.AgentID] = struct{}{}

		// Truly unknown status → UNKNOWN. Known non-working statuses
		// (idle/archived/offline/unstable) are handled explicitly below.
		if !isKnownAgentStatus(w.Status) {
			rs.add(ReasonUnknownAgentStatus)
			r.UnknownWorkers++
			continue
		}
		// Only "working" agents can be fresh; known non-working statuses
		// (idle/archived/offline/unstable) are an explicit counted bucket —
		// never fresh, never stale, never UnknownWorkers, and never
		// UNKNOWN_AGENT_STATUS.
		if w.Status != AgentStatusWorking {
			r.KnownNonWorkingWorkers++
			continue
		}
		// Working agent must carry runtime + observed_at.
		if w.RuntimeID == "" {
			rs.add(ReasonWorkingMissingRuntime)
			r.UnknownWorkers++
			continue
		}
		if w.ObservedAt.IsZero() {
			rs.add(ReasonWorkingMissingObservedAt)
			r.UnknownWorkers++
			continue
		}
		// Must have a matching claimed task on the same runtime.
		if _, ok := claimedSet[agentRuntimeKey{w.AgentID, w.RuntimeID}]; !ok {
			r.StaleOrUnbackedWorkers++
			continue
		}
		// Freshness boundary check.
		if isWorkerFresh(w, cfg) {
			r.FreshMatchedWorkers++
		} else {
			r.StaleOrUnbackedWorkers++
		}
	}

	// ProjectedWorkingCount — derived ONLY from agent projection (fresh matched
	// workers). Never fabricated from daemon/claimed when projection absent.
	if projectionAvailable {
		r.ProjectedWorkingCount = r.FreshMatchedWorkers
		pd := r.ProjectedWorkingCount - r.Server.Claimed
		if pd < 0 {
			pd = -pd
		}
		r.ProjectionDivergence = pd
	}

	// Daemon-server divergence — independent of projection availability.
	if daemonRunning {
		d := daemonActive - r.Server.Claimed
		if d < 0 {
			d = -d
		}
		r.DaemonServerDivergence = d
	} else {
		rs.add(ReasonDaemonNotRunning)
	}

	r.UnknownReasons = rs.sorted()
	return r
}

// isWorkerFresh returns true iff the worker has a valid reference instant
// (cfg.Now), the observation is not in the future, and the observation age
// is within the threshold. Boundary-inclusive: exactly at threshold → fresh.
func isWorkerFresh(w WorkerInput, cfg CoreConfig) bool {
	if cfg.Now.IsZero() {
		return false
	}
	if cfg.Now.Before(w.ObservedAt) {
		return false // observation in the future — clock skew
	}
	return cfg.Now.Sub(w.ObservedAt) <= cfg.FreshnessThreshold
}

// isKnownAgentStatus reports whether status is one of the canonical agent
// statuses in the projection vocabulary. Known non-working statuses are
// handled explicitly; only truly unknown statuses are UNKNOWN.
func isKnownAgentStatus(status string) bool {
	switch status {
	case AgentStatusIdle, AgentStatusWorking, AgentStatusArchived, AgentStatusOffline, AgentStatusUnstable:
		return true
	}
	return false
}

// reconcileHistogram reports whether the histogram buckets sum exactly to the
// scoped total, returning both the reconciled flag and a stable reason code
// (ReasonReconciliationMismatch on mismatch, empty on match). Extracted as a
// pure helper so the reconciliation rule — bool plus reason — is unit-testable
// in isolation.
func reconcileHistogram(scoped, queued, backlog, claimed, unknown int) (bool, string) {
	if queued+backlog+claimed+unknown == scoped {
		return true, ""
	}
	return false, ReasonReconciliationMismatch
}

// ---------------------------------------------------------------------------
// Compatibility wrapper — preserves old ComputeWIPTruth signature
// ---------------------------------------------------------------------------

// ComputeWIPTruth produces a WIPTruthReport from the daemon health snapshot
// and the per-runtime pending task lists. It is a pure function with no I/O
// so tests can exercise it directly.
//
// This backward-compatible wrapper maps the old limited input honestly to
// ComputeWIPTruthCore. Because ServerPendingTask carries no agent_id, every
// active task is UNKNOWN (MISSING_AGENT_ID) and the agent projection is
// absent — an honest UNKNOWN rather than a fabricated count.
func ComputeWIPTruth(snap DaemonHealthSnapshot, tasks []ServerPendingTask, now time.Time) WIPTruthReport {
	var runtimeIDs []string
	for _, ws := range snap.Workspaces {
		runtimeIDs = append(runtimeIDs, ws.Runtimes...)
	}

	daemonRunning := snap.Status == "running"
	daemonActive := 0
	if daemonRunning {
		daemonActive = int(snap.ActiveTaskCount)
	}

	rows := make([]TaskRow, len(tasks))
	for i, t := range tasks {
		rows[i] = TaskRow{
			ID:        t.ID,
			Status:    t.Status,
			RuntimeID: t.RuntimeID,
			// AgentID intentionally empty — ServerPendingTask has no agent.
		}
	}

	report := ComputeWIPTruthCore(runtimeIDs, rows, nil, daemonRunning, daemonActive, DefaultCoreConfig(now))

	report.ObservedAt = now.UTC().Format(time.RFC3339)
	report.Daemon = WIPDaemonSummary{
		ID:      snap.DaemonID,
		Version: snap.CLIVersion,
	}
	if snap.Status == "" {
		report.Daemon.Status = "UNKNOWN"
		report.addUnknownReason(ReasonDaemonStatusMissing)
	} else {
		report.Daemon.Status = snap.Status
		if daemonRunning {
			report.Daemon.ActiveCount = int(snap.ActiveTaskCount)
		}
	}

	sort.Strings(report.UnknownReasons)
	return report
}

// addUnknownReason appends a reason if not already present.
func (r *WIPTruthReport) addUnknownReason(reason string) {
	for _, existing := range r.UnknownReasons {
		if existing == reason {
			return
		}
	}
	r.UnknownReasons = append(r.UnknownReasons, reason)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// reasonSet is a deduplicated set of stable reason codes.
type reasonSet struct {
	m map[string]struct{}
}

func newReasonSet() *reasonSet {
	return &reasonSet{m: make(map[string]struct{})}
}

func (s *reasonSet) add(reason string) {
	s.m[reason] = struct{}{}
}

func (s *reasonSet) sorted() []string {
	if len(s.m) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.m))
	for r := range s.m {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// sortedDedupNonEmpty returns a sorted, deduplicated copy of ss with all
// empty strings removed.
func sortedDedupNonEmpty(ss []string) []string {
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
