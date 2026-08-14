package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/readyfrontier"
	"github.com/multica-ai/multica/server/internal/routescore"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const ContinuousDispatchShadowSchemaV1 = "hivecrew.continuous-dispatch-next-actions/v1"

var (
	ErrContinuousDispatchSourceGap     = errors.New("continuous dispatch source gap")
	ErrContinuousDispatchProjectAbsent = errors.New("continuous dispatch project not found")
)

type ContinuousDispatchShadowStore interface {
	GetProjectInWorkspace(context.Context, db.GetProjectInWorkspaceParams) (db.Project, error)
	CountIssuesByProject(context.Context, pgtype.UUID) (int64, error)
	ListIssues(context.Context, db.ListIssuesParams) ([]db.ListIssuesRow, error)
	ListAllAgents(context.Context, pgtype.UUID) ([]db.Agent, error)
	ListAgentRuntimes(context.Context, pgtype.UUID) ([]db.AgentRuntime, error)
	ListWorkspaceAgentTaskSnapshot(context.Context, pgtype.UUID) ([]db.AgentTaskQueue, error)
	ListTasksByIssue(context.Context, pgtype.UUID) ([]db.AgentTaskQueue, error)
	ListCommentsForIssue(context.Context, db.ListCommentsForIssueParams) ([]db.Comment, error)
}

type ContinuousDispatchEmployeeDirectory interface {
	GetEmployees(context.Context, pgtype.UUID, string, string, int, int) (*EmployeesResult, error)
}

type ContinuousDispatchQuotaSource interface {
	Lookup(context.Context, db.Agent, db.AgentRuntime) (ShadowQuotaSnapshot, error)
}

type ContinuousDispatchLeaseReader interface {
	Read(context.Context, string) (*WriteLease, error)
}

type ShadowQuotaSnapshot struct {
	State      routescore.QuotaState
	CheckedAt  time.Time
	AccountRef string
}

type ContinuousDispatchShadowSources struct {
	Project      bool `json:"project"`
	Organization bool `json:"organization"`
	Runtime      bool `json:"runtime"`
	Tasks        bool `json:"tasks"`
	Quota        bool `json:"quota"`
	WriteLease   bool `json:"write_lease"`
	WIP          bool `json:"wip"`
}

type ContinuousDispatchShadowItem struct {
	IssueID    string `json:"issue_id"`
	IssueTitle string `json:"issue_title"`
	Status     string `json:"status"`
	// SourceTaskID is the completed implementation Task that produced the
	// candidate under review. It is provenance only; clients cannot use it to
	// choose a reviewer or bypass the server-side route planner.
	SourceTaskID     string                                `json:"source_task_id,omitempty"`
	DispatchIdentity continuousdispatch.DispatchIdentity   `json:"dispatch_identity"`
	Generation       continuousdispatch.GenerationEvidence `json:"generation"`
	NextAction       continuousdispatch.NextAction         `json:"next_action"`
}

type ContinuousDispatchShadowResult struct {
	SchemaVersion string                          `json:"schema_version"`
	WorkspaceID   string                          `json:"workspace_id"`
	ProjectID     string                          `json:"project_id"`
	ProjectTitle  string                          `json:"project_title"`
	GeneratedAt   string                          `json:"generated_at"`
	Sources       ContinuousDispatchShadowSources `json:"sources"`
	Items         []ContinuousDispatchShadowItem  `json:"items"`
	Total         int                             `json:"total"`
	Limit         int                             `json:"limit"`
	Offset        int                             `json:"offset"`
}

type continuousDispatchClock interface{ Now() time.Time }

type systemContinuousDispatchClock struct{}

func (systemContinuousDispatchClock) Now() time.Time { return time.Now() }

type ContinuousDispatchShadowService struct {
	store     ContinuousDispatchShadowStore
	directory ContinuousDispatchEmployeeDirectory
	quota     ContinuousDispatchQuotaSource
	leases    ContinuousDispatchLeaseReader
	planner   *continuousdispatch.Planner
	clock     continuousDispatchClock
}

