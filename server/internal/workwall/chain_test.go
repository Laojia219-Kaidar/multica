package workwall

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fakeChainStore is an in-memory chainStore. Workspace scoping is modelled
// exactly like the SQL: a lookup whose workspace does not match returns
// pgx.ErrNoRows, so tests prove the walk never falls back across workspaces.
// It stores only the narrow rows the production queries load, mirroring the
// narrow-read contract of the Work Wall chain.
type fakeChainStore struct {
	workspaceID   pgtype.UUID
	issuePrefix   string
	issues        map[string]db.Issue   // key: workspace|issue
	projects      map[string]db.Project // key: workspace|project
	profiles      map[string]db.GetRuntimeProfileForWorkWallRow
	receipts      map[string]db.GetExecutionReceiptForWorkWallRow // key: task
	autopilotRuns map[string]db.AutopilotRun                      // key: run id
	autopilots    map[string]db.Autopilot                         // key: autopilot id
	runtimes      map[string]db.AgentRuntime                      // key: workspace|runtime
	crossCalls    int                                             // lookups whose workspace arg mismatched the stored row
	issueCalls    []string
	receiptCalls  []pgtype.UUID
}

func newFakeChainStore() *fakeChainStore {
	return &fakeChainStore{
		workspaceID:   tu,
		issuePrefix:   "HIV",
		issues:        map[string]db.Issue{},
		projects:      map[string]db.Project{},
		profiles:      map[string]db.GetRuntimeProfileForWorkWallRow{},
		receipts:      map[string]db.GetExecutionReceiptForWorkWallRow{},
		autopilotRuns: map[string]db.AutopilotRun{},
		autopilots:    map[string]db.Autopilot{},
		runtimes:      map[string]db.AgentRuntime{},
	}
}

func key(ws pgtype.UUID, id pgtype.UUID) string { return uuidStr(ws) + "|" + uuidStr(id) }

func (f *fakeChainStore) GetWorkspaceIssuePrefix(_ context.Context, id pgtype.UUID) (string, error) {
	if id != f.workspaceID {
		return "", pgx.ErrNoRows
	}
	return f.issuePrefix, nil
}

func (f *fakeChainStore) GetIssueInWorkspace(_ context.Context, arg db.GetIssueInWorkspaceParams) (db.Issue, error) {
	issue, ok := f.issues[key(arg.WorkspaceID, arg.ID)]
	if !ok {
		if f.issuesForKey(arg.ID) {
			f.crossCalls++
		}
		return db.Issue{}, pgx.ErrNoRows
	}
	f.issueCalls = append(f.issueCalls, key(arg.WorkspaceID, arg.ID))
	return issue, nil
}

func (f *fakeChainStore) issuesForKey(id pgtype.UUID) bool {
	for _, v := range f.issues {
		if v.ID == id {
			return true
		}
	}
	return false
}

func (f *fakeChainStore) GetProjectInWorkspace(_ context.Context, arg db.GetProjectInWorkspaceParams) (db.Project, error) {
	project, ok := f.projects[key(arg.WorkspaceID, arg.ID)]
	if !ok {
		return db.Project{}, pgx.ErrNoRows
	}
	return project, nil
}

func (f *fakeChainStore) GetRuntimeProfileForWorkWall(_ context.Context, arg db.GetRuntimeProfileForWorkWallParams) (db.GetRuntimeProfileForWorkWallRow, error) {
	profile, ok := f.profiles[key(arg.WorkspaceID, arg.ID)]
	if !ok {
		return db.GetRuntimeProfileForWorkWallRow{}, pgx.ErrNoRows
	}
	return profile, nil
}

func (f *fakeChainStore) GetExecutionReceiptForWorkWall(_ context.Context, taskID pgtype.UUID) (db.GetExecutionReceiptForWorkWallRow, error) {
	f.receiptCalls = append(f.receiptCalls, taskID)
	receipt, ok := f.receipts[uuidStr(taskID)]
	if !ok {
		return db.GetExecutionReceiptForWorkWallRow{}, pgx.ErrNoRows
	}
	return receipt, nil
}

