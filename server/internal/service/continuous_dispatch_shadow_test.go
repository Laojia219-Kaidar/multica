package service

import (
	"context"
	"encoding/json"
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
	comments map[string][]db.Comment
}

func (f *shadowStoreFixture) GetProjectInWorkspace(context.Context, db.GetProjectInWorkspaceParams) (db.Project, error) {
	return f.project, nil
}

func (f *shadowStoreFixture) CountIssuesByProject(context.Context, pgtype.UUID) (int64, error) {
	return int64(len(f.issues)), nil
}

func (f *shadowStoreFixture) CountIssuesByProjectAndStatus(_ context.Context, params db.CountIssuesByProjectAndStatusParams) (int64, error) {
	var count int64
	for _, issue := range f.issues {
		if issue.Status == params.Status {
			count++
		}
	}
	return count, nil
}

func (f *shadowStoreFixture) ListIssues(_ context.Context, params db.ListIssuesParams) ([]db.ListIssuesRow, error) {
	items := make([]db.ListIssuesRow, 0, len(f.issues))
	for _, issue := range f.issues {
		if params.Status.Valid && issue.Status != params.Status.String {
			continue
		}
		items = append(items, issue)
	}
	return items, nil
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

func (f *shadowStoreFixture) ListCommentsForIssue(_ context.Context, params db.ListCommentsForIssueParams) ([]db.Comment, error) {
	return append([]db.Comment(nil), f.comments[uuidString(params.IssueID)]...), nil
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
	if identity := got.Items[0].DispatchIdentity; !identity.Complete() ||
		identity.Stage != "implementation" || identity.Generation != "g-1" ||
		identity.CandidateRevision != "abc123" {
		t.Fatalf("dispatch identity = %+v, want complete canonical tuple", identity)
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

func TestContinuousDispatchShadowReviewQueryFiltersBeforePagination(t *testing.T) {
	fixture, workspaceID, projectID, issueID, primaryID, fallbackID := validShadowFixture(t)
	fixture.issues = append(fixture.issues, db.ListIssuesRow{
		ID: shadowUUID(t, "00000000-0000-0000-0000-000000000302"), WorkspaceID: workspaceID,
		Title: "Review this candidate", Status: "in_review", ProjectID: projectID,
		Metadata: []byte(`{"stage":"review","generation":"g-1","candidate_revision":"abc123","write_mutex_key":"repo:main"}`),
	})
	service := NewContinuousDispatchShadowService(
		fixture,
		shadowDirectoryFixture{result: employeeDirectory(primaryID, fallbackID)},
		shadowQuotaFixture{
			uuidString(primaryID):  {State: routescore.QuotaFresh, CheckedAt: shadowNow.Add(-time.Minute)},
			uuidString(fallbackID): {State: routescore.QuotaFresh, CheckedAt: shadowNow.Add(-time.Minute)},
		},
		shadowLeaseFixture{leases: map[string]*WriteLease{}},
	).WithClock(fixedShadowClock{now: shadowNow})

	got, err := service.InspectReviewProject(context.Background(), workspaceID, projectID, 1, 0)
	if err != nil {
		t.Fatalf("InspectReviewProject: %v", err)
	}
	if got.Total != 1 || len(got.Items) != 1 || got.Items[0].Status != "in_review" || got.Items[0].IssueID == uuidString(issueID) {
		t.Fatalf("review page = %+v, want only the in_review issue with filtered total", got)
	}
}

func TestContinuousDispatchShadowExcludesArchivedAgentAndFailsClosed(t *testing.T) {
	fixture, workspaceID, projectID, _, primaryID, fallbackID := validShadowFixture(t)
	fixture.agents[0].ArchivedAt = pgtype.Timestamptz{Time: shadowNow.Add(-time.Hour), Valid: true}
	service := NewContinuousDispatchShadowService(
		fixture,
		shadowDirectoryFixture{result: employeeDirectory(primaryID, fallbackID)},
		shadowQuotaFixture{
			uuidString(primaryID):  {State: routescore.QuotaFresh, CheckedAt: shadowNow.Add(-time.Minute)},
			uuidString(fallbackID): {State: routescore.QuotaFresh, CheckedAt: shadowNow.Add(-time.Minute)},
		},
		shadowLeaseFixture{leases: map[string]*WriteLease{}},
	).WithClock(fixedShadowClock{now: shadowNow})

	agentsByID := map[string]db.Agent{}
	for _, agent := range fixture.agents {
		agentsByID[uuidString(agent.ID)] = agent
	}
	runtimesByID := map[string]db.AgentRuntime{}
	for _, runtime := range fixture.runtimes {
		runtimesByID[uuidString(runtime.ID)] = runtime
	}
	candidates, _, quotaComplete := service.buildCandidates(
		context.Background(), employeeDirectory(primaryID, fallbackID).Items,
		agentsByID, runtimesByID, map[string]int{},
	)
	if !quotaComplete {
		t.Fatal("quota fixture should be complete")
	}
	for _, candidate := range candidates {
		if candidate.EmployeeID == "DE-PRIMARY" {
			t.Fatalf("archived agent entered candidate set: %+v", candidate)
		}
	}
	if len(candidates) != 1 || candidates[0].EmployeeID != "DE-FALLBACK" {
		t.Fatalf("candidates = %+v, want only fallback", candidates)
	}

	got, err := service.InspectProject(context.Background(), workspaceID, projectID, 50, 0)
	if err != nil {
		t.Fatalf("InspectProject: %v", err)
	}
	if got.Items[0].NextAction.State != continuousdispatch.StateBlocked ||
		got.Items[0].NextAction.Reasons[0] != continuousdispatch.Reason("agent_archived") {
		t.Fatalf("action = %+v, want archived-agent fail-closed", got.Items[0].NextAction)
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
	if got.Items[0].NextAction.State != continuousdispatch.StateBlocked ||
		got.Items[0].NextAction.Reasons[0] != continuousdispatch.ReasonDuplicateUnattributed {
		t.Fatalf("frontier state = %+v, want unattributed duplicate block", got.Items[0].NextAction)
	}
	if !got.Items[0].Generation.DuplicateUnattributed {
		t.Fatalf("generation = %+v, want duplicate evidence", got.Items[0].Generation)
	}
}

func TestContinuousDispatchShadowReviewRequiresStrictCommentTaskLineage(t *testing.T) {
	fixture, workspaceID, projectID, issueID, authorID, reviewerID := validShadowFixture(t)
	fixture.issues[0].Status = "in_review"
	fixture.issues[0].Metadata = []byte(`{"stage":"review","generation":"g-1","candidate_revision":"abc123","preferred_employee_id":"DE-PRIMARY","required_base_id":"base-a","write_mutex_key":"repo:main"}`)
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID),
		Stage: "implementation", CandidateRevision: "abc123", Generation: "g-1",
	}
	source := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000905")
	source.TaskKind = TaskKindWork
	sourceContext, err := json.Marshal(shadowTaskContext{ContinuousDispatch: identity})
	if err != nil {
		t.Fatal(err)
	}
	source.Context = sourceContext
	fixture.tasks[uuidString(issueID)] = []db.AgentTaskQueue{source}
	directory := employeeDirectory(authorID, reviewerID)
	directory.Items[1].PositionTitle = "质量审核工程师"
	directory.Items[1].PositionID = "code_review"
	service := NewContinuousDispatchShadowService(
		fixture,
		shadowDirectoryFixture{result: directory},
		shadowQuotaFixture{
			uuidString(authorID):   {State: routescore.QuotaFresh, CheckedAt: shadowNow.Add(-time.Minute), AccountRef: "author"},
			uuidString(reviewerID): {State: routescore.QuotaFresh, CheckedAt: shadowNow.Add(-time.Minute), AccountRef: "reviewer"},
		},
		shadowLeaseFixture{leases: map[string]*WriteLease{}},
	).WithClock(fixedShadowClock{now: shadowNow})

	withoutComment, err := service.InspectProject(context.Background(), workspaceID, projectID, 50, 0)
	if err != nil {
		t.Fatalf("InspectProject without source comment: %v", err)
	}
	if withoutComment.Items[0].NextAction.State != continuousdispatch.StateBlocked ||
		withoutComment.Items[0].NextAction.Reasons[0] != continuousdispatch.ReasonReviewAuthorEvidenceMissing {
		t.Fatalf("without source comment = %+v, want strict lineage block", withoutComment.Items[0])
	}

	fixture.comments[uuidString(issueID)] = []db.Comment{{
		ID: shadowUUID(t, "00000000-0000-0000-0000-000000000906"), IssueID: issueID, WorkspaceID: workspaceID,
		AuthorType: "agent", AuthorID: authorID, SourceTaskID: source.ID,
	}}
	withLineage, err := service.InspectProject(context.Background(), workspaceID, projectID, 50, 0)
	if err != nil {
		t.Fatalf("InspectProject with source comment: %v", err)
	}
	item := withLineage.Items[0]
	if item.SourceRef != continuousDispatchReviewCommentRef(fixture.comments[uuidString(issueID)][0].ID) ||
		item.SourceTaskID != uuidString(source.ID) || item.NextAction.Selected == nil || item.NextAction.Selected.AgentID != uuidString(reviewerID) {
		t.Fatalf("strict lineage item = %+v, want source task and independent reviewer", item)
	}
	for _, candidate := range item.NextAction.Candidates {
		if candidate.AgentID == uuidString(authorID) && candidate.Eligible {
			t.Fatalf("source author was eligible as reviewer: %+v", candidate)
		}
	}

	fixture.tasks[uuidString(issueID)][0].Context = []byte(`{"continuous_dispatch":{"workspace_id":"00000000-0000-0000-0000-000000000101","issue_id":"00000000-0000-0000-0000-000000000301","stage":"old","candidate_revision":"abc123","generation":"g-1"}}`)
	drifted, err := service.InspectProject(context.Background(), workspaceID, projectID, 50, 0)
	if err != nil {
		t.Fatalf("InspectProject with drifted lineage: %v", err)
	}
	if drifted.Items[0].NextAction.State != continuousdispatch.StateBlocked ||
		drifted.Items[0].NextAction.Reasons[0] != continuousdispatch.ReasonReviewAuthorEvidenceMissing {
		t.Fatalf("drifted lineage = %+v, want source block", drifted.Items[0])
	}
}

func TestResolveReviewSourceLineageHistoricalRepairDoesNotMaskNewWork(t *testing.T) {
	_, workspaceID, _, issueID, authorID, _ := validShadowFixture(t)
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "review",
		CandidateRevision: "candidate-new", Generation: "2",
	}
	workIdentity := continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation",
		CandidateRevision: "candidate-new", Generation: "2",
	}
	oldBase := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000906")
	oldBase.TaskKind = TaskKindWork
	oldBase.Context, _ = json.Marshal(shadowTaskContext{ContinuousDispatch: continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation",
		CandidateRevision: "candidate-old", Generation: "1",
	}})
	oldRepair := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000907")
	oldRepair.TaskKind = TaskKindRepair
	oldRepair.ReviewTargetTaskID = oldBase.ID
	oldRepair.Context = []byte(`{"kind":"repair","candidate_task_id":"00000000-0000-0000-0000-000000000906","review_task_id":"00000000-0000-0000-0000-000000000909"}`)
	work := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000908")
	work.TaskKind = TaskKindWork
	work.Context, _ = json.Marshal(shadowTaskContext{ContinuousDispatch: workIdentity})
	comments := []db.Comment{
		{ID: shadowUUID(t, "00000000-0000-0000-0000-000000000910"), IssueID: issueID, WorkspaceID: workspaceID, AuthorType: "agent", AuthorID: authorID, SourceTaskID: oldRepair.ID},
		{ID: shadowUUID(t, "00000000-0000-0000-0000-000000000911"), IssueID: issueID, WorkspaceID: workspaceID, AuthorType: "agent", AuthorID: authorID, SourceTaskID: work.ID},
	}
	got := resolveReviewSourceLineage(db.ListIssuesRow{ID: issueID, WorkspaceID: workspaceID}, []db.AgentTaskQueue{oldBase, oldRepair, work}, comments, identity)
	if !got.Proven || got.TaskID != uuidString(work.ID) {
		t.Fatalf("lineage = %+v, want current work task despite historical repair", got)
	}
}

