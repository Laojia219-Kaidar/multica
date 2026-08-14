package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/routescore"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var shadowNow = time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)

type shadowStoreFixture struct {
	project  db.Project
	issues   []db.ListIssuesRow
	agents   []db.Agent
	runtimes []db.AgentRuntime
	snapshot []db.AgentTaskQueue
	tasks    map[string][]db.AgentTaskQueue
}

func (f *shadowStoreFixture) GetProjectInWorkspace(context.Context, db.GetProjectInWorkspaceParams) (db.Project, error) {
	return f.project, nil
}

func (f *shadowStoreFixture) CountIssuesByProject(context.Context, pgtype.UUID) (int64, error) {
	return int64(len(f.issues)), nil
}

func (f *shadowStoreFixture) ListIssues(context.Context, db.ListIssuesParams) ([]db.ListIssuesRow, error) {
	return append([]db.ListIssuesRow(nil), f.issues...), nil
}

func (f *shadowStoreFixture) ListAllAgents(context.Context, pgtype.UUID) ([]db.Agent, error) {
	return append([]db.Agent(nil), f.agents...), nil
}

func (f *shadowStoreFixture) ListAgentRuntimes(context.Context, pgtype.UUID) ([]db.AgentRuntime, error) {
	return append([]db.AgentRuntime(nil), f.runtimes...), nil
}

func (f *shadowStoreFixture) ListWorkspaceAgentTaskSnapshot(context.Context, pgtype.UUID) ([]db.AgentTaskQueue, error) {
	return append([]db.AgentTaskQueue(nil), f.snapshot...), nil
}

func (f *shadowStoreFixture) ListTasksByIssue(_ context.Context, issueID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	return append([]db.AgentTaskQueue(nil), f.tasks[uuidString(issueID)]...), nil
}

type shadowDirectoryFixture struct {
	result *EmployeesResult
	err    error
}

func (f shadowDirectoryFixture) GetEmployees(context.Context, pgtype.UUID, string, string, int, int) (*EmployeesResult, error) {
	return f.result, f.err
}

type shadowQuotaFixture map[string]ShadowQuotaSnapshot

func (f shadowQuotaFixture) Lookup(_ context.Context, agent db.Agent, _ db.AgentRuntime) (ShadowQuotaSnapshot, error) {
	snapshot, ok := f[uuidString(agent.ID)]
	if !ok {
		return ShadowQuotaSnapshot{State: routescore.QuotaUnknown}, nil
	}
	return snapshot, nil
}

type shadowLeaseFixture struct {
	leases map[string]*WriteLease
	err    error
}

func (f shadowLeaseFixture) Read(_ context.Context, key string) (*WriteLease, error) {
	if f.err != nil {
		return nil, f.err
	}
	lease, ok := f.leases[key]
	if !ok {
		return nil, ErrLeaseNotFound
	}
	return lease, nil
}

func TestContinuousDispatchShadowReturnsFallbackFromRealSourceRows(t *testing.T) {
	fixture, workspaceID, projectID, issueID, primaryID, fallbackID := validShadowFixture(t)
	service := NewContinuousDispatchShadowService(
		fixture,
		shadowDirectoryFixture{result: employeeDirectory(primaryID, fallbackID)},
		shadowQuotaFixture{
			uuidString(primaryID): {
				State:      routescore.QuotaExhausted,
				CheckedAt:  shadowNow.Add(-time.Minute),
				AccountRef: "glm-account-a",
			},
			uuidString(fallbackID): {
				State:      routescore.QuotaFresh,
				CheckedAt:  shadowNow.Add(-time.Minute),
				AccountRef: "qwen-account-b",
			},
		},
		shadowLeaseFixture{leases: map[string]*WriteLease{}},
	).WithClock(fixedShadowClock{now: shadowNow})

	got, err := service.InspectProject(context.Background(), workspaceID, projectID, 50, 0)
	if err != nil {
		t.Fatalf("InspectProject: %v", err)
	}
	if got.SchemaVersion != ContinuousDispatchShadowSchemaV1 || got.ProjectID != uuidString(projectID) {
		t.Fatalf("unexpected envelope: %+v", got)
	}
	if len(got.Items) != 1 || got.Items[0].IssueID != uuidString(issueID) {
		t.Fatalf("items = %+v", got.Items)
	}
	action := got.Items[0].NextAction
	if action.State != continuousdispatch.StateFallback || action.Selected == nil {
		t.Fatalf("action = %+v, want fallback", action)
	}
	if action.Selected.AgentID != uuidString(fallbackID) || action.Selected.EmployeeID != "DE-FALLBACK" {
		t.Fatalf("selected = %+v, want fallback employee", action.Selected)
	}
	if action.Candidates[1].Reasons[0] != continuousdispatch.Reason("quota_exhausted") {
		t.Fatalf("candidate explanations = %+v", action.Candidates)
	}
	if !got.Sources.Project || !got.Sources.Organization || !got.Sources.Tasks || !got.Sources.Runtime {
		t.Fatalf("source health = %+v", got.Sources)
	}
}