func (f *fakeChainStore) GetAutopilotRun(_ context.Context, id pgtype.UUID) (db.AutopilotRun, error) {
	run, ok := f.autopilotRuns[uuidStr(id)]
	if !ok {
		return db.AutopilotRun{}, pgx.ErrNoRows
	}
	return run, nil
}

func (f *fakeChainStore) GetAutopilot(_ context.Context, id pgtype.UUID) (db.Autopilot, error) {
	ap, ok := f.autopilots[uuidStr(id)]
	if !ok {
		return db.Autopilot{}, pgx.ErrNoRows
	}
	return ap, nil
}

func (f *fakeChainStore) GetAgentRuntimeForWorkspace(_ context.Context, arg db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error) {
	rt, ok := f.runtimes[key(arg.WorkspaceID, arg.ID)]
	if !ok {
		return db.AgentRuntime{}, pgx.ErrNoRows
	}
	return rt, nil
}

func otherWorkspace() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}, Valid: true}
}

func seededChainTask() *db.AgentTaskQueue {
	return &db.AgentTaskQueue{ID: tu, AgentID: tu, IssueID: tu, Status: "running"}
}

func seededIssue() db.Issue {
	return db.Issue{ID: tu, WorkspaceID: tu, Number: 797, Title: "[DEV] Work Wall complete execution-chain projection", ProjectID: tu}
}

func seededRuntime() *db.AgentRuntime {
	return &db.AgentRuntime{ID: tu, WorkspaceID: tu, Provider: "prime", Status: "online", ProfileID: tu}
}

func TestResolveExecutionChain_PositiveHydration(t *testing.T) {
	store := newFakeChainStore()
	store.issues[key(tu, tu)] = seededIssue()
	store.projects[key(tu, tu)] = db.Project{ID: tu, WorkspaceID: tu, Title: "HIVECREW 自我开发项目"}
	store.profiles[key(tu, tu)] = db.GetRuntimeProfileForWorkWallRow{ID: tu, WorkspaceID: tu, DisplayName: "glm-5.3 运行档案"}
	task := seededChainTask()
	runID := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	autopilotID := pgtype.UUID{Bytes: [16]byte{8}, Valid: true}
	task.AutopilotRunID = runID
	// Seed the run lineage: run matches task+issue, parent autopilot matches workspace.
	store.autopilotRuns[uuidStr(runID)] = db.AutopilotRun{
		ID: runID, AutopilotID: autopilotID, TaskID: tu, IssueID: tu,
	}
	store.autopilots[uuidStr(autopilotID)] = db.Autopilot{
		ID: autopilotID, WorkspaceID: tu,
	}
	store.receipts[uuidStr(tu)] = seededReceipt("completed")

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", seededRuntime(), task)
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}
	if chain == nil {
		t.Fatal("chain must resolve for a task")
	}
	if chain.TaskID != uuidStr(tu) {
		t.Fatalf("task id = %q", chain.TaskID)
	}
	if chain.IssueID != uuidStr(tu) || chain.IssueIdentifier != "HIV-797" || chain.IssueTitle != seededIssue().Title {
		t.Fatalf("issue chain = %+v", chain)
	}
	if chain.ProjectID != uuidStr(tu) || chain.ProjectTitle != "HIVECREW 自我开发项目" {
		t.Fatalf("project chain = %+v", chain)
	}
	if chain.RuntimeProfileID != uuidStr(tu) || chain.RuntimeProfileName != "glm-5.3 运行档案" {
		t.Fatalf("profile chain = %+v", chain)
	}
	if chain.RunID != uuidStr(runID) {
		t.Fatalf("run id = %q", chain.RunID)
	}
	if chain.ExecutionReceiptRef != "receipt://"+uuidStr(tu) || chain.ExecutionReceiptStatus != "completed" {
		t.Fatalf("receipt chain = %+v", chain)
	}
}

