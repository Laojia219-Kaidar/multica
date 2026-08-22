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
	workspaceID  pgtype.UUID
	issuePrefix  string
	issues       map[string]db.Issue   // key: workspace|issue
	projects     map[string]db.Project // key: workspace|project
	profiles     map[string]db.GetRuntimeProfileForWorkWallRow
	receipts     map[string]db.GetExecutionReceiptForWorkWallRow // key: task
	crossCalls   int                                             // lookups whose workspace arg mismatched the stored row
	issueCalls   []string
	receiptCalls []pgtype.UUID
}

func newFakeChainStore() *fakeChainStore {
	return &fakeChainStore{
		workspaceID: tu,
		issuePrefix: "HIV",
		issues:      map[string]db.Issue{},
		projects:    map[string]db.Project{},
		profiles:    map[string]db.GetRuntimeProfileForWorkWallRow{},
		receipts:    map[string]db.GetExecutionReceiptForWorkWallRow{},
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
	task.AutopilotRunID = pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
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
	if chain.RunID != uuidStr(pgtype.UUID{Bytes: [16]byte{7}, Valid: true}) {
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
