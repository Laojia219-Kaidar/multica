// Package wavescheduler decomposes a Project's Issues into dependency-aware
// waves and emits preview-only dispatch recommendations.
//
// A wave is a set of Issues that have no unresolved mutual dependencies and
// can therefore be dispatched in parallel. Waves are ordered: wave N+1 only
// becomes eligible once every Issue in wave N reaches a terminal state.
//
// The scheduler is read-only by contract: it never creates Tasks, mutates
// Issue status, or triggers dispatch. It returns a ScheduleResult the caller
// may render as a preview or feed into an idempotent dispatch command later.
//
// Input: Issues + IssueDependency rows for one Project.
// Output: ordered Waves, critical-path annotation, per-node recommendations.
package wavescheduler

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"time"
)

// IssueStatusTerminal enumerates the Issue statuses the scheduler treats as
// done. Anything not in this set is "open" and participates in wave planning.
var IssueStatusTerminal = map[string]bool{
	"done":      true,
	"cancelled": true,
}

// IssueStatusBlocked is the explicit blocked marker. Issues in this status
// are still open but flagged so the scheduler can annotate downstream nodes.
const IssueStatusBlocked = "blocked"

// PriorityRank maps priority strings to a numeric rank (lower = higher
// priority). Unknown priorities sort to the back.
var PriorityRank = map[string]int{
	"urgent":  0,
	"high":    1,
	"medium":  2,
	"low":     3,
	"backlog": 4,
}

// Issue is the scheduler's view of one Issue. Callers populate this from the
// DB row; the scheduler never touches the database directly.
type Issue struct {
	ID         string
	Title      string
	Status     string
	Priority   string
	AssigneeID string
	Stage      int
	UpdatedAt  time.Time
	ParentID   string
}

// Dependency is one directed edge: IssueID depends on DependsOnID.
type Dependency struct {
	IssueID     string
	DependsOnID string
	Type        string // "blocks", "blocked_by", "related"
}

// ScheduleInput bundles everything the scheduler needs for one Project.
type ScheduleInput struct {
	ProjectID    string
	Issues       []Issue
	Dependencies []Dependency
	Now          time.Time
}

