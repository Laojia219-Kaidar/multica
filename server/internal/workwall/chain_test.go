package workwall

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fakeChainStore is an in-memory chainStore. Workspace scoping is modelled
// exactly like the SQL: a lookup whose workspace does not match returns
// pgx.ErrNoRows, so tests prove the walk never falls back across workspaces.
type fakeChainStore struct {
	workspace    db.Workspace
	issues       map[string]db.Issue   // key: workspace|issue
	projects     map[string]db.Project // key: workspace|project
	profiles     map[string]db.RuntimeProfile
	receipts     map[string]db.ExecutionReceipt
	crossCalls   int // lookups whose workspace arg mismatched the stored row
	issueCalls   []string
	receiptCalls []pgtype.UUID
}

func newFakeChainStore() *fakeChainStore {
	return &fakeChainStore{
		workspace: db.Workspace{ID: tu, IssuePrefix: "HIV"},
		issues:    map[string]db.Issue{},
		projects:  map[string]db.Project{},
		profiles:  map[string]db.RuntimeProfile{},
		receipts:  map[string]db.ExecutionReceipt{},
	}
}

func key(ws pgtype.UUID, id pgtype.UUID) string { return uuidStr(ws) + "|" + uuidStr(id) }

func (f *fakeChainStore) GetWorkspace(_ context.Context, id pgtype.UUID) (db.Workspace, error) {
	if id != f.workspace.ID {
		return db.Workspace{}, pgx.ErrNoRows
	}
	return f.workspace, nil
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

func (f *fakeChainStore) GetRuntimeProfileForWorkspace(_ context.Context, arg db.GetRuntimeProfileForWorkspaceParams) (db.RuntimeProfile, error) {
	profile, ok := f.profiles[key(arg.WorkspaceID, arg.ID)]
	if !ok {
		return db.RuntimeProfile{}, pgx.ErrNoRows
	}
	return profile, nil
}

func (f *fakeChainStore) GetExecutionReceipt(_ context.Context, taskID pgtype.UUID) (db.ExecutionReceipt, error) {
	f.receiptCalls = append(f.receiptCalls, taskID)
	receipt, ok := f.receipts[uuidStr(taskID)]
	if !ok {
		return db.ExecutionReceipt{}, pgx.ErrNoRows
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
	store.profiles[key(tu, tu)] = db.RuntimeProfile{ID: tu, WorkspaceID: tu, DisplayName: "glm-5.3 运行档案", Enabled: true}
	task := seededChainTask()
	task.AutopilotRunID = pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	store.receipts[uuidStr(tu)] = db.ExecutionReceipt{
		TaskID: tu, WorkspaceID: tu, IssueID: tu,
		TerminalStatus: pgtype.Text{String: "completed", Valid: true},
		ResultSnapshot: []byte(`{"secret":"must-not-be-read"}`),
		TerminalError:  pgtype.Text{String: "postgres://u:secret@h/db", Valid: true},
	}

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
	store.profiles[key(other, tu)] = db.RuntimeProfile{ID: tu, WorkspaceID: other, DisplayName: "别的工作区档案"}
	store.receipts[uuidStr(tu)] = db.ExecutionReceipt{
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
		receipt db.ExecutionReceipt
	}{
		{"receipt issue differs", db.ExecutionReceipt{
			TaskID: tu, WorkspaceID: tu,
			IssueID:        pgtype.UUID{Bytes: [16]byte{5}, Valid: true},
			TerminalStatus: pgtype.Text{String: "completed", Valid: true},
		}},
		{"unfinalized claim has no terminal status", db.ExecutionReceipt{
			TaskID: tu, WorkspaceID: tu, IssueID: tu,
		}},
		{"terminal status outside closed set", db.ExecutionReceipt{
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

func TestResolveExecutionChain_ReceiptNeverCarriesPayloadFields(t *testing.T) {
	store := newFakeChainStore()
	store.receipts[uuidStr(tu)] = db.ExecutionReceipt{
		TaskID: tu, WorkspaceID: tu, IssueID: tu,
		TerminalStatus:  pgtype.Text{String: "failed", Valid: true},
		RuntimeSnapshot: []byte(`{"env":{"OPENAI_API_KEY":"sk-secret"}}`),
		ResultSnapshot:  []byte(`{"stdout":"DATABASE_URL=postgres://u:p@h/db"}`),
		TerminalError:   pgtype.Text{String: "leaked secret text", Valid: true},
		WorkOrderDigest: "digest-secret",
	}
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
	for _, forbidden := range []string{"sk-secret", "postgres://", "leaked secret text", "digest-secret"} {
		for _, field := range []string{chain.ExecutionReceiptRef, chain.ExecutionReceiptStatus, chain.IssueTitle, chain.ProjectTitle, chain.RuntimeProfileName} {
			if field == forbidden {
				t.Fatalf("chain leaked receipt payload %q", forbidden)
			}
		}
	}
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