func TestResolveExecutionChain_NilTaskYieldsNilChain(t *testing.T) {
	chain, err := resolveExecutionChain(context.Background(), newFakeChainStore(), tu, "HIV", seededRuntime(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chain != nil {
		t.Fatalf("nil task must yield nil chain, got %+v", chain)
	}
}

func TestResolveExecutionChain_NilTaskStillHydratesRuntimeProfile(t *testing.T) {
	store := newFakeChainStore()
	store.profiles[key(tu, tu)] = db.GetRuntimeProfileForWorkWallRow{
		ID: tu, WorkspaceID: tu, DisplayName: "Prime Agent · GLM-5.3",
	}

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", seededRuntime(), nil)
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}
	if chain == nil {
		t.Fatal("idle runtime with a bound profile must return a profile-only chain")
	}
	if chain.TaskID != "" || chain.RuntimeProfileID != uuidStr(tu) || chain.RuntimeProfileName != "Prime Agent · GLM-5.3" {
		t.Fatalf("profile-only chain = %+v", chain)
	}
}

func TestResolveExecutionChain_MissingEvidenceStaysAbsent(t *testing.T) {
	store := newFakeChainStore() // no issue, project, profile or receipt rows
	task := seededChainTask()    // direct task: no autopilot_run_id

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", &db.AgentRuntime{ID: tu, WorkspaceID: tu, ProfileID: tu}, task)
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}
	if chain.IssueID != "" || chain.IssueIdentifier != "" || chain.IssueTitle != "" {
		t.Fatalf("missing issue row must stay absent, got %+v", chain)
	}
	if chain.ProjectID != "" || chain.ProjectTitle != "" {
		t.Fatalf("missing project must stay absent, got %+v", chain)
	}
	if chain.RuntimeProfileID != "" || chain.RuntimeProfileName != "" {
		t.Fatalf("missing profile row must stay absent, got %+v", chain)
	}
	if chain.RunID != "" {
		t.Fatalf("direct task has no authoritative Run ID, got %q", chain.RunID)
	}
	if chain.ExecutionReceiptRef != "" || chain.ExecutionReceiptStatus != "" {
		t.Fatalf("missing receipt must stay absent, got %+v", chain)
	}
	if chain.TaskID != uuidStr(tu) {
		t.Fatalf("task id must still trace, got %q", chain.TaskID)
	}
}

func TestResolveExecutionChain_IssueWithoutProject(t *testing.T) {
	store := newFakeChainStore()
	issue := seededIssue()
	issue.ProjectID = pgtype.UUID{} // issue not attached to a project
	store.issues[key(tu, tu)] = issue

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", nil, seededChainTask())
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}
	if chain.IssueIdentifier != "HIV-797" {
		t.Fatalf("issue identifier = %q", chain.IssueIdentifier)
	}
	if chain.ProjectID != "" || chain.ProjectTitle != "" {
		t.Fatalf("project must stay absent for an unattached issue, got %+v", chain)
	}
}