func NewContinuousDispatchShadowService(
	store ContinuousDispatchShadowStore,
	directory ContinuousDispatchEmployeeDirectory,
	quota ContinuousDispatchQuotaSource,
	leases ContinuousDispatchLeaseReader,
) *ContinuousDispatchShadowService {
	return &ContinuousDispatchShadowService{
		store: store, directory: directory, quota: quota, leases: leases,
		planner: continuousdispatch.NewPlanner(), clock: systemContinuousDispatchClock{},
	}
}

func (s *ContinuousDispatchShadowService) WithClock(clock continuousDispatchClock) *ContinuousDispatchShadowService {
	cp := *s
	cp.clock = clock
	cp.planner = continuousdispatch.NewPlanner().WithScorer(routescore.NewScorer(nil).WithClock(clock))
	return &cp
}

func (s *ContinuousDispatchShadowService) InspectProject(
	ctx context.Context,
	workspaceID pgtype.UUID,
	projectID pgtype.UUID,
	limit int,
	offset int,
) (*ContinuousDispatchShadowResult, error) {
	if s == nil || s.store == nil || s.directory == nil {
		return nil, fmt.Errorf("%w: required adapter unavailable", ErrContinuousDispatchSourceGap)
	}
	project, err := s.store.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrContinuousDispatchProjectAbsent
		}
		return nil, fmt.Errorf("read project: %w", err)
	}
	employees, err := s.directory.GetEmployees(ctx, workspaceID, "", "", 500, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: organization directory unavailable", ErrContinuousDispatchSourceGap)
	}
	issues, err := s.store.ListIssues(ctx, db.ListIssuesParams{
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
		Limit:       int32(limit),
		Offset:      int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("read project issues: %w", err)
	}
	total, err := s.store.CountIssuesByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count project issues: %w", err)
	}
	agents, err := s.store.ListAllAgents(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}
	runtimes, err := s.store.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("read runtimes: %w", err)
	}
	snapshot, err := s.store.ListWorkspaceAgentTaskSnapshot(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("read task snapshot: %w", err)
	}

	now := s.clock.Now().UTC()
	agentsByID := map[string]db.Agent{}
	for _, agent := range agents {
		agentsByID[shadowUUIDString(agent.ID)] = agent
	}
	runtimesByID := map[string]db.AgentRuntime{}
	for _, runtime := range runtimes {
		runtimesByID[shadowUUIDString(runtime.ID)] = runtime
	}
	activeWIP := map[string]int{}
	for _, task := range snapshot {
		switch task.Status {
		case "dispatched", "running", "waiting_local_directory":
			activeWIP[shadowUUIDString(task.AgentID)]++
		}
	}
	wip := composeShadowWIP(agents, runtimesByID, snapshot, now)
	candidates, preferredByAgent, quotaComplete := s.buildCandidates(ctx, employees.Items, agentsByID, runtimesByID, activeWIP)

	items := make([]ContinuousDispatchShadowItem, 0, len(issues))
	leaseComplete := true
	for _, issue := range issues {
		tasks, taskErr := s.store.ListTasksByIssue(ctx, issue.ID)
		if taskErr != nil {
			return nil, fmt.Errorf("read issue tasks: %w", taskErr)
		}
		metadata := parseShadowMetadata(issue.Metadata)
		identity := continuousdispatch.DispatchIdentity{
			WorkspaceID:       shadowUUIDString(workspaceID),
			IssueID:           shadowUUIDString(issue.ID),
			Stage:             metadata.Stage,
			CandidateRevision: metadata.CandidateRevision,
			Generation:        metadata.Generation,
		}
		generation := composeGeneration(identity, tasks)
		lease, leaseKnown := s.composeLease(ctx, metadata.WriteMutexKey, now)
		leaseComplete = leaseComplete && leaseKnown
		frontier := composeFrontier(issue, tasks, agentsByID, runtimesByID, activeWIP, now)
		requirement := composeRequirement(issue)
		lineage := reviewSourceLineage{}
		if requirement.NeedsReview {
			comments, commentErr := s.store.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
				IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Limit: 2000,
			})
			if commentErr != nil {
				return nil, fmt.Errorf("read review source comments: %w", commentErr)
			}
			lineage = resolveReviewSourceLineage(issue, tasks, comments, identity)
		}
		issueCandidates := append([]continuousdispatch.Candidate(nil), candidates...)
		if requirement.NeedsReview {
			for i := range issueCandidates {
				issueCandidates[i].Route.IsAuthor = lineage.AuthorID != "" && issueCandidates[i].Route.AgentID.String() == lineage.AuthorID
			}
		}
		preferredEmployeeID := metadata.PreferredEmployeeID
		if preferredEmployeeID == "" && issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" {
			preferredEmployeeID = preferredByAgent[shadowUUIDString(issue.AssigneeID)]
		}
		next := s.planner.Plan(continuousdispatch.Input{
			Frontier:            frontier,
			Requirement:         requirement,
			Candidates:          issueCandidates,
			PreferredEmployeeID: preferredEmployeeID,
			RequiredBaseID:      metadata.RequiredBaseID,
			Generation:          generation,
			WIP:                 wip,
			Lease:               lease,
			ReviewAuthorKnown:   !requirement.NeedsReview || lineage.Proven,
		})
		items = append(items, ContinuousDispatchShadowItem{
			IssueID: shadowUUIDString(issue.ID), IssueTitle: issue.Title, Status: issue.Status,
			SourceTaskID:     lineage.TaskID,
			DispatchIdentity: identity, Generation: generation, NextAction: next,
		})
	}

	return &ContinuousDispatchShadowResult{
		SchemaVersion: ContinuousDispatchShadowSchemaV1,
		WorkspaceID:   shadowUUIDString(workspaceID), ProjectID: shadowUUIDString(projectID), ProjectTitle: project.Title,
		GeneratedAt: now.Format(time.RFC3339Nano),
		Sources: ContinuousDispatchShadowSources{
			Project: true, Organization: true, Runtime: true, Tasks: true,
			Quota: quotaComplete, WriteLease: leaseComplete, WIP: wip.Known && wip.Reconciled,
		},
		Items: items, Total: int(total), Limit: limit, Offset: offset,
	}, nil
}

