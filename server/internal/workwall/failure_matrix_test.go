package workwall

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"errors"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestFailureMatrix_WorkWallSQLNeverSelectsSecretOrPayloadColumns statically
// asserts that the Work Wall SQL projection reads ONLY the columns the card
// renders. If a future edit widens any of the three queries back to a
// sensitive column (receipt snapshots/digests/errors, workspace
// settings/context, profile command/fixed_args), this test fails at compile
// time — before the production path can ever load that data.
//
// This is the source-level counterpart to
// TestWorkWallNarrowProjectionsStayNarrow, which pins the generated Go row
// shapes. Together they prove the narrow-read contract on both sides: the
// SQL cannot ask for forbidden columns, and the generated row cannot carry
// them into the Work Wall process.
func TestFailureMatrix_WorkWallSQLNeverSelectsSecretOrPayloadColumns(t *testing.T) {
	path := filepath.Join("..", "..", "pkg", "db", "queries", "workwall.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workwall.sql: %v", err)
	}
	sqlLower := strings.ToLower(string(raw))
	// Strip SQL line comments ("-- ...") for the forbidden-column guard so
	// explanatory text in comments cannot trip it. Name/select extraction
	// below still sees the original (with "-- name:" markers).
	var b strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	sqlStripped := strings.ToLower(b.String())
	_ = sqlLower

	// Positive guard: the three expected narrow projections are present and
	// each selects exactly the columns we intend.
	type queryExpect struct {
		name       string
		table      string
		allowedCol []string
	}
	expectations := []queryExpect{
		{
			name:       "GetWorkspaceIssuePrefix",
			table:      "workspace",
			allowedCol: []string{"issue_prefix"},
		},
		{
			name:       "GetRuntimeProfileForWorkWall",
			table:      "runtime_profile",
			allowedCol: []string{"id", "workspace_id", "display_name"},
		},
		{
			name:       "GetExecutionReceiptForWorkWall",
			table:      "execution_receipt",
			allowedCol: []string{"task_id", "workspace_id", "issue_id", "terminal_status"},
		},
	}
	for _, q := range expectations {
		selectClause := extractSelectClause(t, sqlLower, q.name, q.table)
		gotCols := splitColumns(t, selectClause)
		if len(gotCols) != len(q.allowedCol) {
			t.Fatalf("%s selects %d columns %v, want exactly %v", q.name, len(gotCols), gotCols, q.allowedCol)
		}
		wantSet := map[string]bool{}
		for _, c := range q.allowedCol {
			wantSet[c] = true
		}
		for _, c := range gotCols {
			if !wantSet[c] {
				t.Fatalf("%s selects disallowed column %q (allowed set: %v)", q.name, c, q.allowedCol)
			}
		}
	}

	// Negative guard: no Work Wall query may touch a sensitive column by name
	// anywhere in this file. We list column names, not table-qualified
	// identifiers, so a "SELECT table.*" cannot bypass the check either (that
	// is also rejected by the exact-column assertions above).
	forbiddenSubstrings := []string{
		// receipt payload / lineage digest columns
		"runtime_snapshot", "result_snapshot", "terminal_error",
		"runtime_digest", "output_digest", "input_digest",
		"work_order_digest", "employee_digest", "binding_digest", "agent_digest",
		// workspace-wide settings/context (we must only read issue_prefix)
		"settings", "context", "repos",
		// runtime profile execution payload
		"command_name", "fixed_args", "protocol_family",
		// catch-all select, which would defeat every column-level guard
		"*",
	}
	for _, needle := range forbiddenSubstrings {
		if strings.Contains(sqlStripped, needle) {
			t.Fatalf("workwall.sql must not reference %q; the Work Wall projection is a narrow read", needle)
		}
	}
}

// name: GetWorkspaceIssuePrefix  marker is matched case-insensitively; the
// following SELECT ... FROM <table> block is returned in lower case.
var queryBlockRe = map[string]*regexp.Regexp{}

func init() {
	for _, q := range []string{"GetWorkspaceIssuePrefix", "GetRuntimeProfileForWorkWall", "GetExecutionReceiptForWorkWall"} {
		pattern := `(?is)-- name:\s*` + regexp.QuoteMeta(strings.ToLower(q)) + `[ :].*?select\s+(.*?)\s+from\s+`
		queryBlockRe[q] = regexp.MustCompile(pattern)
	}
}

