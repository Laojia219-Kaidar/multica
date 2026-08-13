package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProjectHealth is the derived A-G health classification from the HIV-553
// project-lifecycle contract. It is a READ-ONLY projection over the existing
// project / issue / agent_task_queue truth; it never writes project.status.
//
//   - active_with_frontier (A): at least one nonterminal Task/Run for a
//     project issue.
//   - stalled_no_open_task (B): nonterminal issues exist but no nonterminal
//     Task/Run and no more-specific review/repair block.
//   - review_or_repair_blocked (C): review / blocked / repair gate with no
//     live task.
//   - ready_for_closure (D): all issues terminal + outcomes accepted/waived/
//     failed + closure package present.
//   - duplicate_or_superseded (E): two projects point at the same canonical
//     authority; owner must decide keep/merge/supersede.
//   - source_gap (G): closure evidence (artifact/outcome/receipt/resource)
//     cannot be read back.
//
// owner_decision_required (F) is a co-occurring flag, not a primary health:
// a missing accountable lead forces an owner decision on top of A-G.
type ProjectHealth string

const (
	HealthActiveWithFrontier    ProjectHealth = "active_with_frontier"
	HealthStalledNoOpenTask     ProjectHealth = "stalled_no_open_task"
	HealthReviewOrRepairBlocked ProjectHealth = "review_or_repair_blocked"
	HealthReadyForClosure       ProjectHealth = "ready_for_closure"
	HealthDuplicateOrSuperseded ProjectHealth = "duplicate_or_superseded"
	HealthSourceGap             ProjectHealth = "source_gap"
)

// Nonterminal task states per the HIV-553 contract. 'deferred' is scheduled
// but not yet live; it still counts as a nonterminal frontier.
var nonterminalTaskStatuses = map[string]struct{}{
	"queued":                  {},
	"dispatched":              {},
	"running":                 {},
	"waiting_local_directory": {},
	"deferred":                {},
}

// Nonterminal issue states. done/cancelled are terminal.
var nonterminalIssueStatuses = map[string]struct{}{
	"backlog":     {},
	"todo":        {},
	"in_progress": {},
	"in_review":   {},
	"blocked":     {},
}

// ErrProjectLifecycleNotFound is returned when a project is not in the
// workspace.
var ErrProjectLifecycleNotFound = fmt.Errorf("project lifecycle snapshot not found")

// frozenSupersessions mirrors the HIV-553 Task-linked contract's E
// (duplicate/superseded) disposition. The contract asserted PRJ-HCW-V2 and the
// new OWNER-WORKBENCH point at the same Founder/Workbench canonical authority
// and the owner must decide keep/merge/supersede. Kept as an explicit seed so
// the read model does not invent duplicates from title heuristics.
var frozenSupersessions = map[string]string{
	"1bae6f35-44ae-4052-8c2d-2d2d01638875": "3b0330e7-a2da-4f41-94ab-61c911af2820", // PRJ-HCW-V2 -> OWNER-WORKBENCH
}

// ProjectLifecycleInput is the pre-aggregated, DB-free input to the pure
// classifier. Keeping classification pure makes the contract red-tests run
// without a database.
type ProjectLifecycleInput struct {
	ProjectID             string
	HasLead               bool
	DuplicateOfProjectID  string // non-empty when a frozen supersession applies
	ActiveTaskCount       int
	BlockedIssueCount     int
	ReviewIssueCount      int
	NonterminalIssueCount int
	ConfirmedOutcomeCount int
}

// ProjectLifecycleClassification is the deterministic classification output.
type ProjectLifecycleClassification struct {
	Health                ProjectHealth
	OwnerDecisionRequired bool
	Flags                 []string
	NextAction            string
	ClosureBlockers       []string
}