type reviewSourceLineage struct {
	TaskID   string
	AuthorID string
	Proven   bool
}

// resolveReviewSourceLineage accepts only a current agent Comment that points
// through source_task_id to the completed implementation Task for this exact
// Issue/candidate generation. A completed Task by itself is never sufficient:
// that could be an old attempt or a previous review. The current Issue must
// be in the distinct review stage, while the source Task must be stamped as
// implementation with the same workspace/Issue/revision/generation. This
// gives implementation and review independent receipt identities.
func resolveReviewSourceLineage(
	issue db.ListIssuesRow,
	tasks []db.AgentTaskQueue,
	comments []db.Comment,
	identity continuousdispatch.DispatchIdentity,
) reviewSourceLineage {
	if identity.Stage != "review" {
		return reviewSourceLineage{}
	}
	tasksByID := make(map[string]db.AgentTaskQueue, len(tasks))
	for _, task := range tasks {
		if task.ID.Valid {
			tasksByID[shadowUUIDString(task.ID)] = task
		}
	}
	for i := len(comments) - 1; i >= 0; i-- {
		comment := comments[i]
		if comment.AuthorType != "agent" || !comment.SourceTaskID.Valid || !comment.AuthorID.Valid ||
			comment.IssueID != issue.ID || comment.WorkspaceID != issue.WorkspaceID {
			continue
		}
		task, ok := tasksByID[shadowUUIDString(comment.SourceTaskID)]
		if !ok || task.Status != "completed" || !task.IssueID.Valid || task.IssueID != issue.ID ||
			!task.AgentID.Valid || task.AgentID != comment.AuthorID ||
			(task.HandoffNote.Valid && strings.HasPrefix(task.HandoffNote.String, "review_dispatch ")) {
			continue
		}
		var contextValue shadowTaskContext
		if len(task.Context) == 0 || json.Unmarshal(task.Context, &contextValue) != nil ||
			!contextValue.ContinuousDispatch.Complete() || contextValue.ContinuousDispatch.Stage != "implementation" ||
			contextValue.ContinuousDispatch.WorkspaceID != identity.WorkspaceID ||
			contextValue.ContinuousDispatch.IssueID != identity.IssueID ||
			contextValue.ContinuousDispatch.CandidateRevision != identity.CandidateRevision ||
			contextValue.ContinuousDispatch.Generation != identity.Generation {
			continue
		}
		return reviewSourceLineage{TaskID: shadowUUIDString(task.ID), AuthorID: shadowUUIDString(task.AgentID), Proven: true}
	}
	return reviewSourceLineage{}
}