func TestContinuousDispatchShadowFailsClosedWhenQuotaSourceIsMissing(t *testing.T) {
	fixture, workspaceID, projectID, _, primaryID, fallbackID := validShadowFixture(t)
	service := NewContinuousDispatchShadowService(
		fixture,
		shadowDirectoryFixture{result: employeeDirectory(primaryID, fallbackID)},
		nil,
		shadowLeaseFixture{leases: map[string]*WriteLease{}},
	).WithClock(fixedShadowClock{now: shadowNow})

	got, err := service.InspectProject(context.Background(), workspaceID, projectID, 50, 0)
	if err != nil {
		t.Fatalf("InspectProject: %v", err)
	}
	if got.Sources.Quota {
		t.Fatal("quota source must be reported unavailable")
	}
	if got.Items[0].NextAction.State != continuousdispatch.StateBlocked ||
		got.Items[0].NextAction.Reasons[0] != continuousdispatch.ReasonNoEligibleCandidate {
		t.Fatalf("action = %+v, want quota fail-closed", got.Items[0].NextAction)
	}
	for _, candidate := range got.Items[0].NextAction.Candidates {
		if len(candidate.Reasons) != 1 || candidate.Reasons[0] != continuousdispatch.Reason("quota_unknown") {
			t.Fatalf("candidate = %+v, want quota_unknown", candidate)
		}
	}
}

func TestContinuousDispatchShadowRejectsDuplicateOpenGeneration(t *testing.T) {
	fixture, workspaceID, projectID, issueID, primaryID, fallbackID := validShadowFixture(t)
	fixture.tasks[uuidString(issueID)] = []db.AgentTaskQueue{
		shadowTask(t, issueID, primaryID, "running", "00000000-0000-0000-0000-000000000901"),
		shadowTask(t, issueID, fallbackID, "queued", "00000000-0000-0000-0000-000000000902"),
	}
	service := NewContinuousDispatchShadowService(
		fixture,
		shadowDirectoryFixture{result: employeeDirectory(primaryID, fallbackID)},
		shadowQuotaFixture{},
		shadowLeaseFixture{leases: map[string]*WriteLease{}},
	).WithClock(fixedShadowClock{now: shadowNow})

	got, err := service.InspectProject(context.Background(), workspaceID, projectID, 50, 0)
	if err != nil {
		t.Fatalf("InspectProject: %v", err)
	}
	if got.Items[0].NextAction.State != continuousdispatch.StateRunning {
		t.Fatalf("frontier state = %+v, want running", got.Items[0].NextAction)
	}
	if !got.Items[0].Generation.DuplicateUnattributed {
		t.Fatalf("generation = %+v, want duplicate evidence", got.Items[0].Generation)
	}
}

func TestContinuousDispatchShadowFailsClosedWithoutOrganizationAuthority(t *testing.T) {
	fixture, workspaceID, projectID, _, _, _ := validShadowFixture(t)
	service := NewContinuousDispatchShadowService(
		fixture,
		shadowDirectoryFixture{err: errors.New("authority unavailable")},
		nil,
		nil,
	)
	_, err := service.InspectProject(context.Background(), workspaceID, projectID, 50, 0)
	if !errors.Is(err, ErrContinuousDispatchSourceGap) {
		t.Fatalf("error = %v, want source gap", err)
	}
}

func TestComposeFrontierDoesNotLetOldFailureMaskNewCompletion(t *testing.T) {
	_, _, _, issueID, primaryID, _ := validShadowFixture(t)
	issue := db.ListIssuesRow{
		ID: issueID, Status: "in_progress", AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: primaryID,
	}
	runtimeID := shadowUUID(t, "00000000-0000-0000-0000-000000000501")
	agents := map[string]db.Agent{uuidString(primaryID): {
		ID: primaryID, RuntimeID: runtimeID, MaxConcurrentTasks: 1,
	}}
	runtimes := map[string]db.AgentRuntime{uuidString(runtimeID): {ID: runtimeID, Status: "online"}}
	tasks := []db.AgentTaskQueue{
		shadowTask(t, issueID, primaryID, "completed", "00000000-0000-0000-0000-000000000903"),
		shadowTask(t, issueID, primaryID, "failed", "00000000-0000-0000-0000-000000000904"),
	}

	got := composeFrontier(issue, tasks, agents, runtimes, map[string]int{}, shadowNow)
	if got.HasTask || got.TaskStatus != "" {
		t.Fatalf("frontier = %+v, old failure masked newer completion", got)
	}
}