// ClassifyProject applies the deterministic HIV-553 health rules. It is pure:
// no database, no I/O. The order of the rules matters and is documented inline.
func ClassifyProject(in ProjectLifecycleInput) ProjectLifecycleClassification {
	c := ProjectLifecycleClassification{}

	if !in.HasLead {
		c.OwnerDecisionRequired = true
		c.Flags = append(c.Flags, "owner_decision_required")
		c.ClosureBlockers = append(c.ClosureBlockers, "ACCOUNTABLE_LEAD_REQUIRED")
	}

	// E: frozen duplicate/superseded disposition (contract seed).
	if in.DuplicateOfProjectID != "" {
		c.Health = HealthDuplicateOrSuperseded
		c.OwnerDecisionRequired = true
		c.Flags = append(c.Flags, "duplicate_or_superseded")
		c.NextAction = "owner must decide keep / merge / supersede against the duplicate project"
		c.ClosureBlockers = append(c.ClosureBlockers, "DUPLICATE_AUTHORITY_OWNER_DECISION")
		return c
	}

	// A: real live work beats everything else.
	if in.ActiveTaskCount > 0 {
		c.Health = HealthActiveWithFrontier
		c.Flags = append(c.Flags, "active")
		c.NextAction = fmt.Sprintf("active: %d nonterminal task(s); keep WIP and await receipts", in.ActiveTaskCount)
		return c
	}

	// C: a review/blocked gate with no live task is review_or_repair_blocked.
	//
	// Operationalization note (Quinn review F2, accepted): the frozen HIV-553
	// table judged a few in_review-only projects as B (stalled). Because the
	// structured REVISE/failed-repair signals do not yet exist, this read model
	// conservatively surfaces ANY in_review backlog as C so a review queue is
	// never silently hidden as "stalled". This is a deliberate, fail-closed
	// operationalization, recorded in EVIDENCE (EV-S1-11).
	if in.BlockedIssueCount > 0 {
		c.Health = HealthReviewOrRepairBlocked
		c.Flags = append(c.Flags, "blocked")
		c.NextAction = fmt.Sprintf("blocked: %d blocked issue(s); resolve the block before dispatch", in.BlockedIssueCount)
		c.ClosureBlockers = append(c.ClosureBlockers, "BLOCKED_ISSUES")
		return c
	}
	if in.ReviewIssueCount > 0 {
		c.Health = HealthReviewOrRepairBlocked
		c.Flags = append(c.Flags, "review_backlog")
		c.NextAction = fmt.Sprintf("review backlog: %d in_review issue(s) with no live review task; create a review/disposition task", in.ReviewIssueCount)
		c.ClosureBlockers = append(c.ClosureBlockers, "REVIEW_BACKLOG")
		return c
	}

	// G: all issues terminal but no confirmed outcome — closure evidence
	// cannot be read back, so the project is source_gap, not closable.
	if in.NonterminalIssueCount == 0 && in.ConfirmedOutcomeCount == 0 {
		c.Health = HealthSourceGap
		c.Flags = append(c.Flags, "source_gap")
		c.NextAction = "all issues terminal but no confirmed outcome; map issues to outcomes and generate a closure package"
		c.ClosureBlockers = append(c.ClosureBlockers, "OUTCOME_COVERAGE_INCOMPLETE", "CLOSURE_PACKAGE_MISSING")
		return c
	}

	// B: nonterminal issues remain but no live task and no review/block gate.
	if in.NonterminalIssueCount > 0 {
		c.Health = HealthStalledNoOpenTask
		c.Flags = append(c.Flags, "stalled")
		c.NextAction = fmt.Sprintf("stalled: %d nonterminal issue(s) with no live task; resume the ready frontier or pause explicitly", in.NonterminalIssueCount)
		c.ClosureBlockers = append(c.ClosureBlockers, "ISSUES_WITHOUT_DISPOSITION")
		return c
	}

	// D: every issue terminal and at least one confirmed outcome.
	c.Health = HealthReadyForClosure
	c.Flags = append(c.Flags, "ready_for_closure")
	c.NextAction = "ready for closure: generate the closure package"
	return c
}

