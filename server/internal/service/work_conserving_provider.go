package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/routescore"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"gopkg.in/yaml.v3"
)

const (
	workConservingGoalSourceMaxBytes = 2 << 20
	workConservingProjectionTTL      = 15 * time.Minute
	workConservingPageSize           = 200
	workConservingEmployeePageSize   = 500
)

// WorkConservingGoalSource is the explicit Goal/CHECKLIST binding consumed by
// the projection. It is a read-only execution-status source, not a HiveCrew
// company-object authority. The nested binding is mandatory; top-level Goal
// metadata must never silently become a production routing authority.
type WorkConservingGoalSource struct {
	SchemaVersion           string                           `yaml:"schema_version"`
	WorkConservingAuthority *WorkConservingGoalSourceBinding `yaml:"work_conserving_authority"`
}

type WorkConservingGoalSourceBinding struct {
	SchemaVersion string `yaml:"schema_version"`
	GoalID        string `yaml:"goal_id"`
	WorkspaceID   string `yaml:"workspace_id"`
	ProjectID     string `yaml:"project_id"`
	SourceRef     string `yaml:"source_ref"`
}

type workConservingGoalSnapshot struct {
	Binding WorkConservingGoalSourceBinding
	Digest  string
}

// FileWorkConservingProjectionProvider computes a complete, read-only plan
// from one explicit Goal source and current HiveCrew execution read models.
// It has no command, repository writer, queue, or lease writer.
type FileWorkConservingProjectionProvider struct {
	shadow   *ContinuousDispatchShadowService
	goalPath string
	now      func() time.Time
}

func NewFileWorkConservingProjectionProvider(shadow *ContinuousDispatchShadowService, goalPath string) *FileWorkConservingProjectionProvider {
	return &FileWorkConservingProjectionProvider{shadow: shadow, goalPath: strings.TrimSpace(goalPath), now: time.Now}
}

func (p *FileWorkConservingProjectionProvider) WithClock(now func() time.Time) *FileWorkConservingProjectionProvider {
	cp := *p
	if now != nil {
		cp.now = now
	}
	return &cp
}