func TestResolveExecutionChain_CrossWorkspaceFailClosed(t *testing.T) {
	store := newFakeChainStore()
	// The same issue/project/profile/receipt ids exist, but under a DIFFERENT
	// workspace. The workspace-scoped lookups must resolve to nothing.
	other := otherWorkspace()
	store.issues[key(other, tu)] = seededIssue()
	store.projects[key(other, tu)] = db.Project{ID: tu, WorkspaceID: other, Title: "别的工作区项目"}
	store.profiles[key(other, tu)] = db.GetRuntimeProfileForWorkWallRow{ID: tu, WorkspaceID: other, DisplayName: "别的工作区档案"}
	store.receipts[uuidStr(tu)] = db.GetExecutionReceiptForWorkWallRow{
		TaskID: tu, WorkspaceID: other, IssueID: tu,
		TerminalStatus: pgtype.Text{String: "completed", Valid: true},
	}

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", seededRuntime(), seededChainTask())
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}
	if chain.IssueID != "" || chain.IssueIdentifier != "" || chain.IssueTitle != "" {
		t.Fatalf("cross-workspace issue must fail closed, got %+v", chain)
	}
	if chain.ProjectID != "" || chain.ProjectTitle != "" {
		t.Fatalf("cross-workspace project must fail closed, got %+v", chain)
	}
	if chain.RuntimeProfileID != "" || chain.RuntimeProfileName != "" {
		t.Fatalf("cross-workspace profile must fail closed, got %+v", chain)
	}
	if chain.ExecutionReceiptRef != "" || chain.ExecutionReceiptStatus != "" {
		t.Fatalf("cross-workspace receipt must fail closed, got %+v", chain)
	}
	// The walk still probed by the exact task id.
	if len(store.receiptCalls) != 1 || store.receiptCalls[0] != tu {
		t.Fatalf("receipt probe must use the exact task id, got %v", store.receiptCalls)
	}
}

// ---------------------------------------------------------------------------
// Run lineage fail-closed (HIV-869)
//
// The RunID on the chain must only survive when the autopilot_run row exists,
// its task_id and issue_id match the projected task, and the parent autopilot
// belongs to the same workspace. Any mismatch clears RunID without dropping
// the valid Issue/Task linkage.
// ---------------------------------------------------------------------------

func TestResolveExecutionChain_RunLineageFailClosed(t *testing.T) {
	runID := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	autopilotID := pgtype.UUID{Bytes: [16]byte{8}, Valid: true}

	tests := []struct {
		name string
		seed func(*fakeChainStore)
	}{
		{"unknown run (no row)", func(store *fakeChainStore) {
			// no autopilot_run row at all
		}},
		{"cross-workspace run", func(store *fakeChainStore) {
			store.autopilotRuns[uuidStr(runID)] = db.AutopilotRun{
				ID: runID, AutopilotID: autopilotID, TaskID: tu, IssueID: tu,
			}
			store.autopilots[uuidStr(autopilotID)] = db.Autopilot{
				ID: autopilotID, WorkspaceID: otherWorkspace(),
			}
		}},
		{"task mismatch", func(store *fakeChainStore) {
			store.autopilotRuns[uuidStr(runID)] = db.AutopilotRun{
				ID: runID, AutopilotID: autopilotID,
				TaskID:  pgtype.UUID{Bytes: [16]byte{99}, Valid: true},
				IssueID: tu,
			}
			store.autopilots[uuidStr(autopilotID)] = db.Autopilot{
				ID: autopilotID, WorkspaceID: tu,
			}
		}},
		{"issue mismatch", func(store *fakeChainStore) {
			store.autopilotRuns[uuidStr(runID)] = db.AutopilotRun{
				ID: runID, AutopilotID: autopilotID, TaskID: tu,
				IssueID: pgtype.UUID{Bytes: [16]byte{99}, Valid: true},
			}
			store.autopilots[uuidStr(autopilotID)] = db.Autopilot{
				ID: autopilotID, WorkspaceID: tu,
			}
		}},
		{"missing parent autopilot", func(store *fakeChainStore) {
			store.autopilotRuns[uuidStr(runID)] = db.AutopilotRun{
				ID: runID, AutopilotID: autopilotID, TaskID: tu, IssueID: tu,
			}
			// no autopilot row
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeChainStore()
			store.issues[key(tu, tu)] = seededIssue()
			tt.seed(store)

			task := seededChainTask()
			task.AutopilotRunID = runID

			chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", nil, task)
			if err != nil {
				t.Fatalf("resolveExecutionChain: %v", err)
			}
			if chain.RunID != "" {
				t.Fatalf("RunID must be cleared on lineage failure, got %q", chain.RunID)
			}
			// The valid Issue/Task linkage must survive.
			if chain.TaskID != uuidStr(tu) {
				t.Fatalf("task id must survive, got %q", chain.TaskID)
			}
			if chain.IssueID != uuidStr(tu) {
				t.Fatalf("issue id must survive, got %q", chain.IssueID)
			}
		})
	}
}