// ProjectLifecycleSnapshot is the wire-facing read model for one project.
type ProjectLifecycleSnapshot struct {
	ProjectID             string         `json:"project_id"`
	Status                string         `json:"status"`
	Health                string         `json:"health"`
	OwnerDecisionRequired bool           `json:"owner_decision_required"`
	Flags                 []string       `json:"flags"`
	LeadType              *string        `json:"lead_type"`
	LeadID                *string        `json:"lead_id"`
	FrontierIssueIDs      []string       `json:"frontier_issue_ids"`
	FrontierTasks         []FrontierTask `json:"frontier_tasks"`
	ActiveTaskCount       int            `json:"active_task_count"`
	NonterminalIssueCount int            `json:"nonterminal_issue_count"`
	BlockedIssueCount     int            `json:"blocked_issue_count"`
	ReviewIssueCount      int            `json:"review_issue_count"`
	TerminalIssueCount    int            `json:"terminal_issue_count"`
	LastProgressAt        *string        `json:"last_progress_at"`
	NextAction            string         `json:"next_action"`
	OutcomeConfirmed      int            `json:"outcome_confirmed"`
	OutcomeTotal          int            `json:"outcome_total"`
	ClosureReady          bool           `json:"closure_ready"`
	ClosureBlockers       []string       `json:"closure_blockers"`
	DuplicateOfProjectID  *string        `json:"duplicate_of_project_id"`
}

// FrontierTask is one nonterminal task on the project frontier.
type FrontierTask struct {
	TaskID      string  `json:"task_id"`
	Status      string  `json:"status"`
	AgentID     *string `json:"agent_id"`
	IssueID     *string `json:"issue_id"`
	IssueNumber int32   `json:"issue_number"`
	IssueTitle  string  `json:"issue_title"`
	StartedAt   *string `json:"started_at"`
}

// ProjectLifecycleProjector assembles the derived read model from the existing
// truth tables. It never writes project/issue/task/outcome state.
type ProjectLifecycleProjector struct {
	Queries *db.Queries
}

// NewProjectLifecycleProjector builds the projector from a sqlc Queries handle.
func NewProjectLifecycleProjector(q *db.Queries) *ProjectLifecycleProjector {
	return &ProjectLifecycleProjector{Queries: q}
}