func (p *FileWorkConservingProjectionProvider) ProjectWorkConserving(ctx context.Context, req WorkConservingProjectionRequest) (WorkConservingProjection, error) {
	if p == nil || p.shadow == nil || p.shadow.store == nil || p.shadow.directory == nil || p.goalPath == "" {
		return WorkConservingProjection{}, fmt.Errorf("%w: provider or source is not configured", ErrWorkConservingProjectionSourceGap)
	}
	if !validWorkConservingProjectionRequest(req) {
		return WorkConservingProjection{}, fmt.Errorf("%w: invalid request scope", ErrWorkConservingProjectionSourceGap)
	}
	now := p.now().UTC()
	if now.IsZero() {
		return WorkConservingProjection{}, fmt.Errorf("%w: invalid provider clock", ErrWorkConservingProjectionSourceGap)
	}
	snapshot, err := readWorkConservingGoalSource(p.goalPath)
	if err != nil {
		return WorkConservingProjection{}, err
	}
	workspaceID, projectID := shadowUUIDString(req.WorkspaceID), shadowUUIDString(req.ProjectID)
	if snapshot.Binding.WorkspaceID != workspaceID || snapshot.Binding.ProjectID != projectID {
		return WorkConservingProjection{}, fmt.Errorf("%w: Goal source scope does not match request", ErrWorkConservingProjectionSourceGap)
	}
	project, err := p.shadow.store.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: req.ProjectID, WorkspaceID: req.WorkspaceID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WorkConservingProjection{}, ErrContinuousDispatchProjectAbsent
		}
		return WorkConservingProjection{}, fmt.Errorf("%w: read project: %v", ErrWorkConservingProjectionSourceGap, err)
	}
	if !project.ID.Valid || project.ID != req.ProjectID || !project.WorkspaceID.Valid || project.WorkspaceID != req.WorkspaceID {
		return WorkConservingProjection{}, fmt.Errorf("%w: project scope drift", ErrWorkConservingProjectionSourceGap)
	}
	employees, employeeAuthority, err := p.readAllEmployees(ctx, req.WorkspaceID)
	if err != nil {
		return WorkConservingProjection{}, err
	}
	issues, err := p.readAllIssues(ctx, req.WorkspaceID, req.ProjectID)
	if err != nil {
		return WorkConservingProjection{}, err
	}
	agents, err := p.shadow.store.ListAllAgents(ctx, req.WorkspaceID)
	if err != nil {
		return WorkConservingProjection{}, fmt.Errorf("%w: read agents: %v", ErrWorkConservingProjectionSourceGap, err)
	}
	runtimes, err := p.shadow.store.ListAgentRuntimes(ctx, req.WorkspaceID)
	if err != nil {
		return WorkConservingProjection{}, fmt.Errorf("%w: read runtimes: %v", ErrWorkConservingProjectionSourceGap, err)
	}
	taskSnapshot, err := p.shadow.store.ListWorkspaceAgentTaskSnapshot(ctx, req.WorkspaceID)
	if err != nil {
		return WorkConservingProjection{}, fmt.Errorf("%w: read task snapshot: %v", ErrWorkConservingProjectionSourceGap, err)
	}
	agentsByID, runtimesByID := make(map[string]db.Agent, len(agents)), make(map[string]db.AgentRuntime, len(runtimes))
	for _, agent := range agents {
		agentsByID[shadowUUIDString(agent.ID)] = agent
	}
	for _, runtime := range runtimes {
		runtimesByID[shadowUUIDString(runtime.ID)] = runtime
	}
	activeWIP := make(map[string]int, len(agents))
	for _, task := range taskSnapshot {
		switch task.Status {
		case "queued", "dispatched", "running", "waiting_local_directory":
			activeWIP[shadowUUIDString(task.AgentID)]++
		}
	}
	wip := composeShadowWIP(agents, runtimesByID, taskSnapshot, now)
	candidates, preferredByAgent, _ := p.shadow.buildCandidates(ctx, employees, agentsByID, runtimesByID, activeWIP)
	employeeByID := make(map[string]companyops.PublicEmployeeSummary, len(employees))
	for _, employee := range employees {
		employeeByID[employee.EmployeeID] = employee
	}
	workEmployees := make([]continuousdispatch.WorkConservingEmployee, 0, len(candidates))
	for _, candidate := range candidates {
		employee, ok := employeeByID[candidate.EmployeeID]
		if !ok {
			continue
		}
		workEmployees = append(workEmployees, continuousdispatch.WorkConservingEmployee{
			Candidate: candidate, HealthyKnown: true, Healthy: candidate.Route.RuntimeHealth == routescore.RuntimeOnline,
			IdleKnown: true, Idle: candidate.ActiveWIP == 0, ProvenanceKnown: employeeAuthority,
			AuthorityKnown: employeeAuthority && employee.BindingState == companyops.BindingStateUniqueActiveCandidate && employee.LocalAgent != nil,
			WritePath:      continuousdispatch.WorkConservingWritePath{Known: true},
		})
	}
	workIssues := make([]continuousdispatch.WorkConservingIssue, 0, len(issues))
	locks, seenLocks := make([]continuousdispatch.WorkConservingWriteLock, 0), make(map[string]struct{})
	for _, issue := range issues {
		tasks, taskErr := p.shadow.store.ListTasksByIssue(ctx, issue.ID)
		if taskErr != nil {
			return WorkConservingProjection{}, fmt.Errorf("%w: read issue tasks: %v", ErrWorkConservingProjectionSourceGap, taskErr)
		}
		metadata := parseShadowMetadata(issue.Metadata)
		identity := continuousdispatch.DispatchIdentity{WorkspaceID: shadowUUIDString(req.WorkspaceID), IssueID: shadowUUIDString(issue.ID), Stage: metadata.Stage, CandidateRevision: metadata.CandidateRevision, Generation: metadata.Generation}
		generation := composeGeneration(identity, tasks)
		lease, leaseKnown := p.shadow.composeLease(ctx, metadata.WriteMutexKey, now)
		if !leaseKnown {
			lease.Known = false
		}
		if metadata.WriteMutexKey != "" && leaseKnown && !lease.Available {
			if _, exists := seenLocks[metadata.WriteMutexKey]; !exists {
				if current, readErr := p.shadow.leases.Read(ctx, metadata.WriteMutexKey); readErr == nil && current != nil {
					locks = append(locks, continuousdispatch.WorkConservingWriteLock{Key: metadata.WriteMutexKey, IssueID: shadowUUIDString(issue.ID), Owner: current.HolderID, Active: true})
				}
				seenLocks[metadata.WriteMutexKey] = struct{}{}
			}
		}
		frontier := composeFrontier(issue, tasks, agentsByID, runtimesByID, activeWIP, now)
		preferred := metadata.PreferredEmployeeID
		if preferred == "" && issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" {
			preferred = preferredByAgent[shadowUUIDString(issue.AssigneeID)]
		}
		requirement := composeRequirement(issue)
		lineage := reviewSourceLineage{}
		if requirement.NeedsReview {
			comments, commentErr := p.shadow.store.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Limit: 2000})
			if commentErr != nil {
				return WorkConservingProjection{}, fmt.Errorf("%w: read review lineage: %v", ErrWorkConservingProjectionSourceGap, commentErr)
			}
			lineage = resolveReviewSourceLineage(issue, tasks, comments, identity)
		}
		workIssues = append(workIssues, continuousdispatch.WorkConservingIssue{
			ID: shadowUUIDString(issue.ID), GoalID: snapshot.Binding.GoalID, PreferredEmployeeID: preferred,
			Frontier: frontier, Requirement: requirement, RequiredBaseID: metadata.RequiredBaseID,
			Generation: generation, WIP: wip, Lease: lease, ReviewAuthorKnown: !requirement.NeedsReview || lineage.Proven,
			ReviewAuthorAgentID: lineage.AuthorID,
			ProvenanceKnown:     identity.Complete(), AuthorityKnown: issue.WorkspaceID == req.WorkspaceID && issue.ProjectID == req.ProjectID,
			WritePath: continuousdispatch.WorkConservingWritePath{Known: metadata.WriteMutexKey != "", Key: metadata.WriteMutexKey},
		})
	}
	plan := p.shadow.planner.PlanWorkConserving(continuousdispatch.WorkConservingInput{GoalID: snapshot.Binding.GoalID, Issues: workIssues, Employees: workEmployees, ActiveLocks: locks})
	state := WorkConservingProjectionReady
	if len(plan.BlockedBacklog) > 0 || (len(plan.Suggestions) == 0 && len(workIssues) > 0) {
		state = WorkConservingProjectionBlocked
	}
	return WorkConservingProjection{SchemaVersion: WorkConservingProjectionSchemaV1, State: state,
		ReasonCode: map[bool]string{true: "blocked_backlog", false: "plan_ready"}[state == WorkConservingProjectionBlocked], GoalID: snapshot.Binding.GoalID,
		Authority:   WorkConservingAuthoritySnapshot{WorkspaceID: workspaceID, ProjectID: projectID, SourceRef: snapshot.Binding.SourceRef, Revision: "sha256:" + snapshot.Digest, ObservedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(workConservingProjectionTTL).Format(time.RFC3339)},
		Suggestions: plan.Suggestions, BlockedBacklog: plan.BlockedBacklog, Mismatch: plan.Mismatch, Total: len(plan.Suggestions) + len(plan.BlockedBacklog), Limit: req.Limit, Offset: req.Offset, NoWrite: true}, nil
}