func (s *ContinuousDispatchShadowService) buildCandidates(
	ctx context.Context,
	employees []companyopsapi.PublicEmployeeSummary,
	agents map[string]db.Agent,
	runtimes map[string]db.AgentRuntime,
	activeWIP map[string]int,
) ([]continuousdispatch.Candidate, map[string]string, bool) {
	candidates := make([]continuousdispatch.Candidate, 0, len(employees))
	employeeByAgent := make(map[string]string, len(employees))
	quotaComplete := s.quota != nil
	for _, employee := range employees {
		if employee.Availability != companyopsapi.AvailabilityAvailable || employee.LocalAgent == nil || employee.HiveCrewAgentID == "" {
			continue
		}
		agent, ok := agents[employee.HiveCrewAgentID]
		// Archived agents are no longer executable workforce candidates. The
		// directory/authority projection may still contain a stale employee
		// row, so this service must fail closed locally instead of relying on
		// that projection to filter the archived state.
		if !ok || agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
			continue
		}
		runtime, ok := runtimes[shadowUUIDString(agent.RuntimeID)]
		if !ok {
			continue
		}
		quota := ShadowQuotaSnapshot{State: routescore.QuotaUnknown}
		if s.quota != nil {
			var err error
			quota, err = s.quota.Lookup(ctx, agent, runtime)
			if err != nil {
				quota = ShadowQuotaSnapshot{State: routescore.QuotaUnknown}
				quotaComplete = false
			}
		}
		agentID, agentOK := uuid.FromBytes(agent.ID.Bytes[:])
		runtimeID, runtimeOK := uuid.FromBytes(runtime.ID.Bytes[:])
		if agentOK != nil || runtimeOK != nil {
			continue
		}
		baseID := ""
		baseKnown := runtime.DaemonID.Valid && strings.TrimSpace(runtime.DaemonID.String) != ""
		if baseKnown {
			baseID = strings.TrimSpace(runtime.DaemonID.String)
		}
		model := ""
		if agent.Model.Valid {
			model = agent.Model.String
		}
		maxWIP := int(agent.MaxConcurrentTasks)
		candidate := continuousdispatch.Candidate{
			EmployeeID: employee.EmployeeID,
			Model:      model, AccountRef: quota.AccountRef, BaseID: baseID, BaseKnown: baseKnown,
			WIPKnown: maxWIP > 0, ActiveWIP: activeWIP[employee.HiveCrewAgentID], MaxWIP: maxWIP,
			Route: routescore.Candidate{
				AgentID: agentID, AgentName: agent.Name, Roles: shadowRoles(employee), RuntimeID: runtimeID,
				RuntimeHealth: shadowRuntimeHealth(runtime), Quota: quota.State, QuotaCheckedAt: quota.CheckedAt,
			},
		}
		candidates = append(candidates, candidate)
		employeeByAgent[employee.HiveCrewAgentID] = employee.EmployeeID
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].EmployeeID < candidates[j].EmployeeID })
	return candidates, employeeByAgent, quotaComplete
}