// ListPortfolio returns lifecycle snapshots for every project in the workspace.
func (p *ProjectLifecycleProjector) ListPortfolio(ctx context.Context, workspaceID pgtype.UUID) ([]ProjectLifecycleSnapshot, error) {
	projects, err := p.Queries.ListProjects(ctx, db.ListProjectsParams{WorkspaceID: workspaceID})
	if err != nil {
		return nil, err
	}

	// Issues (workspace-scoped, one pass) grouped by project.
	issues, err := p.Queries.ListIssues(ctx, db.ListIssuesParams{
		WorkspaceID: workspaceID,
		Limit:       100000,
		Offset:      0,
	})
	if err != nil {
		return nil, err
	}
	type issueAgg struct {
		blocked, review, nonterminal, terminal int
	}
	issueByProject := map[string]*issueAgg{}
	for _, is := range issues {
		pid := util.UUIDToString(is.ProjectID)
		if pid == "" {
			continue
		}
		a := issueByProject[pid]
		if a == nil {
			a = &issueAgg{}
			issueByProject[pid] = a
		}
		switch {
		case is.Status == "blocked":
			a.blocked++
		case is.Status == "in_review":
			a.review++
		}
		if _, ok := nonterminalIssueStatuses[is.Status]; ok {
			a.nonterminal++
		} else {
			a.terminal++
		}
	}

	// Active (nonterminal) tasks grouped by project.
	activeTasks, err := p.Queries.ListProjectActiveTasks(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	frontierByProject := map[string][]FrontierTask{}
	activeCountByProject := map[string]int{}
	frontierIssueSet := map[string]map[string]struct{}{}
	for _, t := range activeTasks {
		pid := util.UUIDToString(t.ProjectID)
		if pid == "" {
			continue
		}
		activeCountByProject[pid]++
		ft := FrontierTask{
			TaskID:      util.UUIDToString(t.TaskID),
			Status:      t.TaskStatus,
			AgentID:     uuidOrNil(t.AgentID),
			IssueID:     uuidOrNil(t.IssueID),
			IssueNumber: t.IssueNumber,
			IssueTitle:  t.IssueTitle,
			StartedAt:   timestamptzOrNil(t.StartedAt),
		}
		frontierByProject[pid] = append(frontierByProject[pid], ft)
		if iid := util.UUIDToString(t.IssueID); iid != "" {
			if frontierIssueSet[pid] == nil {
				frontierIssueSet[pid] = map[string]struct{}{}
			}
			frontierIssueSet[pid][iid] = struct{}{}
		}
	}

	// Per-project last successful progress.
	success, err := p.Queries.ListProjectSuccessProgress(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	lastSuccessByProject := map[string]time.Time{}
	for _, s := range success {
		pid := util.UUIDToString(s.ProjectID)
		if pid == "" {
			continue
		}
		if s.LastSuccessAt.Valid {
			lastSuccessByProject[pid] = s.LastSuccessAt.Time
		}
	}

	out := make([]ProjectLifecycleSnapshot, 0, len(projects))
	for _, proj := range projects {
		pid := util.UUIDToString(proj.ID)
		agg := issueByProject[pid]
		var blockedN, reviewN, nonterminalN, terminalN int
		if agg != nil {
			blockedN, reviewN, nonterminalN, terminalN = agg.blocked, agg.review, agg.nonterminal, agg.terminal
		}

		dupOf := frozenSupersessions[pid]
		class := ClassifyProject(ProjectLifecycleInput{
			ProjectID:             pid,
			HasLead:               proj.LeadID.Valid && proj.LeadType.Valid,
			DuplicateOfProjectID:  dupOf,
			ActiveTaskCount:       activeCountByProject[pid],
			BlockedIssueCount:     blockedN,
			ReviewIssueCount:      reviewN,
			NonterminalIssueCount: nonterminalN,
			ConfirmedOutcomeCount: 0, // no confirmed outcomes exist yet (ledger empty)
		})

		// outcome_total must NOT be terminalN: terminal issue disposition is
		// not outcome acceptance (contract). Until the Slice 4 outcome ledger
		// is connected, outcome_total stays 0 and terminal_issue_count carries
		// the honest terminal-count fact separately.

		ft := frontierByProject[pid]
		if ft == nil {
			ft = []FrontierTask{}
		}
		fissueIDs := make([]string, 0, len(frontierIssueSet[pid]))
		for iid := range frontierIssueSet[pid] {
			fissueIDs = append(fissueIDs, iid)
		}
		sort.Strings(fissueIDs)

		var lastProgress *string
		if t, ok := lastSuccessByProject[pid]; ok {
			s := t.UTC().Format(time.RFC3339Nano)
			lastProgress = &s
		}

		var dupOfPtr *string
		if dupOf != "" {
			v := dupOf
			dupOfPtr = &v
		}

		snap := ProjectLifecycleSnapshot{
			ProjectID:             pid,
			Status:                proj.Status,
			Health:                string(class.Health),
			OwnerDecisionRequired: class.OwnerDecisionRequired,
			Flags:                 class.Flags,
			LeadType:              textOrNil(proj.LeadType),
			LeadID:                uuidOrNil(proj.LeadID),
			FrontierIssueIDs:      fissueIDs,
			FrontierTasks:         ft,
			ActiveTaskCount:       activeCountByProject[pid],
			NonterminalIssueCount: nonterminalN,
			BlockedIssueCount:     blockedN,
			ReviewIssueCount:      reviewN,
			TerminalIssueCount:    terminalN,
			LastProgressAt:        lastProgress,
			NextAction:            class.NextAction,
			OutcomeConfirmed:      0,
			OutcomeTotal:          0,
			ClosureReady:          false,
			ClosureBlockers:       class.ClosureBlockers,
			DuplicateOfProjectID:  dupOfPtr,
		}
		out = append(out, snap)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ProjectID < out[j].ProjectID })
	return out, nil
}

// GetSnapshot returns the lifecycle snapshot for a single project, or
// ErrProjectLifecycleNotFound when the project is not in the workspace.
func (p *ProjectLifecycleProjector) GetSnapshot(ctx context.Context, workspaceID, projectID pgtype.UUID) (*ProjectLifecycleSnapshot, error) {
	snaps, err := p.ListPortfolio(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range snaps {
		if snaps[i].ProjectID == util.UUIDToString(projectID) {
			return &snaps[i], nil
		}
	}
	return nil, ErrProjectLifecycleNotFound
}

func textOrNil(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	v := t.String
	return &v
}

func uuidOrNil(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	v := util.UUIDToString(u)
	return &v
}

func timestamptzOrNil(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC().Format(time.RFC3339Nano)
	return &v
}