func TestResolveReviewSourceLineageCurrentRepairWithoutStampBlocksOldWork(t *testing.T) {
	_, workspaceID, _, issueID, authorID, reviewerID := validShadowFixture(t)
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "review",
		CandidateRevision: "candidate-current", Generation: "3",
	}
	baseIdentity := continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation",
		CandidateRevision: "candidate-current", Generation: "3",
	}
	work := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000912")
	work.TaskKind = TaskKindWork
	work.Context, _ = json.Marshal(shadowTaskContext{ContinuousDispatch: baseIdentity})
	repair := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000913")
	repair.TaskKind = TaskKindRepair
	repair.ReviewTargetTaskID = work.ID
	repair.Context = []byte(`{"kind":"repair","candidate_task_id":"00000000-0000-0000-0000-000000000912","review_task_id":"00000000-0000-0000-0000-000000000914"}`)
	review := shadowTask(t, issueID, reviewerID, "completed", "00000000-0000-0000-0000-000000000914")
	review.TaskKind = TaskKindReview
	review.ReviewTargetTaskID = work.ID
	review.Result, _ = json.Marshal(map[string]any{
		"verdict": "revise", "review_state": ReviewStateReviseRequested,
		"reviewer_agent_id": uuidString(reviewerID), "candidate_task_id": uuidString(work.ID),
		"verdict_contract": completedReviewVerdictMarkerV1,
	})
	repair.TriggerEvidenceKind = pgtype.Text{String: "review_repair", Valid: true}
	repair.TriggerEvidenceRefID = review.ID
	comments := []db.Comment{
		{ID: shadowUUID(t, "00000000-0000-0000-0000-000000000915"), IssueID: issueID, WorkspaceID: workspaceID, AuthorType: "agent", AuthorID: authorID, SourceTaskID: work.ID},
		{ID: shadowUUID(t, "00000000-0000-0000-0000-000000000916"), IssueID: issueID, WorkspaceID: workspaceID, AuthorType: "agent", AuthorID: authorID, SourceTaskID: repair.ID, Content: repairCandidateMarkerV1 + " malformed"},
	}
	got := resolveReviewSourceLineage(db.ListIssuesRow{ID: issueID, WorkspaceID: workspaceID}, []db.AgentTaskQueue{work, repair, review}, comments, identity)
	if got.Proven {
		t.Fatalf("lineage = %+v, want blocked until repair is stamped and evidenced", got)
	}
}