func TestResolveExecutionChain_ReceiptLineageMismatchHidesReceipt(t *testing.T) {
	task := seededChainTask()

	tests := []struct {
		name    string
		receipt db.GetExecutionReceiptForWorkWallRow
	}{
		{"receipt issue differs", db.GetExecutionReceiptForWorkWallRow{
			TaskID: tu, WorkspaceID: tu,
			IssueID:        pgtype.UUID{Bytes: [16]byte{5}, Valid: true},
			TerminalStatus: pgtype.Text{String: "completed", Valid: true},
		}},
		{"unfinalized claim has no terminal status", db.GetExecutionReceiptForWorkWallRow{
			TaskID: tu, WorkspaceID: tu, IssueID: tu,
		}},
		{"terminal status outside closed set", db.GetExecutionReceiptForWorkWallRow{
			TaskID: tu, WorkspaceID: tu, IssueID: tu,
			TerminalStatus: pgtype.Text{String: "mostly_done", Valid: true},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeChainStore()
			store.receipts[uuidStr(tu)] = tt.receipt
			chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", nil, task)
			if err != nil {
				t.Fatalf("resolveExecutionChain: %v", err)
			}
			if chain.ExecutionReceiptRef != "" || chain.ExecutionReceiptStatus != "" {
				t.Fatalf("unsafe receipt must stay hidden, got %+v", chain)
			}
		})
	}
}

// seededReceipt builds the narrow receipt row the production query loads.
func seededReceipt(status string) db.GetExecutionReceiptForWorkWallRow {
	return db.GetExecutionReceiptForWorkWallRow{
		TaskID: tu, WorkspaceID: tu, IssueID: tu,
		TerminalStatus: pgtype.Text{String: status, Valid: true},
	}
}

func TestResolveExecutionChain_ReceiptSurfacesOnlyRefAndStatus(t *testing.T) {
	store := newFakeChainStore()
	store.receipts[uuidStr(tu)] = seededReceipt("failed")
	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", nil, seededChainTask())
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}
	// ExecutionChain is a fixed struct of safe strings; assert nothing beyond
	// ref + closed status resolved.
	if chain.ExecutionReceiptStatus != "failed" {
		t.Fatalf("receipt status = %q, want failed", chain.ExecutionReceiptStatus)
	}
	if chain.ExecutionReceiptRef != "receipt://"+uuidStr(tu) {
		t.Fatalf("receipt ref = %q", chain.ExecutionReceiptRef)
	}
}

// TestWorkWallNarrowProjectionsStayNarrow pins the generated narrow row
// shapes the chain reads rely on. If someone widens these queries back to
// SELECT * — reintroducing receipt snapshots/digests/errors, profile
// fixed_args or workspace settings into the Work Wall process path — this
// test fails before any runtime path can load those columns.
func TestWorkWallNarrowProjectionsStayNarrow(t *testing.T) {
	wantFields := func(t *testing.T, typ reflect.Type, want ...string) {
		t.Helper()
		got := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			got[typ.Field(i).Name] = true
		}
		if len(got) != len(want) {
			t.Fatalf("%s carries %d fields (%v), want exactly %v", typ.Name(), len(got), got, want)
		}
		for _, w := range want {
			if !got[w] {
				t.Fatalf("%s is missing required field %q", typ.Name(), w)
			}
		}
	}
	wantFields(t, reflect.TypeOf(db.GetRuntimeProfileForWorkWallRow{}), "ID", "WorkspaceID", "DisplayName")
	wantFields(t, reflect.TypeOf(db.GetExecutionReceiptForWorkWallRow{}), "TaskID", "WorkspaceID", "IssueID", "TerminalStatus")
}