func extractSelectClause(t *testing.T, sqlLower string, name, table string) string {
	t.Helper()
	re := queryBlockRe[name]
	if re == nil {
		t.Fatalf("no regexp prepared for %s", name)
	}
	m := re.FindStringSubmatch(sqlLower)
	if m == nil {
		t.Fatalf("query %s not found in workwall.sql", name)
	}
	// Guard that the FROM target is exactly the table we expect.
	rest := sqlLower[re.FindStringIndex(sqlLower)[1]:]
	if !strings.HasPrefix(strings.TrimSpace(rest), table) {
		t.Fatalf("query %s selects from %q, want table %q", name, strings.TrimSpace(rest)[:len(table)+5], table)
	}
	return m[1]
}

func splitColumns(t *testing.T, clause string) []string {
	t.Helper()
	parts := strings.Split(clause, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		c := strings.TrimSpace(strings.ToLower(p))
		// strip an optional table alias prefix such as "rp.id"
		if i := strings.LastIndex(c, "."); i >= 0 {
			c = c[i+1:]
		}
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// TestFailureMatrix_TerminalTaskStatusMatrix proves that for every terminal
// agent_task_queue status the chain walks correctly: a completed/failed/
// cancelled task surfaces its receipt status ONLY when the receipt lineage
// matches AND the status is in the closed terminal set, regardless of which
// task status produced it. The absent Run ID case for direct tasks is also
// covered (no autopilot_run_id -> empty RunID).
func TestFailureMatrix_TerminalTaskStatusMatrix(t *testing.T) {
	terminal := []string{"completed", "failed", "cancelled"}
	for _, taskStatus := range terminal {
		taskStatus := taskStatus
		t.Run("task_"+taskStatus, func(t *testing.T) {
			task := &db.AgentTaskQueue{
				ID:      tu,
				AgentID: tu,
				IssueID: tu,
				Status:  taskStatus,
				// direct task, no Run row
			}

			// (a) Matching receipt with the same terminal status surfaces.
			store := newFakeChainStore()
			store.issues[key(tu, tu)] = seededIssue()
			store.receipts[uuidStr(tu)] = seededReceipt(taskStatus)
			chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", nil, task)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if chain.RunID != "" {
				t.Fatalf("direct terminal task must not fabricate a Run ID, got %q", chain.RunID)
			}
			if chain.ExecutionReceiptRef == "" || chain.ExecutionReceiptStatus != taskStatus {
				t.Fatalf("matching receipt must surface for %s task, got %+v", taskStatus, chain)
			}

			// (b) No receipt row stays absent — a terminal task does not
			// imply a receipt existed.
			store2 := newFakeChainStore()
			store2.issues[key(tu, tu)] = seededIssue()
			chain2, err := resolveExecutionChain(context.Background(), store2, tu, "HIV", nil, task)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if chain2.ExecutionReceiptRef != "" || chain2.ExecutionReceiptStatus != "" {
				t.Fatalf("absent receipt must stay absent for %s task, got %+v", taskStatus, chain2)
			}

			// (c) Receipt with a mismatched issue lineage is hidden even
			// though its terminal status looks valid.
			store3 := newFakeChainStore()
			store3.receipts[uuidStr(tu)] = db.GetExecutionReceiptForWorkWallRow{
				TaskID:         tu,
				WorkspaceID:    tu,
				IssueID:        pgtype.UUID{Bytes: [16]byte{5}, Valid: true},
				TerminalStatus: pgtype.Text{String: taskStatus, Valid: true},
			}
			chain3, err := resolveExecutionChain(context.Background(), store3, tu, "HIV", nil, task)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if chain3.ExecutionReceiptRef != "" {
				t.Fatalf("lineage-mismatched receipt must stay hidden for %s task", taskStatus)
			}
		})
	}
}

// TestFailureMatrix_StaleAndMissingRuntime proves that a missing or stale
// runtime still hydrates the execution chain from authoritative task/issue/
// receipt rows (those identifiers are independent of runtime heartbeat),
// while the card's freshness reflects the runtime state accurately. The
// chain must never invent a RuntimeProfile when the runtime is absent or has
// no bound profile.
func TestFailureMatrix_StaleAndMissingRuntime(t *testing.T) {
	now := time.Now().UTC()
	task := seededChainTask() // running task with an issue
	store := newFakeChainStore()
	store.issues[key(tu, tu)] = seededIssue()
	store.receipts[uuidStr(tu)] = seededReceipt("completed")

	// (a) Missing runtime row: chain still traces issue/receipt; profile
	// stays absent; card freshness is "missing".
	chainMissing, err := resolveExecutionChain(context.Background(), store, tu, "HIV", nil, task)
	if err != nil {
		t.Fatalf("resolve (missing runtime): %v", err)
	}
	if chainMissing.IssueID == "" || chainMissing.ExecutionReceiptRef == "" {
		t.Fatalf("chain must still trace issue/receipt without a runtime row, got %+v", chainMissing)
	}
	if chainMissing.RuntimeProfileID != "" || chainMissing.RuntimeProfileName != "" {
		t.Fatalf("missing runtime must not surface a profile, got %+v", chainMissing)
	}
	dtoMissing := AssembleAgent(agent(), nil, task, nil, chainMissing, nil, now, 0)
	if dtoMissing.FreshnessState != liveactivity.FreshnessMissing {
		t.Fatalf("missing runtime -> freshness %q, want missing", dtoMissing.FreshnessState)
	}

	// (b) Stale runtime row (online but last_seen beyond threshold): chain
	// still traces issue/receipt/profile when a profile exists; card
	// freshness is "stale".
	staleRT := seededRuntime()
	staleRT.LastSeenAt = pgtype.Timestamptz{Time: now.Add(-10 * time.Minute), Valid: true}
	store.profiles[key(tu, tu)] = db.GetRuntimeProfileForWorkWallRow{
		ID: tu, WorkspaceID: tu, DisplayName: "glm-5.3 运行档案",
	}
	chainStale, err := resolveExecutionChain(context.Background(), store, tu, "HIV", staleRT, task)
	if err != nil {
		t.Fatalf("resolve (stale runtime): %v", err)
	}
	if chainStale.RuntimeProfileID == "" {
		t.Fatalf("stale runtime with a bound profile must still hydrate the profile")
	}
	dtoStale := AssembleAgent(agent(), staleRT, task, nil, chainStale, nil, now, 0)
	if dtoStale.FreshnessState != liveactivity.FreshnessStale {
		t.Fatalf("stale runtime -> freshness %q, want stale", dtoStale.FreshnessState)
	}
	// A stale runtime must NOT be reported as "working" even with a running
	// task — fail-closed to unknown.
	if dtoStale.PresenceState != liveactivity.PresenceUnknown {
		t.Fatalf("stale runtime + running task -> presence %q, want unknown", dtoStale.PresenceState)
	}

	// (c) Runtime present but with no bound profile_id: profile stays absent.
	rtNoProfile := seededRuntime()
	rtNoProfile.ProfileID = pgtype.UUID{}
	rtNoProfile.LastSeenAt = pgtype.Timestamptz{Time: now.Add(-time.Second), Valid: true}
	chainNoProfile, err := resolveExecutionChain(context.Background(), store, tu, "HIV", rtNoProfile, task)
	if err != nil {
		t.Fatalf("resolve (no profile): %v", err)
	}
	if chainNoProfile.RuntimeProfileID != "" || chainNoProfile.RuntimeProfileName != "" {
		t.Fatalf("runtime without profile_id must not surface a profile, got %+v", chainNoProfile)
	}
}

// TestFailureMatrix_CrossWorkspaceIsolationForEveryLink proves each
// individual chain link (issue, project, profile, receipt) is independently
// workspace-scoped: even when the other links are correctly present in the
// snapshot workspace, a single link that only exists across the workspace
// boundary fails closed and does not leak. This extends the existing
// all-cross-workspace test with per-link isolation cases.
func TestFailureMatrix_CrossWorkspaceIsolationForEveryLink(t *testing.T) {
	other := otherWorkspace()
	runtime := seededRuntime()
	task := seededChainTask()

	cases := []struct {
		name    string
		seed    func(store *fakeChainStore)
		wantBad func(c *ExecutionChain) bool
	}{
		{
			name: "issue only exists in other workspace",
			seed: func(s *fakeChainStore) {
				s.issues[key(other, tu)] = seededIssue()
			},
			wantBad: func(c *ExecutionChain) bool {
				return c.IssueID != "" || c.IssueIdentifier != "" || c.ProjectID != ""
			},
		},
		{
			name: "project exists in other workspace only",
			seed: func(s *fakeChainStore) {
				iss := seededIssue()
				s.issues[key(tu, tu)] = iss
				s.projects[key(other, tu)] = db.Project{ID: tu, WorkspaceID: other, Title: "别的项目"}
			},
			wantBad: func(c *ExecutionChain) bool {
				return c.ProjectID != ""
			},
		},
		{
			name: "runtime profile exists in other workspace only",
			seed: func(s *fakeChainStore) {
				s.profiles[key(other, tu)] = db.GetRuntimeProfileForWorkWallRow{
					ID: tu, WorkspaceID: other, DisplayName: "别的档案",
				}
			},
			wantBad: func(c *ExecutionChain) bool {
				return c.RuntimeProfileID != ""
			},
		},
		{
			name: "receipt workspace does not match",
			seed: func(s *fakeChainStore) {
				s.issues[key(tu, tu)] = seededIssue()
				s.receipts[uuidStr(tu)] = db.GetExecutionReceiptForWorkWallRow{
					TaskID:         tu,
					WorkspaceID:    other,
					IssueID:        tu,
					TerminalStatus: pgtype.Text{String: "completed", Valid: true},
				}
			},
			wantBad: func(c *ExecutionChain) bool {
				return c.ExecutionReceiptRef != ""
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeChainStore()
			tc.seed(store)
			chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", runtime, task)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if tc.wantBad(chain) {
				t.Fatalf("cross-workspace leak: %+v", chain)
			}
		})
	}
}