// WaveNode is one Issue inside a Wave, annotated with scheduling metadata.
type WaveNode struct {
	IssueID    string   `json:"issue_id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Priority   string   `json:"priority"`
	AssigneeID string   `json:"assignee_id,omitempty"`
	WaveIndex  int      `json:"wave_index"`
	Depth      int      `json:"depth"`
	IsCritical bool     `json:"is_critical"`
	BlockedBy  []string `json:"blocked_by,omitempty"`
	// MissingDependencies lists dependency targets that are not part of the
	// current Project's issue set (cross-project, deleted, or otherwise
	// unknown). Such nodes are explicitly marked UNKNOWN and never ready:
	// the scheduler fails closed instead of silently dropping the edge.
	MissingDependencies []string `json:"missing_dependencies,omitempty"`
	Ready               bool     `json:"ready"`
	// IdempotencyKey is a stable, content-addressed key the dispatch command
	// can use to guarantee at-most-once execution per (project, issue, wave).
	IdempotencyKey string `json:"idempotency_key"`
	// MutexKey groups nodes that must not run concurrently even across waves
	// (e.g. same assignee with concurrency=1).
	MutexKey string `json:"mutex_key"`
}

// Wave is one parallel-dispatch group.
type Wave struct {
	Index      int        `json:"index"`
	Nodes      []WaveNode `json:"nodes"`
	ReadyAt    string     `json:"ready_at,omitempty"`
	IsCritical bool       `json:"is_critical"`
}

// ScheduleResult is the full preview output.
type ScheduleResult struct {
	ProjectID     string   `json:"project_id"`
	GeneratedAt   string   `json:"generated_at"`
	Waves         []Wave   `json:"waves"`
	CriticalPath  []string `json:"critical_path"`
	TotalIssues   int      `json:"total_issues"`
	ReadyNow      int      `json:"ready_now"`
	CycleDetected bool     `json:"cycle_detected"`
}

// Schedule decomposes the input into ordered waves. It is safe to call with
// empty inputs and always returns a non-nil result.
func Schedule(in ScheduleInput) *ScheduleResult {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	result := &ScheduleResult{
		ProjectID:   in.ProjectID,
		GeneratedAt: now.Format(time.RFC3339),
		TotalIssues: len(in.Issues),
	}

	if len(in.Issues) == 0 {
		return result
	}

	// Build adjacency: issue -> set of issues it depends on (must complete first).
	issueMap := make(map[string]*Issue, len(in.Issues))
	for i := range in.Issues {
		issueMap[in.Issues[i].ID] = &in.Issues[i]
	}

	// depsOf[X] = set of IDs X depends on (canonical hard edges, still open).
	depsOf := make(map[string]map[string]bool, len(in.Issues))
	// rdepsOf[X] = set of IDs that depend on X (X blocks them, canonically).
	rdepsOf := make(map[string]map[string]bool, len(in.Issues))
	// missingDepsOf[X] = dependency targets of X that are not in this
	// Project's issue set. These edges are never dropped silently: the node
	// is held and annotated as UNKNOWN (fail-closed).
	missingDepsOf := make(map[string]map[string]bool, len(in.Issues))
	for id := range issueMap {
		depsOf[id] = make(map[string]bool)
		rdepsOf[id] = make(map[string]bool)
		missingDepsOf[id] = make(map[string]bool)
	}

	for _, d := range in.Dependencies {
		// Normalize each row into the canonical scheduling direction, where
		// the hard edge always points from "must finish first" to "depends
		// on it":
		//   blocked_by: issue_id is blocked by depends_on_issue_id
		//               -> depends_on_issue_id must finish before issue_id
		//   blocks:     issue_id blocks depends_on_issue_id
		//               -> issue_id must finish before depends_on_issue_id
		//   related:    informational only, never an ordering constraint.
		var dependent, target string
		switch d.Type {
		case "blocked_by":
			dependent, target = d.IssueID, d.DependsOnID
		case "blocks":
			dependent, target = d.DependsOnID, d.IssueID
		default:
			// related (or an unknown type): not a hard scheduling constraint.
			continue
		}
		if _, ok := issueMap[dependent]; !ok {
			continue
		}
		if _, ok := issueMap[target]; !ok {
			// Cross-project or otherwise unknown dependency target: record
			// it explicitly instead of silently dropping the edge.
			missingDepsOf[dependent][target] = true
			continue
		}
		// Only open targets create ordering constraints.
		if IssueStatusTerminal[issueMap[target].Status] {
			continue
		}
		depsOf[dependent][target] = true
		rdepsOf[target][dependent] = true
	}

	// Topological decomposition into waves (Kahn's algorithm variant).
	// Each wave contains all nodes whose remaining open dependencies are empty.
	assigned := make(map[string]bool, len(in.Issues))
	remaining := make(map[string]bool, len(in.Issues))
	for id := range issueMap {
		if !IssueStatusTerminal[issueMap[id].Status] {
			remaining[id] = true
		}
	}

	var waves []Wave
	waveIdx := 0
	for len(remaining) > 0 {
		// Collect nodes with all deps satisfied.
		var ready []string
		for id := range remaining {
			// Nodes with UNKNOWN (missing) dependency targets can never be
			// satisfied within this issue set: hold them and fail closed.
			if len(missingDepsOf[id]) > 0 {
				continue
			}
			allMet := true
			for dep := range depsOf[id] {
				if remaining[dep] && !assigned[dep] {
					allMet = false
					break
				}
			}
			if allMet {
				ready = append(ready, id)
			}
		}

		if len(ready) == 0 {
			// No node can advance: either the remaining open issues contain a
			// hard-edge cycle, or some are held by UNKNOWN (cross-project /
			// missing) dependencies. Distinguish the two explicitly; held
			// issues are never marked ready and carry their missing targets.
			if hasCycle(remaining, depsOf) {
				result.CycleDetected = true
			}
			var blockedNodes []WaveNode
			for id := range remaining {
				iss := issueMap[id]
				var missing []string
				for m := range missingDepsOf[id] {
					missing = append(missing, m)
				}
				sort.Strings(missing)
				blockedNodes = append(blockedNodes, WaveNode{
					IssueID:             id,
					Title:               iss.Title,
					Status:              iss.Status,
					Priority:            iss.Priority,
					AssigneeID:          iss.AssigneeID,
					WaveIndex:           waveIdx,
					Depth:               waveIdx,
					Ready:               false,
					MissingDependencies: missing,
					IdempotencyKey:      idempotencyKey(in.ProjectID, id, waveIdx),
					MutexKey:            mutexKey(iss),
				})
			}
			sortNodes(blockedNodes)
			waves = append(waves, Wave{
				Index: waveIdx,
				Nodes: blockedNodes,
			})
			break
		}

		// Sort ready nodes: priority first, then ID for determinism.
		sort.Strings(ready)
		sort.SliceStable(ready, func(i, j int) bool {
			pi := priorityRank(issueMap[ready[i]].Priority)
			pj := priorityRank(issueMap[ready[j]].Priority)
			return pi < pj
		})

		var nodes []WaveNode
		for _, id := range ready {
			iss := issueMap[id]
			var blockedBy []string
			for dep := range depsOf[id] {
				if issueMap[dep].Status == IssueStatusBlocked {
					blockedBy = append(blockedBy, dep)
				}
			}
			sort.Strings(blockedBy)

			nodes = append(nodes, WaveNode{
				IssueID:        id,
				Title:          iss.Title,
				Status:         iss.Status,
				Priority:       iss.Priority,
				AssigneeID:     iss.AssigneeID,
				WaveIndex:      waveIdx,
				Depth:          waveIdx,
				Ready:          waveIdx == 0,
				BlockedBy:      blockedBy,
				IdempotencyKey: idempotencyKey(in.ProjectID, id, waveIdx),
				MutexKey:       mutexKey(iss),
			})
			assigned[id] = true
			delete(remaining, id)
		}

		w := Wave{
			Index: waveIdx,
			Nodes: nodes,
		}
		if waveIdx == 0 {
			w.ReadyAt = now.Format(time.RFC3339)
			w.IsCritical = true
		}
		waves = append(waves, w)
		waveIdx++
	}

	// Critical path: longest chain from any root to any leaf.
	criticalPath := findCriticalPath(issueMap, rdepsOf)
	criticalSet := make(map[string]bool, len(criticalPath))
	for _, id := range criticalPath {
		criticalSet[id] = true
	}
	for wi := range waves {
		for ni := range waves[wi].Nodes {
			if criticalSet[waves[wi].Nodes[ni].IssueID] {
				waves[wi].Nodes[ni].IsCritical = true
				waves[wi].IsCritical = true
			}
		}
	}

	result.Waves = waves
	result.CriticalPath = criticalPath
	for _, w := range waves {
		for _, n := range w.Nodes {
			if n.Ready {
				result.ReadyNow++
			}
		}
	}

	return result
}

// findCriticalPath returns the longest hard-edge dependency chain (by node
// count) over open Issues, from root to leaf. Terminal (done/cancelled)
// issues never appear in the path nor act as parents, consistent with wave
// decomposition, and only blocks/blocked_by edges in canonical direction
// participate. When multiple chains tie, the one with the highest-priority
// root wins.
func findCriticalPath(issueMap map[string]*Issue, rdepsOf map[string]map[string]bool) []string {
	if len(issueMap) == 0 {
		return nil
	}

	// children[X] = open Issues that depend on X (canonical hard edges only),
	// sorted for deterministic traversal. Terminal nodes are never entered.
	children := make(map[string][]string, len(issueMap))
	hasParent := make(map[string]bool, len(issueMap))
	for parent, dependents := range rdepsOf {
		if IssueStatusTerminal[issueMap[parent].Status] {
			continue
		}
		for c := range dependents {
			if IssueStatusTerminal[issueMap[c].Status] {
				continue
			}
			children[parent] = append(children[parent], c)
			hasParent[c] = true
		}
		sort.Strings(children[parent])
	}

	// Roots are open Issues with no open hard-edge parent. Parents that are
	// terminal were never recorded as edges, so they do not suppress a root.
	var roots []string
	for id := range issueMap {
		if IssueStatusTerminal[issueMap[id].Status] {
			continue
		}
		if !hasParent[id] {
			roots = append(roots, id)
		}
	}
	if len(roots) == 0 {
		// Every open Issue has an open hard-edge parent, so the open graph
		// contains a cycle (a DAG always has at least one root). Fall back to
		// every open Issue as a candidate root; the visited set keeps the DFS
		// cycle-safe. This is the pure-cycle case, not "all parents done".
		for id := range issueMap {
			if !IssueStatusTerminal[issueMap[id].Status] {
				roots = append(roots, id)
			}
		}
	}
	sort.Strings(roots)

	// DFS to find the longest simple path (cycle-safe via visited set).
	var best []string
	var dfs func(id string, path []string, visited map[string]bool)
	dfs = func(id string, path []string, visited map[string]bool) {
		kids := children[id]
		leaf := true
		for _, c := range kids {
			if visited[c] {
				continue
			}
			leaf = false
			visited[c] = true
			dfs(c, append(path, c), visited)
			delete(visited, c)
		}
		if leaf {
			if len(path) > len(best) || (len(path) == len(best) && len(best) > 0 && comparePriority(path[0], best[0], issueMap) < 0) {
				best = append([]string(nil), path...)
			}
		}
	}
	for _, r := range roots {
		visited := map[string]bool{r: true}
		dfs(r, []string{r}, visited)
	}
	return best
}

// hasCycle reports whether the canonical hard-edge graph induced by the
// remaining open issues contains a cycle. Edges to assigned, terminal, or
// missing targets are ignored; only edges inside `remaining` can close one.
func hasCycle(remaining map[string]bool, depsOf map[string]map[string]bool) bool {
	const (
		white = uint8(0)
		gray  = uint8(1)
		black = uint8(2)
	)
	color := make(map[string]uint8, len(remaining))
	var visit func(id string) bool
	visit = func(id string) bool {
		color[id] = gray
		for dep := range depsOf[id] {
			if !remaining[dep] {
				continue
			}
			switch color[dep] {
			case gray:
				return true
			case white:
				if visit(dep) {
					return true
				}
			}
		}
		color[id] = black
		return false
	}
	for id := range remaining {
		if color[id] == white && visit(id) {
			return true
		}
	}
	return false
}

func comparePriority(a, b string, m map[string]*Issue) int {
	return priorityRank(m[a].Priority) - priorityRank(m[b].Priority)
}

func priorityRank(p string) int {
	if r, ok := PriorityRank[p]; ok {
		return r
	}
	return len(PriorityRank)
}

// idempotencyKey produces a stable SHA-256 prefix for (project, issue, wave).
func idempotencyKey(projectID, issueID string, waveIdx int) string {
	raw := fmt.Sprintf("wave:%s:%s:%d", projectID, issueID, waveIdx)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("wave-%x", h[:8])
}

// mutexKey groups dispatches that must serialize. Same assignee → same key.
// Unassigned issues get a per-issue key (no mutual exclusion).
func mutexKey(iss *Issue) string {
	if iss.AssigneeID == "" {
		return fmt.Sprintf("issue:%s", iss.ID)
	}
	return fmt.Sprintf("assignee:%s", iss.AssigneeID)
}

func sortNodes(nodes []WaveNode) {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].IssueID < nodes[j].IssueID
	})
}