func TestResolveExecutionChain_IssueIdentifierNeedsStoredPrefix(t *testing.T) {
	store := newFakeChainStore()
	store.issues[key(tu, tu)] = seededIssue()

	chain, err := resolveExecutionChain(context.Background(), store, tu, "", nil, seededChainTask())
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}
	if chain.IssueID == "" || chain.IssueTitle == "" {
		t.Fatalf("issue id/title must still hydrate, got %+v", chain)
	}
	if chain.IssueIdentifier != "" {
		t.Fatalf("identifier must stay absent without a stored prefix, got %q", chain.IssueIdentifier)
	}
}

func TestResolveExecutionChain_StoreErrorPropagates(t *testing.T) {
	store := &errChainStore{}
	if _, err := resolveExecutionChain(context.Background(), store, tu, "HIV", nil, seededChainTask()); err == nil {
		t.Fatal("storage errors must propagate, not silently degrade")
	}
}

type errChainStore struct{ fakeChainStore }

func (e *errChainStore) GetIssueInWorkspace(_ context.Context, _ db.GetIssueInWorkspaceParams) (db.Issue, error) {
	return db.Issue{}, errors.New("storage down")
}

func TestResolveIssuePrefix_ReadsStoredWorkspacePrefix(t *testing.T) {
	store := newFakeChainStore()
	prefix, err := resolveIssuePrefix(context.Background(), store, tu)
	if err != nil {
		t.Fatalf("resolveIssuePrefix: %v", err)
	}
	if prefix != "HIV" {
		t.Fatalf("prefix = %q, want HIV", prefix)
	}
	if _, err := resolveIssuePrefix(context.Background(), store, otherWorkspace()); err == nil {
		t.Fatal("unknown workspace must fail loudly")
	}
}

func TestReceiptTerminalStatusOK_ClosedSet(t *testing.T) {
	for _, ok := range []string{"completed", "failed", "cancelled"} {
		if !receiptTerminalStatusOK(ok) {
			t.Fatalf("%q must be in the closed set", ok)
		}
	}
	for _, bad := range []string{"", "running", "mostly_done", "Completed"} {
		if receiptTerminalStatusOK(bad) {
			t.Fatalf("%q must be outside the closed set", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Execution-runtime lineage (HIV-940)
//
// The card's current runtime fields (RuntimeID / RuntimeCarrier) come from
// the agent's CURRENT binding. The execution-runtime fields come from the
// Task's own runtime_id. After an A→B rebind the card shows current B but
// execution A. Missing/unknown Task Runtime omits execution fields without
// dropping the rest of the chain.
// ---------------------------------------------------------------------------

func taskRuntimeUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}, Valid: true}
}

func otherProfileUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3}, Valid: true}
}

func TestResolveExecutionChain_TaskRuntimeRebindShowsExecutionA(t *testing.T) {
	store := newFakeChainStore()
	store.issues[key(tu, tu)] = seededIssue()

	taskRT := taskRuntimeUUID()
	taskProfile := otherProfileUUID()

	// Agent's current runtime (tu) has profile tu.
	store.profiles[key(tu, tu)] = db.GetRuntimeProfileForWorkWallRow{
		ID: tu, WorkspaceID: tu, DisplayName: "Prime 当前档案",
	}
	// Task's runtime (taskRT) has profile taskProfile — a DIFFERENT profile.
	store.runtimes[key(tu, taskRT)] = db.AgentRuntime{
		ID: taskRT, WorkspaceID: tu, Provider: "codex", Status: "online", ProfileID: taskProfile,
	}
	store.profiles[key(tu, taskProfile)] = db.GetRuntimeProfileForWorkWallRow{
		ID: taskProfile, WorkspaceID: tu, DisplayName: "Codex 执行档案",
	}

	task := seededChainTask()
	task.RuntimeID = taskRT

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", seededRuntime(), task)
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}

	// Current profile remains bound to the Agent's current runtime.
	if chain.RuntimeProfileID != uuidStr(tu) || chain.RuntimeProfileName != "Prime 当前档案" {
		t.Fatalf("profile must follow current runtime, got profile=%q name=%q", chain.RuntimeProfileID, chain.RuntimeProfileName)
	}
	// Execution-runtime fields trace the Task's original runtime.
	if chain.ExecutionRuntimeID != uuidStr(taskRT) {
		t.Fatalf("execution_runtime_id = %q, want %q", chain.ExecutionRuntimeID, uuidStr(taskRT))
	}
	if chain.ExecutionRuntimeCarrier != "codex" {
		t.Fatalf("execution_runtime_carrier = %q, want codex", chain.ExecutionRuntimeCarrier)
	}
	if chain.ExecutionProfileID != uuidStr(taskProfile) || chain.ExecutionProfileName != "Codex 执行档案" {
		t.Fatalf("execution profile = %q / %q", chain.ExecutionProfileID, chain.ExecutionProfileName)
	}
	// The rest of the chain survives.
	if chain.TaskID != uuidStr(tu) || chain.IssueID != uuidStr(tu) {
		t.Fatalf("task/issue must survive rebind, got %+v", chain)
	}
}