// TestFailureMatrix_ReceiptInvalidTerminalStatusMatrix verifies the receipt
// gate for every terminal status variant the schema could carry, including
// NULL (unfinalized claim) and casing mismatches. Only the exact closed set
// passes.
func TestFailureMatrix_ReceiptInvalidTerminalStatusMatrix(t *testing.T) {
	task := seededChainTask()
	cases := []struct {
		name   string
		status pgtype.Text
		safe   bool
	}{
		{"completed is safe", pgtype.Text{String: "completed", Valid: true}, true},
		{"failed is safe", pgtype.Text{String: "failed", Valid: true}, true},
		{"cancelled is safe", pgtype.Text{String: "cancelled", Valid: true}, true},
		{"NULL unfinalized is hidden", pgtype.Text{Valid: false}, false},
		{"empty string is hidden", pgtype.Text{String: "", Valid: true}, false},
		{"running is hidden", pgtype.Text{String: "running", Valid: true}, false},
		{"dispatched is hidden", pgtype.Text{String: "dispatched", Valid: true}, false},
		{"capitalized Completed is hidden (case-sensitive)", pgtype.Text{String: "Completed", Valid: true}, false},
		{"unknown value is hidden", pgtype.Text{String: "weird_status", Valid: true}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeChainStore()
			store.receipts[uuidStr(tu)] = db.GetExecutionReceiptForWorkWallRow{
				TaskID:         tu,
				WorkspaceID:    tu,
				IssueID:        tu,
				TerminalStatus: tc.status,
			}
			chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", nil, task)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if tc.safe {
				if chain.ExecutionReceiptRef == "" || chain.ExecutionReceiptStatus != tc.status.String {
					t.Fatalf("safe receipt %q must surface, got %+v", tc.status.String, chain)
				}
			} else {
				if chain.ExecutionReceiptRef != "" || chain.ExecutionReceiptStatus != "" {
					t.Fatalf("unsafe receipt %+v must be hidden, got ref=%q status=%q", tc.status, chain.ExecutionReceiptRef, chain.ExecutionReceiptStatus)
				}
			}
		})
	}
}

