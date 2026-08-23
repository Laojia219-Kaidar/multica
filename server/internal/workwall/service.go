package workwall

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EmployeeDirectory is the narrow CompanyOps Employee directory read seam the
// Work Wall reuses for formal Employee identity (HIV-854). It is exactly the
// existing public directory service surface — no second directory API is
// created and no CompanyOps source file is modified. Production binds
// *service.CompanyOpsDirectoryService (compile-time proof below); tests stub
// the same two methods.
type EmployeeDirectory interface {
	// GetEmployees returns one page of public employee summaries. The Work
	// Wall passes empty q / availability filters and pages by offset.
	GetEmployees(ctx context.Context, workspaceID pgtype.UUID, q string, availabilityFilter string, limit int, offset int) (*service.EmployeesResult, error)
	// GetWorkforceBaseRuntimeJoin returns the strict Employee→Agent→Runtime→
	// Base join rows for the whole workspace in one read.
	GetWorkforceBaseRuntimeJoin(ctx context.Context, workspaceID pgtype.UUID) (companyopsapi.PublicAuthorityRef, []service.WorkforceBaseRuntimeRow, error)
}

// Compile-time proof that the production handler binds the real CompanyOps
// directory service onto the seam without any wrapper layer.
var _ EmployeeDirectory = (*service.CompanyOpsDirectoryService)(nil)

const (
	// directoryPageSize mirrors the maximum page size the existing CompanyOps
	// employees endpoint accepts (handler limit 1..500). The Work Wall always
	// requests full pages and advances by offset until the reported total.
	directoryPageSize = 500
	// directoryMaxPages bounds the employee paging loop (100 pages × 500 =
	// 50,000 employees). Past the bound the read is treated as inconsistent
	// and fails closed as an authority gap instead of looping forever.
	directoryMaxPages = 100
)

// errDirectoryReadFailed is the internal sentinel for a contract-violating
// directory page (nil result, oversized page). Like every directory error it
// is classified, never surfaced: the Work Wall exposes only the sanitized
// gap state, so no raw error text can reach a card or a response.
var errDirectoryReadFailed = errors.New("companyops directory read failed")

// Service assembles the workspace-wide work-wall snapshot from existing
// HiveCrew read models. It performs no writes and adds no schema.
//
// Directory is the optional CompanyOps Employee directory seam. When it is
// absent (adapter not configured at startup) every card fails closed as an
// authority gap: cards stay visible, formal Employee fields stay cleared, and
// freshness cannot look `fresh`.
type Service struct {
	Q              *db.Queries
	Directory      EmployeeDirectory
	Now            func() time.Time
	StaleThreshold time.Duration
}

// NewService binds the snapshot service to the generated queries and to the
// CompanyOps directory service when one was configured at startup. A nil
// directory is valid: the Work Wall then degrades every card to the
// authority-gap state instead of fabricating Employee identity.
func NewService(q *db.Queries, directory *service.CompanyOpsDirectoryService) *Service {
	var directorySeam EmployeeDirectory
	if directory != nil {
		directorySeam = directory
	}
	return &Service{Q: q, Directory: directorySeam, Now: time.Now, StaleThreshold: defaultStaleThreshold}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) threshold() time.Duration {
	if s.StaleThreshold <= 0 {
		return defaultStaleThreshold
	}
	return s.StaleThreshold
}

// employeeAuthorityIndex is the per-snapshot result of one directory read
// pass: the resolved Employee authority evidence for every Agent the
// authority names.
type employeeAuthorityIndex struct {
	// unavailable marks a whole-read failure (seam absent, transport error,
	// malformed authority response, empty authoritative workforce, or
	// truncated/inconsistent paging). Every card then fails closed as an
	// authority gap — the read never partially overlays.
	unavailable bool
	// byAgent carries the per-agent evidence; absence means no authority row
	// names that Agent (a legitimate Agent-only card).
	byAgent map[string]*EmployeeAuthority
}