type shadowIssueMetadata struct {
	Stage               string `json:"stage"`
	Generation          string `json:"generation"`
	CandidateRevision   string `json:"candidate_revision"`
	PreferredEmployeeID string `json:"preferred_employee_id"`
	RequiredBaseID      string `json:"required_base_id"`
	WriteMutexKey       string `json:"write_mutex_key"`
	ReviewState         string `json:"review_state"`
}

func parseShadowMetadata(raw []byte) shadowIssueMetadata {
	var value shadowIssueMetadata
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return shadowIssueMetadata{}
	}
	value.Stage = strings.TrimSpace(value.Stage)
	value.Generation = strings.TrimSpace(value.Generation)
	value.CandidateRevision = strings.TrimSpace(value.CandidateRevision)
	value.PreferredEmployeeID = strings.TrimSpace(value.PreferredEmployeeID)
	value.RequiredBaseID = strings.TrimSpace(value.RequiredBaseID)
	value.WriteMutexKey = strings.TrimSpace(value.WriteMutexKey)
	value.ReviewState = strings.TrimSpace(value.ReviewState)
	return value
}

type shadowTaskContext struct {
	ContinuousDispatch continuousdispatch.DispatchIdentity `json:"continuous_dispatch"`
}

func composeGeneration(identity continuousdispatch.DispatchIdentity, tasks []db.AgentTaskQueue) continuousdispatch.GenerationEvidence {
	matching := make([]db.AgentTaskQueue, 0, 1)
	conflictingOrUnattributed := 0
	for _, task := range tasks {
		switch task.Status {
		case "queued", "dispatched", "running", "waiting_local_directory":
			var contextValue shadowTaskContext
			if len(task.Context) == 0 || json.Unmarshal(task.Context, &contextValue) != nil ||
				!contextValue.ContinuousDispatch.Complete() || contextValue.ContinuousDispatch != identity {
				conflictingOrUnattributed++
				continue
			}
			matching = append(matching, task)
		}
	}
	evidence := continuousdispatch.GenerationEvidence{
		Known:                 identity.Complete(),
		DuplicateUnattributed: conflictingOrUnattributed > 0 || len(matching) > 1,
	}
	if len(matching) == 1 && !evidence.DuplicateUnattributed {
		evidence.OpenTaskID = shadowUUIDString(matching[0].ID)
	}
	return evidence
}

func composeShadowWIP(agents []db.Agent, runtimes map[string]db.AgentRuntime, tasks []db.AgentTaskQueue, now time.Time) continuousdispatch.WIPTruthEvidence {
	evidence := continuousdispatch.WIPTruthEvidence{Required: true, Known: true, Reconciled: true, ProjectionAvailable: len(agents) > 0}
	for _, task := range tasks {
		switch task.Status {
		case "queued", "dispatched", "running", "waiting_local_directory", "completed", "failed":
		default:
			evidence.UnknownRows++
		}
		if !task.AgentID.Valid || ((task.Status == "dispatched" || task.Status == "running" || task.Status == "waiting_local_directory") && !task.RuntimeID.Valid) {
			evidence.UnknownRows++
		}
	}
	for _, agent := range agents {
		if (agent.Status != "idle" && agent.Status != "working") || !agent.RuntimeID.Valid {
			evidence.UnknownWorkers++
			continue
		}
		runtime, ok := runtimes[shadowUUIDString(agent.RuntimeID)]
		if !ok || !runtime.LastSeenAt.Valid || now.Sub(runtime.LastSeenAt.Time) > 5*time.Minute {
			evidence.UnknownWorkers++
		}
	}
	return evidence
}