func TestResolveExecutionChain_MissingTaskRuntimeOmitsExecutionFields(t *testing.T) {
	store := newFakeChainStore()
	store.issues[key(tu, tu)] = seededIssue()
	store.profiles[key(tu, tu)] = db.GetRuntimeProfileForWorkWallRow{
		ID: tu, WorkspaceID: tu, DisplayName: "glm-5.3 运行档案",
	}

	unknownRT := pgtype.UUID{Bytes: [16]byte{8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8}, Valid: true}
	task := seededChainTask()
	task.RuntimeID = unknownRT
	// No runtime row seeded for unknownRT — simulates a deleted/unknown runtime.

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", seededRuntime(), task)
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}

	// Execution fields must be empty — the runtime row is missing.
	if chain.ExecutionRuntimeID != "" || chain.ExecutionRuntimeCarrier != "" {
		t.Fatalf("missing task runtime must omit execution fields, got %+v", chain)
	}
	if chain.ExecutionProfileID != "" || chain.ExecutionProfileName != "" {
		t.Fatalf("missing task runtime must omit execution profile, got %+v", chain)
	}
	// Profile falls back to the agent's current runtime.
	if chain.RuntimeProfileID != uuidStr(tu) || chain.RuntimeProfileName != "glm-5.3 运行档案" {
		t.Fatalf("profile must fall back to current runtime, got %q / %q", chain.RuntimeProfileID, chain.RuntimeProfileName)
	}
	// The rest of the chain survives.
	if chain.TaskID != uuidStr(tu) || chain.IssueIdentifier != "HIV-797" {
		t.Fatalf("task/issue must survive missing task runtime, got %+v", chain)
	}
}

func TestResolveExecutionChain_TaskWithoutRuntimeIDUsesCurrentRuntime(t *testing.T) {
	store := newFakeChainStore()
	store.issues[key(tu, tu)] = seededIssue()
	store.profiles[key(tu, tu)] = db.GetRuntimeProfileForWorkWallRow{
		ID: tu, WorkspaceID: tu, DisplayName: "glm-5.3 运行档案",
	}

	task := seededChainTask()
	task.RuntimeID = pgtype.UUID{} // no task runtime

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", seededRuntime(), task)
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}

	// No execution-runtime fields — the task has no runtime_id.
	if chain.ExecutionRuntimeID != "" || chain.ExecutionRuntimeCarrier != "" {
		t.Fatalf("task without runtime_id must not set execution fields, got %+v", chain)
	}
	if chain.ExecutionProfileID != "" {
		t.Fatalf("task without runtime_id must not set execution profile, got %+v", chain)
	}
	// Profile comes from the agent's current runtime (backward compatible).
	if chain.RuntimeProfileID != uuidStr(tu) {
		t.Fatalf("profile must come from current runtime, got %q", chain.RuntimeProfileID)
	}
}