// forAgent returns the authority evidence for one Agent card. nil means an
// Agent-only card with healthy authority; a gap value means failed evidence.
func (i employeeAuthorityIndex) forAgent(agentID string) *EmployeeAuthority {
	if i.unavailable {
		return &EmployeeAuthority{State: EmployeeAuthorityGap}
	}
	return i.byAgent[agentID]
}

// Snapshot returns one EmployeeLiveActivityV1 per user-authored agent in the
// workspace. Read-only; sources: agent, agent_runtime, agent_task_queue, plus
// the execution-chain reads (workspace, issue, project, runtime_profile,
// execution_receipt) that hydrate the card's Project/Issue/Run/Receipt/Profile
// identifiers, and one CompanyOps directory pass that resolves the formal
// Employee identity. Every chain lookup stays workspace-scoped.
func (s *Service) Snapshot(ctx context.Context, workspaceID pgtype.UUID) ([]liveactivity.EmployeeLiveActivityV1, error) {
	agents, err := s.Q.ListAgents(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	runtimes, err := s.Q.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	tasks, err := s.Q.ListWorkspaceAgentTaskSnapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	chainReads := chainStoreFor(s.Q)
	issuePrefix, err := resolveIssuePrefix(ctx, chainReads, workspaceID)
	if err != nil {
		return nil, err
	}

	// Employee authority resolution (HIV-854): one workforce-join read plus
	// paged employee summaries per snapshot resolves the formal identity for
	// every card at once — no per-agent directory reads. A nil or failing
	// seam degrades every card to the authority-gap state; it never removes
	// a card and never fabricates identity.
	var dirAuth employeeAuthorityIndex
	if len(agents) > 0 {
		dirAuth = s.resolveEmployeeAuthority(ctx, workspaceID)
	}

	rtByID := make(map[string]*db.AgentRuntime, len(runtimes))
	for i := range runtimes {
		rtByID[uuidStr(runtimes[i].ID)] = &runtimes[i]
	}

	activeByAgent := make(map[string]*db.AgentTaskQueue)
	outcomeByAgent := make(map[string]*db.AgentTaskQueue)
	for i := range tasks {
		t := &tasks[i]
		aid := uuidStr(t.AgentID)
		if isActiveTaskStatus(t.Status) {
			// Deterministic priority selection (HIV-869, R3 repair):
			// running > dispatched > waiting_local_directory > queued,
			// then lexicographically smaller UUID wins. Terminal statuses
			// (completed/failed/cancelled) never become the active task.
			activeByAgent[aid] = selectActiveTask(activeByAgent[aid], t)
		} else {
			outcomeByAgent[aid] = t
		}
	}

	activityByAgent := make(map[string][]db.ActivityLog)
	for aid, t := range activeByAgent {
		if !t.IssueID.Valid {
			continue
		}
		acts, err := s.Q.ListActivitiesForIssue(ctx, db.ListActivitiesForIssueParams{
			IssueID: t.IssueID,
			Limit:   5,
		})
		if err != nil {
			return nil, err
		}
		activityByAgent[aid] = acts
	}

	now := s.now()
	out := make([]liveactivity.EmployeeLiveActivityV1, 0, len(agents))
	for i := range agents {
		a := agents[i]
		aid := uuidStr(a.ID)
		rt := rtByID[uuidStr(a.RuntimeID)]

		// Hydrate the chain for the task the card shows: the active task
		// when working/queued, otherwise the most recent terminal task.
		shown := activeByAgent[aid]
		if shown == nil {
			shown = outcomeByAgent[aid]
		}
		chain, err := resolveExecutionChain(ctx, chainReads, workspaceID, issuePrefix, rt, shown)
		if err != nil {
			return nil, err
		}

		out = append(out, AssembleAgentCard(
			a,
			rt,
			activeByAgent[aid],
			outcomeByAgent[aid],
			chain,
			dirAuth.forAgent(aid),
			activityByAgent[aid],
			now,
			s.threshold(),
		))
	}
	return out, nil
}

// taskActivePriority maps an active task status to its selection priority
// (higher = preferred). The deterministic ordering is:
// running > dispatched > waiting_local_directory > queued.
func taskActivePriority(status string) int {
	switch status {
	case "running":
		return 4
	case "dispatched":
		return 3
	case "waiting_local_directory":
		return 2
	case "queued":
		return 1
	default:
		return 0
	}
}

// selectActiveTask returns the better of the current holder and a challenger
// for the per-agent active-task slot. The challenger wins only when it has a
// strictly higher active priority, or the same priority and a
// lexicographically smaller UUID (stable deterministic tie-break regardless
// of input order). A nil holder always loses to any challenger.
func selectActiveTask(holder, challenger *db.AgentTaskQueue) *db.AgentTaskQueue {
	if holder == nil {
		return challenger
	}
	hp := taskActivePriority(holder.Status)
	cp := taskActivePriority(challenger.Status)
	if cp > hp {
		return challenger
	}
	if cp == hp && uuidStr(challenger.ID) < uuidStr(holder.ID) {
		return challenger
	}
	return holder
}

// resolveEmployeeAuthority performs one CompanyOps directory pass and resolves
// the Employee authority evidence per Agent. Fail-closed rules:
//
//   - A nil seam, a failed workforce-join read, a failed or truncated
//     employee page read, or an explicitly empty authoritative workforce
//     marks the whole index unavailable: every card degrades to the
//     authority-gap state (Agent card preserved, formal fields cleared,
//     never `fresh`).
//   - Rows are never filtered before the ambiguity evaluation: a valid row
//     plus any malformed or duplicated row naming the same Agent fails that
//     Agent closed.
//   - An overlay is applied only when the workforce-join row and the employee
//     summary row pair exactly (same Employee ID, same canonical Agent ID)
//     and every formal field is complete and canonical.
//
// Raw errors are classified and dropped here; nothing but the sanitized gap
// state ever leaves this function.
func (s *Service) resolveEmployeeAuthority(ctx context.Context, workspaceID pgtype.UUID) employeeAuthorityIndex {
	index := employeeAuthorityIndex{byAgent: make(map[string]*EmployeeAuthority)}
	if s.Directory == nil {
		index.unavailable = true
		return index
	}

	_, joinRows, err := s.Directory.GetWorkforceBaseRuntimeJoin(ctx, workspaceID)
	if err != nil {
		index.unavailable = true
		return index
	}
	summaries, complete, err := s.readEmployeeSummaries(ctx, workspaceID)
	if err != nil || !complete {
		index.unavailable = true
		return index
	}

	// Collect every row that names an Agent, including malformed ones: the
	// presence of extra evidence is exactly what must fail an Agent closed.
	joinByAgent := make(map[string][]service.WorkforceBaseRuntimeRow)
	for _, row := range joinRows {
		aid := strings.TrimSpace(row.HiveCrewAgentID)
		if aid == "" {
			// A join row without an executable HiveCrew agent binding carries
			// no Agent evidence at all (the seam leaves HiveCrewAgentID empty
			// for every non-available employee).
			continue
		}
		joinByAgent[aid] = append(joinByAgent[aid], row)
	}
	summaryByAgent := make(map[string][]companyopsapi.PublicEmployeeSummary)
	for _, summary := range summaries {
		aid := strings.TrimSpace(summary.HiveCrewAgentID)
		if aid == "" {
			continue
		}
		summaryByAgent[aid] = append(summaryByAgent[aid], summary)
	}

	agentIDs := make([]string, 0, len(joinByAgent)+len(summaryByAgent))
	for aid := range joinByAgent {
		agentIDs = append(agentIDs, aid)
	}
	for aid := range summaryByAgent {
		if _, seen := joinByAgent[aid]; !seen {
			agentIDs = append(agentIDs, aid)
		}
	}
	sort.Strings(agentIDs)

	for _, aid := range agentIDs {
		joinRows, summaryRows := joinByAgent[aid], summaryByAgent[aid]
		switch {
		case len(joinRows) > 1 || len(summaryRows) > 1:
			// Duplicated or conflicting evidence names this Agent: never pick
			// the valid-looking row, fail the Agent closed.
			index.byAgent[aid] = &EmployeeAuthority{State: EmployeeAuthorityGap}
		case len(joinRows) == 1 && len(summaryRows) == 1:
			if identity := stitchEmployeeIdentity(joinRows[0], summaryRows[0]); identity != nil {
				index.byAgent[aid] = &EmployeeAuthority{State: EmployeeAuthorityVerified, Identity: identity}
			} else {
				// Complete pairing impossible: incomplete fields or an
				// exact-match conflict between the two reads.
				index.byAgent[aid] = &EmployeeAuthority{State: EmployeeAuthorityGap}
			}
		default:
			// One-sided evidence (only the join or only the summary names the
			// Agent): incomplete, fail closed.
			index.byAgent[aid] = &EmployeeAuthority{State: EmployeeAuthorityGap}
		}
	}
	return index
}

// readEmployeeSummaries pages through the CompanyOps employee summaries with
// the contract the existing endpoint already supports (limit ≤ 500, offset
// paging against the reported Total). It returns complete=false when the
// pages cannot cover the reported Total — the read is then truncated and the
// caller fails the whole authority resolution closed instead of overlaying a
// partial roster.
func (s *Service) readEmployeeSummaries(ctx context.Context, workspaceID pgtype.UUID) ([]companyopsapi.PublicEmployeeSummary, bool, error) {
	var all []companyopsapi.PublicEmployeeSummary
	collected := 0
	reportedTotal := 0
	for page := 0; page < directoryMaxPages; page++ {
		result, err := s.Directory.GetEmployees(ctx, workspaceID, "", "", directoryPageSize, collected)
		if err != nil {
			return nil, false, err
		}
		if result == nil {
			return nil, false, errDirectoryReadFailed
		}
		if len(result.Items) > directoryPageSize {
			// A page larger than the requested limit violates the endpoint
			// contract; treat the read as inconsistent and fail closed.
			return nil, false, errDirectoryReadFailed
		}
		all = append(all, result.Items...)
		collected += len(result.Items)
		reportedTotal = result.Total
		if len(result.Items) == 0 {
			break
		}
		if reportedTotal > 0 && collected >= reportedTotal {
			break
		}
	}
	// Truncation boundary: the pages must cover the authority's own reported
	// Total (and a non-empty page stream cannot coexist with a zero Total).
	if reportedTotal > collected {
		return nil, false, errDirectoryReadFailed
	}
	if collected > 0 && reportedTotal <= 0 {
		return nil, false, errDirectoryReadFailed
	}
	return all, true, nil
}

// stitchEmployeeIdentity pairs one workforce-join row with one employee
// summary row into formal Employee identity evidence. It returns nil unless
// the pairing is complete, exact and unambiguous: identical canonical Agent
// IDs, identical canonical Employee IDs, and every formal field non-blank and
// canonical (no padding/whitespace). The verified Base is the join row's
// observed machine title; no Base registry ID exists on this read, so none is
// invented.
func stitchEmployeeIdentity(row service.WorkforceBaseRuntimeRow, summary companyopsapi.PublicEmployeeSummary) *EmployeeIdentity {
	canonical := func(value string) bool {
		return value != "" && value == strings.TrimSpace(value)
	}
	if !canonical(row.HiveCrewAgentID) || !canonical(summary.HiveCrewAgentID) ||
		row.HiveCrewAgentID != summary.HiveCrewAgentID {
		return nil
	}
	if !canonical(row.EmployeeID) || !canonical(summary.EmployeeID) ||
		row.EmployeeID != summary.EmployeeID {
		return nil
	}
	if !canonical(summary.DisplayName) ||
		!canonical(summary.DepartmentID) || !canonical(summary.DepartmentName) ||
		!canonical(summary.PositionID) || !canonical(summary.PositionTitle) ||
		!canonical(row.BaseMachineTitle) {
		return nil
	}
	return &EmployeeIdentity{
		EmployeeID:       row.EmployeeID,
		DisplayName:      summary.DisplayName,
		DepartmentID:     summary.DepartmentID,
		DepartmentName:   summary.DepartmentName,
		PositionID:       summary.PositionID,
		PositionTitle:    summary.PositionTitle,
		BaseMachineTitle: row.BaseMachineTitle,
	}
}