func composeFrontier(issue db.ListIssuesRow, tasks []db.AgentTaskQueue, agents map[string]db.Agent, runtimes map[string]db.AgentRuntime, activeWIP map[string]int, now time.Time) readyfrontier.IssueInput {
	metadata := parseShadowMetadata(issue.Metadata)
	in := readyfrontier.IssueInput{Status: issue.Status, ReviewState: metadata.ReviewState, CapacityKnown: false}
	// ListTasksByIssue is newest first. Only the newest attempt may determine
	// the frontier: an older failed run must not mask a later completed run.
	if len(tasks) > 0 {
		task := tasks[0]
		switch task.Status {
		case "queued", "dispatched", "running", "waiting_local_directory", "failed":
			in.HasTask = true
			in.TaskStatus = task.Status
			in.PrepareLeaseExpired = task.PrepareLeaseExpiresAt.Valid && task.PrepareLeaseExpiresAt.Time.Before(now)
		}
	}
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return in
	}
	in.HasAssignee = true
	if issue.AssigneeType.String != "agent" {
		return in
	}
	agent, ok := agents[shadowUUIDString(issue.AssigneeID)]
	if !ok {
		in.AgentArchived = true
		return in
	}
	in.AgentArchived = agent.ArchivedAt.Valid
	in.RuntimeBound = agent.RuntimeID.Valid
	if !agent.RuntimeID.Valid {
		return in
	}
	runtime, ok := runtimes[shadowUUIDString(agent.RuntimeID)]
	in.RuntimeOnline = ok && runtime.Status == "online"
	if agent.MaxConcurrentTasks > 0 {
		in.CapacityKnown = true
		in.CapacityFree = activeWIP[shadowUUIDString(agent.ID)] < int(agent.MaxConcurrentTasks)
	}
	return in
}

func composeRequirement(issue db.ListIssuesRow) routescore.TaskRequirement {
	review := issue.Status == "in_review"
	requirement := routescore.TaskRequirement{RequiredRoles: []string{"implementation"}, MaxLatencyMs: 10 * 60 * 1000, MaxCostUSD: 1}
	if review {
		requirement.RequiredRoles = []string{"code_review", "independent_test_review"}
		requirement.NeedsReview = true
	}
	return requirement
}

func (s *ContinuousDispatchShadowService) composeLease(ctx context.Context, mutexKey string, now time.Time) (continuousdispatch.LeaseEvidence, bool) {
	if mutexKey == "" {
		return continuousdispatch.LeaseEvidence{Known: true, Available: true}, true
	}
	evidence := continuousdispatch.LeaseEvidence{Required: true}
	if s.leases == nil {
		return evidence, false
	}
	lease, err := s.leases.Read(ctx, mutexKey)
	if errors.Is(err, ErrLeaseNotFound) {
		evidence.Known, evidence.Available = true, true
		return evidence, true
	}
	if err != nil {
		return evidence, false
	}
	evidence.Known = true
	evidence.LeaseID = lease.ID.String()
	held := lease.Status == WriteLeaseHeld && lease.ExpiresAt != nil && lease.ExpiresAt.After(now)
	evidence.Available = !held
	return evidence, true
}

func shadowRuntimeHealth(runtime db.AgentRuntime) routescore.RuntimeStatus {
	switch runtime.Status {
	case "online":
		return routescore.RuntimeOnline
	case "degraded":
		return routescore.RuntimeDegraded
	case "offline":
		return routescore.RuntimeOffline
	default:
		return routescore.RuntimeUnresponsive
	}
}

func shadowRoles(employee companyopsapi.PublicEmployeeSummary) []string {
	value := strings.ToLower(employee.PositionID + " " + employee.PositionTitle)
	roles := []string{strings.ToLower(strings.TrimSpace(employee.PositionID))}
	for _, candidate := range []struct {
		needle string
		role   string
	}{
		{"全栈", "implementation"}, {"工程师", "implementation"}, {"开发", "implementation"},
		{"审核", "code_review"}, {"审查", "code_review"}, {"质量", "code_review"},
		{"测试", "independent_test_review"}, {"返修", "repair_integration"}, {"集成", "repair_integration"},
		{"运维", "operations"}, {"调度", "dispatch_management"}, {"管理", "dispatch_management"},
	} {
		if strings.Contains(value, candidate.needle) {
			roles = append(roles, candidate.role)
		}
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, len(roles))
	for _, role := range roles {
		if role == "" {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		result = append(result, role)
	}
	return result
}

func shadowUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}