func TestResolveExecutionChain_NilTaskNoExecutionRuntime(t *testing.T) {
	store := newFakeChainStore()
	store.profiles[key(tu, tu)] = db.GetRuntimeProfileForWorkWallRow{
		ID: tu, WorkspaceID: tu, DisplayName: "当前档案",
	}

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", seededRuntime(), nil)
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}
	if chain == nil {
		t.Fatal("idle runtime with profile must return a profile-only chain")
	}
	if chain.ExecutionRuntimeID != "" || chain.ExecutionRuntimeCarrier != "" {
		t.Fatalf("nil task must not set execution runtime, got %+v", chain)
	}
	if chain.ExecutionProfileID != "" {
		t.Fatalf("nil task must not set execution profile, got %+v", chain)
	}
	if chain.RuntimeProfileID != uuidStr(tu) || chain.RuntimeProfileName != "当前档案" {
		t.Fatalf("profile must come from current runtime for idle card, got %+v", chain)
	}
}

func TestResolveExecutionChain_TaskRuntimeSameAsCurrentSetsExecutionFields(t *testing.T) {
	store := newFakeChainStore()
	store.issues[key(tu, tu)] = seededIssue()
	store.profiles[key(tu, tu)] = db.GetRuntimeProfileForWorkWallRow{
		ID: tu, WorkspaceID: tu, DisplayName: "glm-5.3 运行档案",
	}
	// Seed the runtime row so the task runtime lookup succeeds.
	store.runtimes[key(tu, tu)] = db.AgentRuntime{
		ID: tu, WorkspaceID: tu, Provider: "prime", Status: "online", ProfileID: tu,
	}

	// Task's runtime_id matches the agent's current runtime (tu).
	task := seededChainTask()
	task.RuntimeID = tu

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", seededRuntime(), task)
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}

	// Execution fields are populated because the task runtime was resolved.
	if chain.ExecutionRuntimeID != uuidStr(tu) {
		t.Fatalf("execution_runtime_id = %q, want %q", chain.ExecutionRuntimeID, uuidStr(tu))
	}
	if chain.ExecutionRuntimeCarrier != "prime" {
		t.Fatalf("execution_runtime_carrier = %q, want prime", chain.ExecutionRuntimeCarrier)
	}
	// Profile comes from the task's runtime (same as current in this case).
	if chain.RuntimeProfileID != uuidStr(tu) {
		t.Fatalf("profile = %q", chain.RuntimeProfileID)
	}
	if chain.ExecutionProfileID != uuidStr(tu) {
		t.Fatalf("execution profile = %q", chain.ExecutionProfileID)
	}
}

func TestResolveExecutionChain_CrossWorkspaceTaskRuntimeFailsClosed(t *testing.T) {
	store := newFakeChainStore()
	store.issues[key(tu, tu)] = seededIssue()
	store.profiles[key(tu, tu)] = db.GetRuntimeProfileForWorkWallRow{
		ID: tu, WorkspaceID: tu, DisplayName: "glm-5.3 运行档案",
	}
	other := otherWorkspace()

	taskRT := taskRuntimeUUID()
	// The task runtime exists but in a DIFFERENT workspace.
	store.runtimes[key(other, taskRT)] = db.AgentRuntime{
		ID: taskRT, WorkspaceID: other, Provider: "leaked", Status: "online",
	}

	task := seededChainTask()
	task.RuntimeID = taskRT

	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", seededRuntime(), task)
	if err != nil {
		t.Fatalf("resolveExecutionChain: %v", err)
	}

	// Cross-workspace runtime must fail closed.
	if chain.ExecutionRuntimeID != "" || chain.ExecutionRuntimeCarrier != "" {
		t.Fatalf("cross-workspace task runtime must fail closed, got %+v", chain)
	}
	// Profile falls back to current runtime.
	if chain.RuntimeProfileID != uuidStr(tu) {
		t.Fatalf("profile must fall back to current runtime, got %q", chain.RuntimeProfileID)
	}
}