func readWorkConservingGoalSource(path string) (workConservingGoalSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return workConservingGoalSnapshot{}, fmt.Errorf("%w: stat Goal source: %v", ErrWorkConservingProjectionSourceGap, err)
	}
	if info.Size() <= 0 || info.Size() > workConservingGoalSourceMaxBytes {
		return workConservingGoalSnapshot{}, fmt.Errorf("%w: Goal source size is invalid", ErrWorkConservingProjectionSourceGap)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return workConservingGoalSnapshot{}, fmt.Errorf("%w: read Goal source: %v", ErrWorkConservingProjectionSourceGap, err)
	}
	var document WorkConservingGoalSource
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return workConservingGoalSnapshot{}, fmt.Errorf("%w: parse Goal source: %v", ErrWorkConservingProjectionSourceGap, err)
	}
	if document.WorkConservingAuthority == nil {
		return workConservingGoalSnapshot{}, fmt.Errorf("%w: Goal source work_conserving_authority is missing", ErrWorkConservingProjectionSourceGap)
	}
	if strings.TrimSpace(document.SchemaVersion) == "" {
		return workConservingGoalSnapshot{}, fmt.Errorf("%w: Goal source schema_version is missing", ErrWorkConservingProjectionSourceGap)
	}
	binding := *document.WorkConservingAuthority
	for name, value := range map[string]string{"schema_version": binding.SchemaVersion, "goal_id": binding.GoalID, "workspace_id": binding.WorkspaceID, "project_id": binding.ProjectID, "source_ref": binding.SourceRef} {
		if strings.TrimSpace(value) == "" {
			return workConservingGoalSnapshot{}, fmt.Errorf("%w: Goal source %s is missing", ErrWorkConservingProjectionSourceGap, name)
		}
	}
	if binding.SchemaVersion != document.SchemaVersion {
		return workConservingGoalSnapshot{}, fmt.Errorf("%w: Goal source schema mismatch", ErrWorkConservingProjectionSourceGap)
	}
	digest := sha256.Sum256(raw)
	return workConservingGoalSnapshot{Binding: binding, Digest: hex.EncodeToString(digest[:])}, nil
}