// TestFailureMatrix_AbsentRunIDForDirectTasks covers the case called out in
// the work order: "absent Run ID". Direct (comment/assignment-triggered)
// tasks have no autopilot_run_id, so the Work Wall must never fabricate one
// — neither in the chain nor on the DTO.
func TestFailureMatrix_AbsentRunIDForDirectTasks(t *testing.T) {
	now := time.Now().UTC()
	task := &db.AgentTaskQueue{
		ID:      tu,
		AgentID: tu,
		IssueID: tu,
		Status:  "running",
		// AutopilotRunID explicitly invalid.
	}
	store := newFakeChainStore()
	store.issues[key(tu, tu)] = seededIssue()
	chain, err := resolveExecutionChain(context.Background(), store, tu, "HIV", nil, task)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if chain.RunID != "" {
		t.Fatalf("direct task must have no Run ID in chain, got %q", chain.RunID)
	}
	dto := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), task, nil, chain, nil, now, 0)
	if dto.RunID != "" {
		t.Fatalf("DTO must not fabricate run_id for a direct task, got %q", dto.RunID)
	}
}

// TestFailureMatrix_StorageErrorPropagatesForEveryLink proves that any
// non-ErrNoRows storage failure while walking ANY chain link surfaces to the
// caller rather than silently degrading the card. ErrNoRows on a missing
// link is the normal "absent" path and must not error.
func TestFailureMatrix_StorageErrorPropagatesForEveryLink(t *testing.T) {
	task := seededChainTask()
	boom := errors.New("storage down")

	t.Run("issue lookup error propagates", func(t *testing.T) {
		s := &errChainStore{}
		if _, err := resolveExecutionChain(context.Background(), s, tu, "HIV", nil, task); err == nil {
			t.Fatal("issue storage error must propagate")
		}
	})
	t.Run("project lookup error propagates", func(t *testing.T) {
		s := newFakeChainStore()
		s.issues[key(tu, tu)] = seededIssue()
		w := &projectErrStore{fakeChainStore: s, err: boom}
		if _, err := resolveExecutionChain(context.Background(), w, tu, "HIV", nil, task); err == nil {
			t.Fatal("project storage error must propagate")
		}
	})
	t.Run("profile lookup error propagates", func(t *testing.T) {
		s := newFakeChainStore()
		w := &profileErrStore{fakeChainStore: s, err: boom}
		if _, err := resolveExecutionChain(context.Background(), w, tu, "HIV", seededRuntime(), task); err == nil {
			t.Fatal("profile storage error must propagate")
		}
	})
	t.Run("receipt lookup error propagates", func(t *testing.T) {
		s := newFakeChainStore()
		w := &receiptErrStore{fakeChainStore: s, err: boom}
		if _, err := resolveExecutionChain(context.Background(), w, tu, "HIV", nil, task); err == nil {
			t.Fatal("receipt storage error must propagate")
		}
	})
	t.Run("ErrNoRows on every link is not an error", func(t *testing.T) {
		s := newFakeChainStore() // nothing seeded
		if _, err := resolveExecutionChain(context.Background(), s, tu, "HIV", seededRuntime(), task); err != nil {
			t.Fatalf("absent rows are normal, got error %v", err)
		}
	})
}

type projectErrStore struct {
	*fakeChainStore
	err error
}

func (s *projectErrStore) GetProjectInWorkspace(_ context.Context, _ db.GetProjectInWorkspaceParams) (db.Project, error) {
	return db.Project{}, s.err
}

type profileErrStore struct {
	*fakeChainStore
	err error
}

func (s *profileErrStore) GetRuntimeProfileForWorkWall(_ context.Context, _ db.GetRuntimeProfileForWorkWallParams) (db.GetRuntimeProfileForWorkWallRow, error) {
	return db.GetRuntimeProfileForWorkWallRow{}, s.err
}

type receiptErrStore struct {
	*fakeChainStore
	err error
}

func (s *receiptErrStore) GetExecutionReceiptForWorkWall(_ context.Context, _ pgtype.UUID) (db.GetExecutionReceiptForWorkWallRow, error) {
	return db.GetExecutionReceiptForWorkWallRow{}, s.err
}