func TestResolveReviewSourceLineageAcceptsStampedRepairWithOldBaseIdentity(t *testing.T) {
	_, workspaceID, _, issueID, authorID, reviewerID := validShadowFixture(t)
	identity := continuousdispatch.DispatchIdentity{WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "review", CandidateRevision: "candidate-new", Generation: "2"}
	baseIdentity := continuousdispatch.DispatchIdentity{WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation", CandidateRevision: "candidate-old", Generation: "1"}
	newIdentity := continuousdispatch.DispatchIdentity{WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation", CandidateRevision: "candidate-new", Generation: "2"}
	base := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000923")
	base.TaskKind = TaskKindWork
	base.Context, _ = json.Marshal(shadowTaskContext{ContinuousDispatch: baseIdentity})
	review := shadowTask(t, issueID, reviewerID, "completed", "00000000-0000-0000-0000-000000000924")
	review.TaskKind = TaskKindReview
	review.ReviewTargetTaskID = base.ID
	review.Result, _ = json.Marshal(map[string]any{"verdict": "revise", "review_state": ReviewStateReviseRequested, "reviewer_agent_id": uuidString(reviewerID), "candidate_task_id": uuidString(base.ID), "verdict_contract": completedReviewVerdictMarkerV1})
	repair := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000925")
	repair.TaskKind = TaskKindRepair
	repair.ReviewTargetTaskID = base.ID
	repair.TriggerEvidenceKind = pgtype.Text{String: "review_repair", Valid: true}
	repair.TriggerEvidenceRefID = review.ID
	repair.Context, _ = json.Marshal(map[string]any{"kind": TaskKindRepair, "candidate_task_id": uuidString(base.ID), "review_task_id": uuidString(review.ID), "continuous_dispatch": newIdentity})
	marker := repairCandidatePayload{RepairTaskID: uuidString(repair.ID), BaseTaskID: uuidString(base.ID), BaseCandidateRevision: "candidate-old", BaseGeneration: "1", CandidateRevision: "candidate-new", Generation: "2"}
	repair.Result, _ = json.Marshal(repairCandidateRuntimeResult{Output: repairCandidateMarkerLine(marker)})
	comment := db.Comment{ID: shadowUUID(t, "00000000-0000-0000-0000-000000000926"), IssueID: issueID, WorkspaceID: workspaceID, AuthorType: "agent", AuthorID: authorID, SourceTaskID: repair.ID, Content: repairCandidateMarkerLine(marker)}
	got := resolveReviewSourceLineage(db.ListIssuesRow{ID: issueID, WorkspaceID: workspaceID}, []db.AgentTaskQueue{base, review, repair}, []db.Comment{comment}, identity)
	if !got.Proven || got.TaskID != uuidString(repair.ID) {
		t.Fatalf("lineage = %+v, want stamped repair with old base identity", got)
	}
}

func TestShadowRepairBaseLineageRejectsRequiredEvidenceDrift(t *testing.T) {
	_, workspaceID, _, issueID, authorID, reviewerID := validShadowFixture(t)
	identity := continuousdispatch.DispatchIdentity{WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "review", CandidateRevision: "candidate-new", Generation: "2"}
	baseIdentity := continuousdispatch.DispatchIdentity{WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation", CandidateRevision: "candidate-old", Generation: "1"}
	newIdentity := continuousdispatch.DispatchIdentity{WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation", CandidateRevision: "candidate-new", Generation: "2"}
	base := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000927")
	base.TaskKind = TaskKindWork
	base.Context, _ = json.Marshal(shadowTaskContext{ContinuousDispatch: baseIdentity})
	review := shadowTask(t, reviewerID, reviewerID, "completed", "00000000-0000-0000-0000-000000000928")
	review.IssueID = issueID
	review.TaskKind = TaskKindReview
	review.ReviewTargetTaskID = base.ID
	review.Result, _ = json.Marshal(map[string]any{"verdict": "revise", "review_state": ReviewStateReviseRequested, "reviewer_agent_id": uuidString(reviewerID), "candidate_task_id": uuidString(base.ID), "verdict_contract": completedReviewVerdictMarkerV1})
	repair := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000929")
	repair.TaskKind = TaskKindRepair
	repair.ReviewTargetTaskID = base.ID
	repair.TriggerEvidenceKind = pgtype.Text{String: "review_repair", Valid: true}
	repair.TriggerEvidenceRefID = review.ID
	repair.Context, _ = json.Marshal(map[string]any{"kind": TaskKindRepair, "candidate_task_id": uuidString(base.ID), "review_task_id": uuidString(review.ID), "continuous_dispatch": newIdentity})
	marker := repairCandidatePayload{RepairTaskID: uuidString(repair.ID), BaseTaskID: uuidString(base.ID), BaseCandidateRevision: "candidate-old", BaseGeneration: "1", CandidateRevision: "candidate-new", Generation: "2"}
	repair.Result, _ = json.Marshal(repairCandidateRuntimeResult{Output: repairCandidateMarkerLine(marker)})
	baseTasks := map[string]db.AgentTaskQueue{uuidString(base.ID): base, uuidString(review.ID): review, uuidString(repair.ID): repair}
	tests := map[string]func(map[string]db.AgentTaskQueue, *db.AgentTaskQueue){
		"base context missing": func(tasks map[string]db.AgentTaskQueue, _ *db.AgentTaskQueue) {
			task := tasks[uuidString(base.ID)]
			task.Context = nil
			tasks[uuidString(base.ID)] = task
		},
		"base identity drift": func(tasks map[string]db.AgentTaskQueue, _ *db.AgentTaskQueue) {
			task := tasks[uuidString(base.ID)]
			task.Context, _ = json.Marshal(shadowTaskContext{ContinuousDispatch: continuousdispatch.DispatchIdentity{WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation", CandidateRevision: "other", Generation: "1"}})
			tasks[uuidString(base.ID)] = task
		},
		"marker base drift": func(_ map[string]db.AgentTaskQueue, task *db.AgentTaskQueue) {
			marker.BaseCandidateRevision = "other"
			task.Result, _ = json.Marshal(repairCandidateRuntimeResult{Output: repairCandidateMarkerLine(marker)})
		},
		"trigger kind drift": func(_ map[string]db.AgentTaskQueue, task *db.AgentTaskQueue) {
			task.TriggerEvidenceKind = pgtype.Text{String: "other", Valid: true}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			tasks := make(map[string]db.AgentTaskQueue, len(baseTasks))
			for key, value := range baseTasks {
				tasks[key] = value
			}
			candidate := tasks[uuidString(repair.ID)]
			mutate(tasks, &candidate)
			tasks[uuidString(repair.ID)] = candidate
			if _, _, _, proven := shadowRepairBaseLineage(candidate, db.ListIssuesRow{ID: issueID, WorkspaceID: workspaceID}, identity, tasks, true); proven {
				t.Fatalf("drifted repair lineage unexpectedly proven")
			}
		})
	}
}

func TestResolveReviewSourceLineageRejectsReviewTaskAsImplementationSource(t *testing.T) {
	_, workspaceID, _, issueID, authorID, _ := validShadowFixture(t)
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "review",
		CandidateRevision: "candidate-review-kind", Generation: "4",
	}
	task := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000917")
	task.TaskKind = TaskKindReview
	task.Context, _ = json.Marshal(shadowTaskContext{ContinuousDispatch: continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation",
		CandidateRevision: identity.CandidateRevision, Generation: identity.Generation,
	}})
	comment := db.Comment{
		ID: shadowUUID(t, "00000000-0000-0000-0000-000000000918"), IssueID: issueID, WorkspaceID: workspaceID,
		AuthorType: "agent", AuthorID: authorID, SourceTaskID: task.ID,
	}
	got := resolveReviewSourceLineage(db.ListIssuesRow{ID: issueID, WorkspaceID: workspaceID}, []db.AgentTaskQueue{task}, []db.Comment{comment}, identity)
	if got.Proven {
		t.Fatalf("lineage = %+v, want review Task rejected as implementation source", got)
	}
}

func TestResolveReviewSourceLineageRejectsRepairBasedOnRepairTask(t *testing.T) {
	_, workspaceID, _, issueID, authorID, _ := validShadowFixture(t)
	identity := continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "review",
		CandidateRevision: "candidate-round-two", Generation: "2",
	}
	implementation := continuousdispatch.DispatchIdentity{
		WorkspaceID: uuidString(workspaceID), IssueID: uuidString(issueID), Stage: "implementation",
		CandidateRevision: identity.CandidateRevision, Generation: identity.Generation,
	}
	priorRepair := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000919")
	priorRepair.TaskKind = TaskKindRepair
	currentRepair := shadowTask(t, issueID, authorID, "completed", "00000000-0000-0000-0000-000000000920")
	currentRepair.TaskKind = TaskKindRepair
	currentRepair.ReviewTargetTaskID = priorRepair.ID
	currentRepair.Context, _ = json.Marshal(map[string]any{
		"kind":                TaskKindRepair,
		"candidate_task_id":   uuidString(priorRepair.ID),
		"review_task_id":      "00000000-0000-0000-0000-000000000921",
		"continuous_dispatch": implementation,
	})
	marker := repairCandidatePayload{
		RepairTaskID: uuidString(currentRepair.ID), BaseTaskID: uuidString(priorRepair.ID),
		BaseCandidateRevision: "candidate-round-one", BaseGeneration: "1",
		CandidateRevision: identity.CandidateRevision, Generation: identity.Generation,
	}
	currentRepair.Result, _ = json.Marshal(repairCandidateRuntimeResult{Output: repairCandidateMarkerLine(marker)})
	comment := db.Comment{
		ID: shadowUUID(t, "00000000-0000-0000-0000-000000000922"), IssueID: issueID, WorkspaceID: workspaceID,
		AuthorType: "agent", AuthorID: authorID, SourceTaskID: currentRepair.ID, Content: repairCandidateMarkerLine(marker),
	}
	got := resolveReviewSourceLineage(db.ListIssuesRow{ID: issueID, WorkspaceID: workspaceID}, []db.AgentTaskQueue{priorRepair, currentRepair}, []db.Comment{comment}, identity)
	if got.Proven {
		t.Fatalf("lineage = %+v, want repair-as-base rejected for single-round contract", got)
	}
}

func TestContinuousDispatchShadowReturnsOnlyExactGenerationTaskReceipt(t *testing.T) {
	fixture, workspaceID, projectID, issueID, primaryID, fallbackID := validShadowFixture(t)
	task := shadowTask(t, issueID, primaryID, "running", "00000000-0000-0000-0000-000000000905")
	task.Context = []byte(`{"continuous_dispatch":{"workspace_id":"00000000-0000-0000-0000-000000000101","issue_id":"00000000-0000-0000-0000-000000000301","stage":"implementation","candidate_revision":"abc123","generation":"g-1"}}`)
	fixture.tasks[uuidString(issueID)] = []db.AgentTaskQueue{task}
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
	action := got.Items[0].NextAction
	if action.State != continuousdispatch.StateAlreadyDispatched ||
		action.ExistingTaskID != "00000000-0000-0000-0000-000000000905" {
		t.Fatalf("action = %+v, want exact existing generation receipt", action)
	}
	if got.Items[0].Generation.DuplicateUnattributed {
		t.Fatalf("generation = %+v, exact task was treated as unattributed", got.Items[0].Generation)
	}
}

func TestContinuousDispatchShadowFailsClosedWhenStageIsMissing(t *testing.T) {
	fixture, workspaceID, projectID, _, primaryID, fallbackID := validShadowFixture(t)
	fixture.issues[0].Metadata = []byte(`{"generation":"g-1","candidate_revision":"abc123","required_base_id":"base-a"}`)
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
	if got.Items[0].Generation.Known || got.Items[0].NextAction.State != continuousdispatch.StateBlocked ||
		got.Items[0].NextAction.Reasons[0] != continuousdispatch.ReasonGenerationEvidenceMissing {
		t.Fatalf("item = %+v, want stage-missing fail closed", got.Items[0])
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
			Metadata:     []byte(`{"stage":"implementation","generation":"g-1","candidate_revision":"abc123","preferred_employee_id":"DE-PRIMARY","required_base_id":"base-a","write_mutex_key":"repo:main"}`),
		}},
		agents: []db.Agent{
			{ID: primaryID, WorkspaceID: workspaceID, Name: "Primary", RuntimeID: primaryRuntimeID, Status: "idle", MaxConcurrentTasks: 1, Model: pgtype.Text{String: "glm-5.2", Valid: true}, Kind: "user"},
			{ID: fallbackID, WorkspaceID: workspaceID, Name: "Fallback", RuntimeID: fallbackRuntimeID, Status: "idle", MaxConcurrentTasks: 1, Model: pgtype.Text{String: "qwen3.8-max", Valid: true}, Kind: "user"},
		},
		runtimes: []db.AgentRuntime{
			{ID: primaryRuntimeID, WorkspaceID: workspaceID, Status: "online", DaemonID: pgtype.Text{String: "base-a", Valid: true}, LastSeenAt: pgtype.Timestamptz{Time: shadowNow.Add(-time.Minute), Valid: true}},
			{ID: fallbackRuntimeID, WorkspaceID: workspaceID, Status: "online", DaemonID: pgtype.Text{String: "base-a", Valid: true}, LastSeenAt: pgtype.Timestamptz{Time: shadowNow.Add(-time.Minute), Valid: true}},
		},
		tasks:    map[string][]db.AgentTaskQueue{uuidString(issueID): {}},
		comments: map[string][]db.Comment{uuidString(issueID): {}},
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