func (p *FileWorkConservingProjectionProvider) readAllEmployees(ctx context.Context, workspaceID pgtype.UUID) ([]companyops.PublicEmployeeSummary, bool, error) {
	items := make([]companyops.PublicEmployeeSummary, 0)
	seen := make(map[string]struct{})
	authority := false
	for offset := 0; ; offset += workConservingEmployeePageSize {
		page, err := p.shadow.directory.GetEmployees(ctx, workspaceID, "", "", workConservingEmployeePageSize, offset)
		if err != nil || page == nil {
			return nil, false, fmt.Errorf("%w: read employee directory: %v", ErrWorkConservingProjectionSourceGap, err)
		}
		if page.WorkspaceID != shadowUUIDString(workspaceID) {
			return nil, false, fmt.Errorf("%w: employee directory workspace drift", ErrWorkConservingProjectionSourceGap)
		}
		authority = authority || (strings.TrimSpace(page.Authority.SourceRef) != "" && strings.TrimSpace(page.Authority.SourceRevision) != "")
		for _, item := range page.Items {
			if item.EmployeeID == "" {
				return nil, false, fmt.Errorf("%w: employee directory has blank identity", ErrWorkConservingProjectionSourceGap)
			}
			if _, duplicate := seen[item.EmployeeID]; duplicate {
				return nil, false, fmt.Errorf("%w: employee directory has duplicate identity", ErrWorkConservingProjectionSourceGap)
			}
			seen[item.EmployeeID] = struct{}{}
			items = append(items, item)
		}
		if len(items) >= page.Total || len(page.Items) < workConservingEmployeePageSize {
			return items, authority, nil
		}
	}
}

func (p *FileWorkConservingProjectionProvider) readAllIssues(ctx context.Context, workspaceID, projectID pgtype.UUID) ([]db.ListIssuesRow, error) {
	items := make([]db.ListIssuesRow, 0)
	seen := make(map[string]struct{})
	for offset := 0; ; offset += workConservingPageSize {
		page, err := p.shadow.store.ListIssues(ctx, db.ListIssuesParams{WorkspaceID: workspaceID, ProjectID: projectID, Limit: int32(workConservingPageSize), Offset: int32(offset)})
		if err != nil {
			return nil, fmt.Errorf("%w: read project issues: %v", ErrWorkConservingProjectionSourceGap, err)
		}
		for _, issue := range page {
			id := shadowUUIDString(issue.ID)
			if id == "" {
				return nil, fmt.Errorf("%w: project issue has blank identity", ErrWorkConservingProjectionSourceGap)
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, fmt.Errorf("%w: project issue pagination duplicated identity", ErrWorkConservingProjectionSourceGap)
			}
			seen[id] = struct{}{}
			if issue.WorkspaceID != workspaceID || issue.ProjectID != projectID {
				return nil, fmt.Errorf("%w: project issue scope drift", ErrWorkConservingProjectionSourceGap)
			}
			items = append(items, issue)
		}
		if len(page) < workConservingPageSize {
			return items, nil
		}
	}
}