func validShadowFixture(t *testing.T) (*shadowStoreFixture, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	workspaceID := shadowUUID(t, "00000000-0000-0000-0000-000000000101")
	projectID := shadowUUID(t, "00000000-0000-0000-0000-000000000201")
	issueID := shadowUUID(t, "00000000-0000-0000-0000-000000000301")
	primaryID := shadowUUID(t, "00000000-0000-0000-0000-000000000401")
	fallbackID := shadowUUID(t, "00000000-0000-0000-0000-000000000402")
	primaryRuntimeID := shadowUUID(t, "00000000-0000-0000-0000-000000000501")
	fallbackRuntimeID := shadowUUID(t, "00000000-0000-0000-0000-000000000502")
	fixture := &shadowStoreFixture{
		project: db.Project{ID: projectID, WorkspaceID: workspaceID, Title: "Real project"},
		issues: []db.ListIssuesRow{{
			ID:           issueID,
			WorkspaceID:  workspaceID,
			Title:        "Implement bounded adapter",
			Status:       "in_progress",
			AssigneeType: pgtype.Text{String: "agent", Valid: true},
			AssigneeID:   primaryID,
			ProjectID:    projectID,
			Metadata:     []byte(`{"generation":"g-1","candidate_revision":"abc123","preferred_employee_id":"DE-PRIMARY","required_base_id":"base-a","write_mutex_key":"repo:main"}`),
		}},
		agents: []db.Agent{
			{ID: primaryID, WorkspaceID: workspaceID, Name: "Primary", RuntimeID: primaryRuntimeID, Status: "idle", MaxConcurrentTasks: 1, Model: pgtype.Text{String: "glm-5.2", Valid: true}, Kind: "user"},
			{ID: fallbackID, WorkspaceID: workspaceID, Name: "Fallback", RuntimeID: fallbackRuntimeID, Status: "idle", MaxConcurrentTasks: 1, Model: pgtype.Text{String: "qwen3.8-max", Valid: true}, Kind: "user"},
		},
		runtimes: []db.AgentRuntime{
			{ID: primaryRuntimeID, WorkspaceID: workspaceID, Status: "online", DaemonID: pgtype.Text{String: "base-a", Valid: true}, LastSeenAt: pgtype.Timestamptz{Time: shadowNow.Add(-time.Minute), Valid: true}},
			{ID: fallbackRuntimeID, WorkspaceID: workspaceID, Status: "online", DaemonID: pgtype.Text{String: "base-a", Valid: true}, LastSeenAt: pgtype.Timestamptz{Time: shadowNow.Add(-time.Minute), Valid: true}},
		},
		tasks: map[string][]db.AgentTaskQueue{uuidString(issueID): {}},
	}
	return fixture, workspaceID, projectID, issueID, primaryID, fallbackID
}

func employeeDirectory(primaryID, fallbackID pgtype.UUID) *EmployeesResult {
	return &EmployeesResult{
		SchemaVersion: companyopsapi.PublicEmployeesSchema,
		WorkspaceID:   "00000000-0000-0000-0000-000000000101",
		Items: []companyopsapi.PublicEmployeeSummary{
			{
				EmployeeID: "DE-PRIMARY", DisplayName: "Primary", PositionTitle: "全栈工程师", PositionID: "implementation",
				Availability: companyopsapi.AvailabilityAvailable, HiveCrewAgentID: uuidString(primaryID),
				LocalAgent: &companyopsapi.PublicLocalAgent{ID: uuidString(primaryID), RuntimeStatus: "online"},
			},
			{
				EmployeeID: "DE-FALLBACK", DisplayName: "Fallback", PositionTitle: "返修与集成工程师", PositionID: "repair_integration",
				Availability: companyopsapi.AvailabilityAvailable, HiveCrewAgentID: uuidString(fallbackID),
				LocalAgent: &companyopsapi.PublicLocalAgent{ID: uuidString(fallbackID), RuntimeStatus: "online"},
			},
		},
		Total: 2, Limit: 500,
	}
}

func shadowTask(t *testing.T, issueID, agentID pgtype.UUID, status, id string) db.AgentTaskQueue {
	t.Helper()
	return db.AgentTaskQueue{ID: shadowUUID(t, id), IssueID: issueID, AgentID: agentID, Status: status, CreatedAt: pgtype.Timestamptz{Time: shadowNow.Add(-time.Minute), Valid: true}}
}

func shadowUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	parsed, err := uuid.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

type fixedShadowClock struct{ now time.Time }

func (c fixedShadowClock) Now() time.Time { return c.now }
