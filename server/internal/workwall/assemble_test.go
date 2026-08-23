package workwall

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var tu = pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, Valid: true}

func agent() db.Agent {
	return db.Agent{
		ID:          tu,
		WorkspaceID: tu,
		Name:        "Emory",
		AvatarUrl:   pgtype.Text{String: "https://cdn/e.png", Valid: true},
		Model:       pgtype.Text{String: "deepseek-v4", Valid: true},
		RuntimeID:   tu,
	}
}

func rt(status string, lastSeen time.Time) *db.AgentRuntime {
	return &db.AgentRuntime{
		ID:         tu,
		Provider:   "prime",
		Status:     status,
		LastSeenAt: pgtype.Timestamptz{Time: lastSeen, Valid: true},
	}
}

func task(status string, t time.Time) *db.AgentTaskQueue {
	atq := &db.AgentTaskQueue{
		ID:      tu,
		AgentID: tu,
		IssueID: tu,
		Status:  status,
	}
	switch status {
	case "completed", "failed":
		if !t.IsZero() {
			atq.CompletedAt = pgtype.Timestamptz{Time: t, Valid: true}
		}
	default:
		if !t.IsZero() {
			atq.StartedAt = pgtype.Timestamptz{Time: t, Valid: true}
		}
	}
	return atq
}

func TestAssembleAgent_PresenceMatrix(t *testing.T) {
	now := time.Now().UTC()
	fresh := now.Add(-5 * time.Second)
	stale := now.Add(-10 * time.Minute)

	tests := []struct {
		name        string
		rt          *db.AgentRuntime
		active      *db.AgentTaskQueue
		lastOutcome *db.AgentTaskQueue
		want        liveactivity.PresenceState
		wantFresh   liveactivity.FreshnessState
	}{
		{"runtime offline -> offline", rt("offline", fresh), task("running", now.Add(-time.Minute)), nil, liveactivity.PresenceOffline, liveactivity.FreshnessFresh},
		{"online + no task -> idle", rt("online", fresh), nil, nil, liveactivity.PresenceIdle, liveactivity.FreshnessFresh},
		{"queued task -> queued", rt("online", fresh), task("queued", time.Time{}), nil, liveactivity.PresenceQueued, liveactivity.FreshnessFresh},
		{"running + fresh heartbeat -> working", rt("online", fresh), task("running", now.Add(-time.Minute)), nil, liveactivity.PresenceWorking, liveactivity.FreshnessFresh},
		{"running + stale heartbeat -> unknown", rt("online", stale), task("running", now.Add(-time.Minute)), nil, liveactivity.PresenceUnknown, liveactivity.FreshnessStale},
		{"waiting_local_directory -> waiting", rt("online", fresh), task("waiting_local_directory", now.Add(-time.Minute)), nil, liveactivity.PresenceWaiting, liveactivity.FreshnessFresh},
		{"missing runtime -> offline + missing freshness", nil, nil, nil, liveactivity.PresenceOffline, liveactivity.FreshnessMissing},
		{"recent completed -> recently_completed", rt("online", fresh), nil, task("completed", now.Add(-time.Minute)), liveactivity.PresenceRecentlyCompleted, liveactivity.FreshnessFresh},
		{"old completed -> idle", rt("online", fresh), nil, task("completed", now.Add(-time.Hour)), liveactivity.PresenceIdle, liveactivity.FreshnessFresh},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssembleAgent(agent(), tt.rt, tt.active, tt.lastOutcome, nil, nil, now, 0)
			if got.PresenceState != tt.want {
				t.Fatalf("presence = %q, want %q", got.PresenceState, tt.want)
			}
			if got.FreshnessState != tt.wantFresh {
				t.Fatalf("freshness = %q, want %q", got.FreshnessState, tt.wantFresh)
			}
		})
	}
}

func TestAssembleAgent_IdentityAndRefs(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, nil, nil, nil, now, 0)
	if got.DisplayName != "Emory" {
		t.Fatalf("display_name = %q", got.DisplayName)
	}
	if got.ModelName != "deepseek-v4" {
		t.Fatalf("model_name = %q", got.ModelName)
	}
	if got.SchemaVersion != liveactivity.SchemaVersionV1 {
		t.Fatalf("schema_version = %q", got.SchemaVersion)
	}
	if len(got.SourceRefs) < 2 {
		t.Fatalf("source_refs too short: %v", got.SourceRefs)
	}
}

func TestAssembleAgent_PopulatesRecentActivity(t *testing.T) {
	now := time.Now().UTC()
	acts := []db.ActivityLog{
		{
			ID:        tu,
			Action:    "task_completed",
			CreatedAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true},
		},
	}
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, nil, nil, acts, now, 0)
	if len(got.RecentEvents) != 1 {
		t.Fatalf("expected 1 recent event, got %d", len(got.RecentEvents))
	}
	if got.ActivityKind != "activity.task_completed" {
		t.Fatalf("activity_kind = %q", got.ActivityKind)
	}
	if got.ActivitySummary != "任务完成" {
		t.Fatalf("activity_summary = %q", got.ActivitySummary)
	}
}

// chainFixture builds a fully hydrated chain for the chain-overlay tests.
func chainFixture() *ExecutionChain {
	return &ExecutionChain{
		TaskID:                 uuidStr(tu),
		IssueID:                uuidStr(tu),
		IssueIdentifier:        "HIV-797",
		IssueTitle:             "[DEV] Work Wall complete execution-chain projection",
		ProjectID:              uuidStr(tu),
		ProjectTitle:           "HiveCrew",
		RuntimeProfileID:       uuidStr(tu),
		RuntimeProfileName:     "glm-5.3-profile",
		RunID:                  uuidStr(tu),
		ExecutionReceiptRef:    "receipt://" + uuidStr(tu),
		ExecutionReceiptStatus: "completed",
	}
}

func TestAssembleAgent_ChainOverlayHydratesIdentifiers(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), task("running", now.Add(-time.Minute)), nil, chainFixture(), nil, now, 0)

	if got.IssueIdentifier != "HIV-797" {
		t.Fatalf("issue_identifier = %q", got.IssueIdentifier)
	}
	if got.IssueTitle != "[DEV] Work Wall complete execution-chain projection" {
		t.Fatalf("issue_title = %q", got.IssueTitle)
	}
	if got.ProjectID != uuidStr(tu) || got.ProjectTitle != "HiveCrew" {
		t.Fatalf("project chain = %q / %q", got.ProjectID, got.ProjectTitle)
	}
	if got.RuntimeProfileID != uuidStr(tu) || got.RuntimeProfileName != "glm-5.3-profile" {
		t.Fatalf("runtime profile chain = %q / %q", got.RuntimeProfileID, got.RuntimeProfileName)
	}
	if got.RunID != uuidStr(tu) {
		t.Fatalf("run_id = %q", got.RunID)
	}
	if got.ExecutionReceiptRef != "receipt://"+uuidStr(tu) || got.ExecutionReceiptStatus != "completed" {
		t.Fatalf("receipt = %q / %q", got.ExecutionReceiptRef, got.ExecutionReceiptStatus)
	}

	refs := map[string]bool{}
	for _, r := range got.SourceRefs {
		refs[r] = true
	}
	for _, want := range []string{"issue://" + uuidStr(tu), "project://" + uuidStr(tu), "profile://" + uuidStr(tu), "receipt://" + uuidStr(tu)} {
		if !refs[want] {
			t.Fatalf("source_refs missing %q: %v", want, got.SourceRefs)
		}
	}
}

func TestAssembleAgent_NilChainLeavesIdentifiersAbsent(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), task("running", now.Add(-time.Minute)), nil, nil, nil, now, 0)
	if got.IssueIdentifier != "" || got.IssueTitle != "" || got.ProjectID != "" || got.ProjectTitle != "" {
		t.Fatalf("issue/project identifiers must stay absent without chain evidence: %+v", got)
	}
	if got.RuntimeProfileID != "" || got.RuntimeProfileName != "" || got.RunID != "" {
		t.Fatalf("profile/run identifiers must stay absent without chain evidence: %+v", got)
	}
	if got.ExecutionReceiptRef != "" || got.ExecutionReceiptStatus != "" {
		t.Fatalf("receipt must stay absent without chain evidence: %+v", got)
	}
	if got.PresenceState != liveactivity.PresenceWorking {
		t.Fatalf("presence derivation must be untouched, got %q", got.PresenceState)
	}
}

func TestAssembleAgent_RecentTerminalTaskTracesTaskAndChain(t *testing.T) {
	now := time.Now().UTC()
	outcome := task("completed", now.Add(-time.Minute))
	chain := chainFixture()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, outcome, chain, nil, now, 0)

	if got.PresenceState != liveactivity.PresenceRecentlyCompleted {
		t.Fatalf("presence = %q, want recently_completed", got.PresenceState)
	}
	if got.TaskID != chain.TaskID {
		t.Fatalf("task_id = %q, want the recent terminal task %q", got.TaskID, chain.TaskID)
	}
	if got.IssueIdentifier != "HIV-797" {
		t.Fatalf("issue_identifier = %q for the recent terminal task", got.IssueIdentifier)
	}
	if got.ExecutionReceiptStatus != "completed" {
		t.Fatalf("receipt status = %q", got.ExecutionReceiptStatus)
	}
}

func TestAssembleAgent_EmptyChainEvidenceStaysEmpty(t *testing.T) {
	now := time.Now().UTC()
	chain := &ExecutionChain{TaskID: uuidStr(tu)} // task exists, every link missing
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), task("running", now.Add(-time.Minute)), nil, chain, nil, now, 0)
	if got.IssueIdentifier != "" || got.ProjectTitle != "" || got.RuntimeProfileName != "" || got.RunID != "" || got.ExecutionReceiptRef != "" {
		t.Fatalf("missing evidence must render empty, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Employee authority overlay (HIV-854)
//
// The Work Wall reuses the existing CompanyOps Employee directory read seam
// (EmployeeDirectory, bound to *service.CompanyOpsDirectoryService by the
// handler) and overlays formal Employee identity only from complete, exact
// and unambiguous evidence. Every other state keeps the Agent card visible
// with the formal fields cleared and never looking `fresh`.
// ---------------------------------------------------------------------------

const (
	overlayAgentUUID = "11111111-1111-1111-1111-111111111111"
	otherAgentUUID   = "22222222-2222-2222-2222-222222222222"
)

func verifiedIdentityFixture() *EmployeeIdentity {
	return &EmployeeIdentity{
		EmployeeID:       "DE-KAI-01",
		DisplayName:      "Kai｜后端与全栈工程师",
		DepartmentID:     "DEPT-ENG",
		DepartmentName:   "工程部",
		PositionID:       "POS-BE",
		PositionTitle:    "后端与全栈工程师",
		BaseMachineTitle: "HiveCosm Mac mini",
	}
}

func TestAssembleAgentCard_VerifiedAuthorityOverlaysFormalIdentity(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgentCard(
		agent(),
		rt("online", now.Add(-time.Second)),
		task("running", now.Add(-time.Minute)),
		nil,
		nil,
		&EmployeeAuthority{State: EmployeeAuthorityVerified, Identity: verifiedIdentityFixture()},
		nil,
		now, 0,
	)

	if got.EmployeeID != "DE-KAI-01" {
		t.Fatalf("employee_id = %q, want the formal DE identifier", got.EmployeeID)
	}
	if got.AgentID != uuidStr(tu) {
		t.Fatalf("agent_id = %q, want the agent UUID", got.AgentID)
	}
	if got.DisplayName != "Kai｜后端与全栈工程师" {
		t.Fatalf("display_name = %q, want the formal employee name", got.DisplayName)
	}
	if got.DepartmentID != "DEPT-ENG" || got.DepartmentName != "工程部" {
		t.Fatalf("department = %q / %q", got.DepartmentID, got.DepartmentName)
	}
	if got.PositionName != "后端与全栈工程师" {
		t.Fatalf("position_name = %q", got.PositionName)
	}
	if got.BaseName != "HiveCosm Mac mini" {
		t.Fatalf("base_name = %q, want the verified machine title", got.BaseName)
	}
	if got.BaseID != "" {
		t.Fatalf("base_id = %q, want empty — the join carries no Base registry ID", got.BaseID)
	}
	refs := map[string]bool{}
	for _, ref := range got.SourceRefs {
		refs[ref] = true
	}
	if !refs["employee://DE-KAI-01"] {
		t.Fatalf("source_refs missing employee:// ref: %v", got.SourceRefs)
	}
	// Verified authority with a fresh runtime may look fresh.
	if got.FreshnessState != liveactivity.FreshnessFresh {
		t.Fatalf("freshness = %q, want fresh for verified authority + fresh runtime", got.FreshnessState)
	}
	if got.PresenceState != liveactivity.PresenceWorking {
		t.Fatalf("presence = %q, want working (overlay must not touch derivation)", got.PresenceState)
	}
}

func TestAssembleAgentCard_VerifiedOverlayPreservesExecutionChain(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgentCard(
		agent(),
		rt("online", now.Add(-time.Second)),
		task("running", now.Add(-time.Minute)),
		nil,
		chainFixture(),
		&EmployeeAuthority{State: EmployeeAuthorityVerified, Identity: verifiedIdentityFixture()},
		nil,
		now, 0,
	)

	// The authority overlay must never override Agent-owned or chain-owned
	// identifiers: the non-authority execution chain stays fully visible.
	if got.IssueIdentifier != "HIV-797" || got.IssueTitle == "" {
		t.Fatalf("issue chain lost under overlay: %+v", got)
	}
	if got.ProjectID != uuidStr(tu) || got.ProjectTitle != "HiveCrew" {
		t.Fatalf("project chain lost under overlay: %+v", got)
	}
	if got.TaskID != uuidStr(tu) || got.RunID != uuidStr(tu) {
		t.Fatalf("task/run identifiers lost under overlay: %+v", got)
	}
	if got.RuntimeProfileID != uuidStr(tu) || got.RuntimeProfileName != "glm-5.3-profile" {
		t.Fatalf("runtime profile lost under overlay: %+v", got)
	}
	if got.ExecutionReceiptRef != "receipt://"+uuidStr(tu) || got.ExecutionReceiptStatus != "completed" {
		t.Fatalf("receipt lost under overlay: %+v", got)
	}
	if got.RuntimeCarrier != "prime" || got.ModelName != "deepseek-v4" {
		t.Fatalf("runtime carrier/model lost under overlay: %+v", got)
	}
}

func TestAssembleAgentCard_AgentOnlyCardClearsFormalEmployeeFields(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgentCard(agent(), rt("online", now.Add(-time.Second)), nil, nil, nil, nil, nil, now, 0)

	// No authority evidence: the card stays a legitimate Agent-only
	// projection. Formal fields are cleared — employee_id no longer mirrors
	// the agent UUID — and a healthy authority keeps runtime freshness.
	if got.EmployeeID != "" {
		t.Fatalf("employee_id = %q, want empty for an agent-only card", got.EmployeeID)
	}
	if got.AgentID != uuidStr(tu) {
		t.Fatalf("agent_id = %q", got.AgentID)
	}
	if got.DisplayName != "Emory" {
		t.Fatalf("display_name = %q, want the agent name fallback", got.DisplayName)
	}
	if got.DepartmentID != "" || got.DepartmentName != "" || got.PositionName != "" {
		t.Fatalf("formal organization fields must stay cleared: %+v", got)
	}
	if got.BaseID != "" || got.BaseName != "" {
		t.Fatalf("base fields must stay cleared without verified authority: %+v", got)
	}
	for _, ref := range got.SourceRefs {
		if strings.HasPrefix(ref, "employee://") {
			t.Fatalf("agent-only card must not carry an employee source ref: %v", got.SourceRefs)
		}
	}
	if got.FreshnessState != liveactivity.FreshnessFresh {
		t.Fatalf("freshness = %q, want fresh: unmatched agent with healthy authority keeps runtime freshness", got.FreshnessState)
	}
}

func TestAssembleAgentCard_AuthorityGapCannotLookFresh(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string
		rt   *db.AgentRuntime
		want liveactivity.FreshnessState
	}{
		{"fresh runtime degrades to the generic gap state", rt("online", now.Add(-5*time.Second)), liveactivity.FreshnessConflict},
		{"stale runtime stays stale (already non-fresh)", rt("online", now.Add(-10*time.Minute)), liveactivity.FreshnessStale},
		{"missing runtime stays missing (already non-fresh)", nil, liveactivity.FreshnessMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gap := &EmployeeAuthority{State: EmployeeAuthorityGap}
			got := AssembleAgentCard(agent(), tt.rt, nil, nil, nil, gap, nil, now, 0)
			if got.FreshnessState != tt.want {
				t.Fatalf("freshness = %q, want %q", got.FreshnessState, tt.want)
			}
			if got.FreshnessState == liveactivity.FreshnessFresh {
				t.Fatalf("authority-unavailable fallback must never look fresh")
			}
			// Formal fields stay cleared on the gap card.
			if got.EmployeeID != "" || got.DepartmentName != "" || got.PositionName != "" || got.BaseName != "" {
				t.Fatalf("gap card must keep formal Employee fields cleared: %+v", got)
			}
		})
	}
}

func TestAssembleAgentCard_GapPreservesChainEventsAndPresence(t *testing.T) {
	now := time.Now().UTC()
	acts := []db.ActivityLog{
		{ID: tu, Action: "task_completed", CreatedAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}},
	}
	got := AssembleAgentCard(
		agent(),
		rt("online", now.Add(-time.Second)),
		task("running", now.Add(-time.Minute)),
		nil,
		chainFixture(),
		&EmployeeAuthority{State: EmployeeAuthorityGap},
		acts,
		now, 0,
	)

	// An authority gap clears only the formal Employee fields: the Agent
	// card, the non-authority execution chain and the sanitized recent-event
	// projection all remain visible.
	if got.PresenceState != liveactivity.PresenceWorking {
		t.Fatalf("presence = %q, want working", got.PresenceState)
	}
	if got.IssueIdentifier != "HIV-797" || got.ProjectID == "" || got.ExecutionReceiptRef == "" {
		t.Fatalf("execution chain must survive an authority gap: %+v", got)
	}
	if len(got.RecentEvents) != 1 || got.ActivitySummary != "任务完成" {
		t.Fatalf("sanitized recent events must survive an authority gap: %+v", got)
	}
	if got.BlockedReason != "" || got.NextAction != "" {
		t.Fatalf("gap must not fabricate blocked_reason/next_action text: %+v", got)
	}
}

func TestAssembleAgent_FrozenEntryPointHasNoEmployeeMirror(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, nil, nil, nil, now, 0)
	if got.EmployeeID != "" {
		t.Fatalf("employee_id = %q, want empty: the v0 agent_id mirror is removed", got.EmployeeID)
	}
	if got.AgentID != uuidStr(tu) {
		t.Fatalf("agent_id = %q", got.AgentID)
	}
}

// ---------------------------------------------------------------------------
// CompanyOps directory seam resolution (the adapter the Work Wall handlers
// bind through workwall.NewService). All cases run without a database.
// ---------------------------------------------------------------------------

// fakeEmployeeDirectory stubs the EmployeeDirectory seam: a fixed queue of
// employee pages (or a generator), plus one workforce-join result.
type fakeEmployeeDirectory struct {
	joinRows []service.WorkforceBaseRuntimeRow
	joinErr  error

	pages        []*service.EmployeesResult
	pagesFn      func(offset int) *service.EmployeesResult
	employeesErr error

	joinCalls         int
	employeeCalls     []int
	employeeCallLimit []int
}

func (f *fakeEmployeeDirectory) GetEmployees(_ context.Context, _ pgtype.UUID, q string, availabilityFilter string, limit int, offset int) (*service.EmployeesResult, error) {
	f.employeeCalls = append(f.employeeCalls, offset)
	f.employeeCallLimit = append(f.employeeCallLimit, limit)
	if q != "" || availabilityFilter != "" {
		return nil, errors.New("work wall must page the unfiltered roster")
	}
	if f.employeesErr != nil {
		return nil, f.employeesErr
	}
	if f.pagesFn != nil {
		return f.pagesFn(offset), nil
	}
	if len(f.pages) == 0 {
		return &service.EmployeesResult{Total: 0, Limit: limit, Offset: offset}, nil
	}
	page := *f.pages[0]
	f.pages = f.pages[1:]
	page.Limit = limit
	page.Offset = offset
	return &page, nil
}

func (f *fakeEmployeeDirectory) GetWorkforceBaseRuntimeJoin(_ context.Context, _ pgtype.UUID) (companyopsapi.PublicAuthorityRef, []service.WorkforceBaseRuntimeRow, error) {
	f.joinCalls++
	if f.joinErr != nil {
		return companyopsapi.PublicAuthorityRef{}, nil, f.joinErr
	}
	return companyopsapi.PublicAuthorityRef{}, f.joinRows, nil
}

func summaryFor(employeeID, agentID, name string) companyopsapi.PublicEmployeeSummary {
	return companyopsapi.PublicEmployeeSummary{
		EmployeeID:            employeeID,
		WorkforceAgentID:      "KT-TEST",
		DisplayName:           name,
		EmployeeContractState: "existing_digital_employee_contract",
		DepartmentID:          "DEPT-ENG",
		DepartmentName:        "工程部",
		PositionID:            "POS-BE",
		PositionTitle:         "后端与全栈工程师",
		BindingState:          "unique_active_candidate",
		Availability:          companyopsapi.AvailabilityAvailable,
		HiveCrewAgentID:       agentID,
	}
}

func joinRowFor(employeeID, agentID, base string) service.WorkforceBaseRuntimeRow {
	return service.WorkforceBaseRuntimeRow{
		EmployeeID:       employeeID,
		WorkforceAgentID: "KT-TEST",
		HiveCrewAgentID:  agentID,
		RuntimeID:        overlayAgentUUID,
		BaseMachineTitle: base,
		AgentStatus:      "idle",
		RuntimeStatus:    "online",
	}
}

func employeesPage(total int, items ...companyopsapi.PublicEmployeeSummary) *service.EmployeesResult {
	return &service.EmployeesResult{Items: items, Total: total}
}

func resolveWith(dir EmployeeDirectory) employeeAuthorityIndex {
	svc := &Service{Directory: dir}
	return svc.resolveEmployeeAuthority(context.Background(), tu)
}

func TestResolveEmployeeAuthority_VerifiedExactMatch(t *testing.T) {
	dir := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
		pages:    []*service.EmployeesResult{employeesPage(1, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师"))},
	}
	index := resolveWith(dir)
	if index.unavailable {
		t.Fatalf("healthy directory must not be classified unavailable")
	}
	authority := index.forAgent(overlayAgentUUID)
	if authority == nil || authority.State != EmployeeAuthorityVerified || authority.Identity == nil {
		t.Fatalf("authority = %+v, want verified identity", authority)
	}
	ident := authority.Identity
	if ident.EmployeeID != "DE-KAI-01" || ident.DisplayName != "Kai｜后端与全栈工程师" ||
		ident.DepartmentName != "工程部" || ident.PositionTitle != "后端与全栈工程师" ||
		ident.BaseMachineTitle != "HiveCosm Mac mini" {
		t.Fatalf("identity = %+v", ident)
	}
	// An agent the authority does not name is a legitimate agent-only card.
	if other := index.forAgent(otherAgentUUID); other != nil {
		t.Fatalf("unmatched agent must resolve to nil (agent-only), got %+v", other)
	}
}

func TestResolveEmployeeAuthority_EmployeeIDConflictFailsClosed(t *testing.T) {
	// The join binds the agent to DE-KAI-01 while the summary binds the same
	// agent to DE-OTHER-02: exact-match is impossible, fail closed.
	dir := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
		pages:    []*service.EmployeesResult{employeesPage(1, summaryFor("DE-OTHER-02", overlayAgentUUID, "别人"))},
	}
	if authority := resolveWith(dir).forAgent(overlayAgentUUID); authority == nil || authority.State != EmployeeAuthorityGap {
		t.Fatalf("authority = %+v, want gap for join/summary employee conflict", authority)
	}
}

func TestResolveEmployeeAuthority_OneSidedEvidenceIsIncomplete(t *testing.T) {
	joinOnly := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
		pages:    []*service.EmployeesResult{employeesPage(0)},
	}
	if authority := resolveWith(joinOnly).forAgent(overlayAgentUUID); authority == nil || authority.State != EmployeeAuthorityGap {
		t.Fatalf("join-only evidence = %+v, want gap", authority)
	}

	summaryOnly := &fakeEmployeeDirectory{
		pages: []*service.EmployeesResult{employeesPage(1, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师"))},
	}
	if authority := resolveWith(summaryOnly).forAgent(overlayAgentUUID); authority == nil || authority.State != EmployeeAuthorityGap {
		t.Fatalf("summary-only evidence = %+v, want gap", authority)
	}
}

func TestResolveEmployeeAuthority_MalformedRowsFailClosed(t *testing.T) {
	validSummary := summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师")
	cases := []struct {
		name    string
		join    service.WorkforceBaseRuntimeRow
		summary companyopsapi.PublicEmployeeSummary
	}{
		{"blank employee id on join", joinRowFor("", overlayAgentUUID, "HiveCosm Mac mini"), validSummary},
		{"padded agent id on join", joinRowFor("DE-KAI-01", " "+overlayAgentUUID, "HiveCosm Mac mini"), validSummary},
		{"padded agent id on summary", joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini"), summaryFor("DE-KAI-01", overlayAgentUUID+" ", "Kai")},
		{"blank base title", joinRowFor("DE-KAI-01", overlayAgentUUID, "  "), validSummary},
		{"blank department name", joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini"), func() companyopsapi.PublicEmployeeSummary {
			s := validSummary
			s.DepartmentName = " "
			return s
		}()},
		{"blank position title", joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini"), func() companyopsapi.PublicEmployeeSummary {
			s := validSummary
			s.PositionTitle = ""
			return s
		}()},
		{"blank display name", joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini"), func() companyopsapi.PublicEmployeeSummary {
			s := validSummary
			s.DisplayName = " "
			return s
		}()},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := &fakeEmployeeDirectory{
				joinRows: []service.WorkforceBaseRuntimeRow{tt.join},
				pages:    []*service.EmployeesResult{employeesPage(1, tt.summary)},
			}
			if authority := resolveWith(dir).forAgent(overlayAgentUUID); authority == nil || authority.State != EmployeeAuthorityGap {
				t.Fatalf("authority = %+v, want gap for malformed evidence", authority)
			}
		})
	}
}

func TestResolveEmployeeAuthority_ValidPlusMalformedFailsClosed(t *testing.T) {
	// A valid join row plus a malformed join row naming the same agent must
	// fail closed: the malformed row is evidence, it is never filtered out
	// before the ambiguity evaluation.
	dir := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{
			joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini"),
			joinRowFor("", overlayAgentUUID, ""),
		},
		pages: []*service.EmployeesResult{employeesPage(1, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师"))},
	}
	if authority := resolveWith(dir).forAgent(overlayAgentUUID); authority == nil || authority.State != EmployeeAuthorityGap {
		t.Fatalf("authority = %+v, want gap: valid+malformed must fail closed", authority)
	}

	// Duplicate employee summaries naming the same agent (e.g. the same
	// employee served on two pages) are equally ambiguous.
	dup := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
		pages: []*service.EmployeesResult{
			employeesPage(2, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师")),
			employeesPage(2, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师")),
		},
	}
	if authority := resolveWith(dup).forAgent(overlayAgentUUID); authority == nil || authority.State != EmployeeAuthorityGap {
		t.Fatalf("authority = %+v, want gap for duplicate employee rows", authority)
	}
}

func TestResolveEmployeeAuthority_AuthorityUnavailableFailsEveryCardClosed(t *testing.T) {
	cases := []struct {
		name string
		dir  *fakeEmployeeDirectory
	}{
		{"workforce join transport failure", &fakeEmployeeDirectory{
			joinErr: fmt.Errorf("%w: HTTP 503", companyopsapi.ErrAdapterSourceGap),
			pages:   []*service.EmployeesResult{employeesPage(1, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai"))},
		}},
		{"workforce join malformed authority", &fakeEmployeeDirectory{
			joinErr: fmt.Errorf("%w: organization: employees array is required and nonempty", companyopsapi.ErrAdapterMalformed),
			pages:   []*service.EmployeesResult{employeesPage(1, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai"))},
		}},
		{"employee page malformed row fails whole read", &fakeEmployeeDirectory{
			joinRows:     []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
			employeesErr: fmt.Errorf("%w: employee summary is malformed", companyopsapi.ErrAdapterMalformed),
		}},
		{"explicit empty authoritative workforce", &fakeEmployeeDirectory{
			joinRows:     []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
			employeesErr: service.ErrCompanyOpsEmptyAuthoritativeWorkforce,
		}},
		{"nil page result is a contract violation", &fakeEmployeeDirectory{
			joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
			pagesFn:  func(int) *service.EmployeesResult { return nil },
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			index := resolveWith(tt.dir)
			if !index.unavailable {
				t.Fatalf("index must be classified unavailable")
			}
			// Every card degrades to the gap state; none is dropped.
			if authority := index.forAgent(overlayAgentUUID); authority == nil || authority.State != EmployeeAuthorityGap {
				t.Fatalf("authority = %+v, want gap for every card", authority)
			}
			if authority := index.forAgent(otherAgentUUID); authority == nil || authority.State != EmployeeAuthorityGap {
				t.Fatalf("authority = %+v, want gap for every card", authority)
			}
		})
	}

	// A nil seam (directory adapter not configured at startup) behaves the
	// same way.
	svc := &Service{}
	if index := svc.resolveEmployeeAuthority(context.Background(), tu); !index.unavailable {
		t.Fatalf("nil seam must classify the whole read unavailable")
	}
}

func TestResolveEmployeeAuthority_PagesUntilReportedTotal(t *testing.T) {
	// 1002 employees across three pages: the seam must page by offset with
	// the contract limit until the reported Total is covered, and an employee
	// bound on the LAST page still resolves verified.
	big := make([]companyopsapi.PublicEmployeeSummary, 0, 1002)
	for i := 0; i < 1001; i++ {
		big = append(big, summaryFor(fmt.Sprintf("DE-FILLER-%04d", i), "", "填充"))
	}
	big = append(big, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师"))

	dir := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
		pages: []*service.EmployeesResult{
			employeesPage(1002, big[:500]...),
			employeesPage(1002, big[500:1000]...),
			employeesPage(1002, big[1000:]...),
		},
	}
	index := resolveWith(dir)

	wantOffsets := []int{0, 500, 1000}
	if !reflect.DeepEqual(dir.employeeCalls, wantOffsets) {
		t.Fatalf("employee page offsets = %v, want %v", dir.employeeCalls, wantOffsets)
	}
	for _, limit := range dir.employeeCallLimit {
		if limit != directoryPageSize {
			t.Fatalf("page limit = %d, want the contract bound %d", limit, directoryPageSize)
		}
	}
	if dir.joinCalls != 1 {
		t.Fatalf("join calls = %d, want exactly one per snapshot", dir.joinCalls)
	}
	if authority := index.forAgent(overlayAgentUUID); authority == nil || authority.State != EmployeeAuthorityVerified {
		t.Fatalf("authority = %+v, want verified for the last-page employee", authority)
	}
}

func TestResolveEmployeeAuthority_TruncationFailsClosed(t *testing.T) {
	// Total reports 10 but the pages stop after 4: the roster read is
	// truncated, so the whole authority resolution fails closed — no partial
	// overlay from the rows that did arrive.
	truncated := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
		pages: []*service.EmployeesResult{
			employeesPage(10, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师")),
			employeesPage(10, summaryFor("DE-OTHER-02", otherAgentUUID, "别人")),
			employeesPage(10),
		},
	}
	if index := resolveWith(truncated); !index.unavailable {
		t.Fatalf("truncated roster must classify the whole read unavailable")
	}

	// A page larger than the requested contract limit is inconsistent.
	oversized := make([]companyopsapi.PublicEmployeeSummary, directoryPageSize+1)
	for i := range oversized {
		oversized[i] = summaryFor(fmt.Sprintf("DE-X-%04d", i), "", "x")
	}
	oversizedDir := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
		pages:    []*service.EmployeesResult{employeesPage(len(oversized), oversized...)},
	}
	if index := resolveWith(oversizedDir); !index.unavailable {
		t.Fatalf("oversized page must classify the whole read unavailable")
	}

	// Items served while the authority reports Total=0 is inconsistent.
	inconsistent := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini")},
		pages:    []*service.EmployeesResult{employeesPage(0, summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师"))},
	}
	if index := resolveWith(inconsistent); !index.unavailable {
		t.Fatalf("non-zero items with zero Total must classify the whole read unavailable")
	}
}

func TestResolveEmployeeAuthority_NoPerAgentDirectoryReads(t *testing.T) {
	// Three named agents must still cost exactly one join read plus the
	// employee pages: the resolution is per-snapshot, never per-agent (N+1
	// guard).
	dir := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{
			joinRowFor("DE-KAI-01", overlayAgentUUID, "HiveCosm Mac mini"),
			joinRowFor("DE-OTHER-02", otherAgentUUID, "HiveCosm DGX"),
			joinRowFor("DE-THIRD-03", "33333333-3333-3333-3333-333333333333", "HiveCosm MacBook Pro"),
		},
		pages: []*service.EmployeesResult{employeesPage(3,
			summaryFor("DE-KAI-01", overlayAgentUUID, "Kai｜后端与全栈工程师"),
			summaryFor("DE-OTHER-02", otherAgentUUID, "Raven｜视觉工程师"),
			summaryFor("DE-THIRD-03", "33333333-3333-3333-3333-333333333333", "Gauss｜审核工程师"),
		)},
	}
	index := resolveWith(dir)
	if dir.joinCalls != 1 || len(dir.employeeCalls) != 1 {
		t.Fatalf("directory reads = %d join + %v pages, want 1 + 1 for any agent count", dir.joinCalls, dir.employeeCalls)
	}
	for _, aid := range []string{overlayAgentUUID, otherAgentUUID, "33333333-3333-3333-3333-333333333333"} {
		if authority := index.forAgent(aid); authority == nil || authority.State != EmployeeAuthorityVerified {
			t.Fatalf("authority for %s = %+v, want verified", aid, authority)
		}
	}
}

func TestResolveEmployeeAuthority_TerminalHintsCannotEstablishIdentity(t *testing.T) {
	// Terminal Live stays a separate observation surface: process names,
	// commands and workforce identifiers that merely LOOK like terminal
	// output can never establish Employee identity. Only an exact canonical
	// HiveCrewAgentID pairing on both directory reads binds identity.
	dir := &fakeEmployeeDirectory{
		joinRows: []service.WorkforceBaseRuntimeRow{{
			// WorkforceAgentID mimics a terminal process hint; the agent
			// binding column stays empty, so this row names no agent.
			EmployeeID:       "DE-KAI-01",
			WorkforceAgentID: "node server.js",
			BaseMachineTitle: "HiveCosm Mac mini",
		}},
		pages: []*service.EmployeesResult{employeesPage(1, func() companyopsapi.PublicEmployeeSummary {
			s := summaryFor("DE-KAI-01", "", "Kai｜后端与全栈工程师")
			s.WorkforceAgentID = "node server.js"
			return s
		}())},
	}
	index := resolveWith(dir)
	if index.unavailable {
		t.Fatalf("directory read is healthy")
	}
	if authority := index.forAgent(overlayAgentUUID); authority != nil {
		t.Fatalf("terminal-shaped hints must not create identity evidence, got %+v", authority)
	}
}

func TestNewServiceDirectoryBinding(t *testing.T) {
	// The production constructor is nil-tolerant: an unconfigured directory
	// leaves the seam nil (authority gap), a configured one binds the exact
	// CompanyOps service without a wrapper.
	if svc := NewService(nil, nil); svc.Directory != nil {
		t.Fatalf("nil directory must leave the seam nil")
	}
	real := service.NewCompanyOpsDirectoryService(nil, nil)
	if svc := NewService(nil, real); svc.Directory == nil {
		t.Fatalf("configured directory must bind the seam")
	}
	var _ EmployeeDirectory = real
}

// ---------------------------------------------------------------------------
// Runtime carrier vs LLM provider semantic separation (HIV-911)
//
// agent_runtime.provider is the Multica runtime protocol/carrier (e.g.
// "prime", "volcengine"), NOT the LLM provider. The assembler must map it
// to RuntimeCarrier and never present it as the LLM provider.
// ---------------------------------------------------------------------------

func TestAssembleAgent_RuntimeCarrierIsNotLLMProvider(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgent(agent(), rt("online", now.Add(-time.Second)), nil, nil, nil, nil, now, 0)

	if got.RuntimeCarrier != "prime" {
		t.Fatalf("runtime_carrier = %q, want %q (the agent_runtime.provider value)", got.RuntimeCarrier, "prime")
	}
	if got.LLMProvider != "" {
		t.Fatalf("llm_provider = %q, want empty: no workspace-scoped authoritative LLM provider source exists", got.LLMProvider)
	}
	if got.RuntimeCarrier == got.LLMProvider && got.RuntimeCarrier != "" {
		t.Fatalf("runtime_carrier and llm_provider must never carry the same non-empty value — they are independent semantics")
	}
}

func TestAssembleAgent_RuntimeCarrierPreservedUnderAuthorityOverlay(t *testing.T) {
	now := time.Now().UTC()
	got := AssembleAgentCard(
		agent(),
		rt("online", now.Add(-time.Second)),
		nil, nil, nil,
		&EmployeeAuthority{State: EmployeeAuthorityVerified, Identity: verifiedIdentityFixture()},
		nil, now, 0,
	)

	if got.RuntimeCarrier != "prime" {
		t.Fatalf("runtime_carrier = %q, want %q after authority overlay", got.RuntimeCarrier, "prime")
	}
	if got.LLMProvider != "" {
		t.Fatalf("llm_provider = %q, want empty after authority overlay", got.LLMProvider)
	}
}

// ---------------------------------------------------------------------------
// Execution-runtime projection (HIV-940)
// ---------------------------------------------------------------------------

func rebindChainFixture() *ExecutionChain {
	return &ExecutionChain{
		TaskID:                  uuidStr(tu),
		IssueID:                 uuidStr(tu),
		IssueIdentifier:         "HIV-940",
		IssueTitle:              "Task-runtime lineage",
		RuntimeProfileID:        "profile-task-rt",
		RuntimeProfileName:      "Codex 执行档案",
		ExecutionRuntimeID:      "rt-task-uuid",
		ExecutionRuntimeCarrier: "codex",
		ExecutionProfileID:      "profile-task-rt",
		ExecutionProfileName:    "Codex 执行档案",
	}
}

func TestAssembleAgentCard_RebindShowsCurrentBExecutionA(t *testing.T) {
	now := time.Now().UTC()
	// Agent's current runtime is "prime" (runtime_id = tu).
	// Task's execution runtime is "codex" (from the chain).
	got := AssembleAgentCard(
		agent(),
		rt("online", now.Add(-time.Second)),
		task("running", now.Add(-time.Minute)),
		nil,
		rebindChainFixture(),
		nil,
		nil,
		now, 0,
	)

	// Current binding: the agent's runtime.
	if got.RuntimeID != uuidStr(tu) {
		t.Fatalf("runtime_id = %q, want the agent's current runtime", got.RuntimeID)
	}
	if got.RuntimeCarrier != "prime" {
		t.Fatalf("runtime_carrier = %q, want the agent's current carrier", got.RuntimeCarrier)
	}
	// Execution chain: the task's original runtime.
	if got.ExecutionRuntimeID != "rt-task-uuid" {
		t.Fatalf("execution_runtime_id = %q, want the task's runtime", got.ExecutionRuntimeID)
	}
	if got.ExecutionRuntimeCarrier != "codex" {
		t.Fatalf("execution_runtime_carrier = %q, want the task's carrier", got.ExecutionRuntimeCarrier)
	}
	if got.ExecutionProfileID != "profile-task-rt" || got.ExecutionProfileName != "Codex 执行档案" {
		t.Fatalf("execution profile = %q / %q", got.ExecutionProfileID, got.ExecutionProfileName)
	}
	// Profile on the card follows the task runtime (HIV-940).
	if got.RuntimeProfileID != "profile-task-rt" {
		t.Fatalf("runtime_profile_id = %q, want the task runtime's profile", got.RuntimeProfileID)
	}
}

func TestAssembleAgentCard_NoExecutionRuntimeOmitsFields(t *testing.T) {
	now := time.Now().UTC()
	chain := &ExecutionChain{
		TaskID:          uuidStr(tu),
		IssueID:         uuidStr(tu),
		IssueIdentifier: "HIV-940",
		// No execution-runtime fields — task runtime was missing.
	}
	got := AssembleAgentCard(
		agent(),
		rt("online", now.Add(-time.Second)),
		task("running", now.Add(-time.Minute)),
		nil,
		chain,
		nil,
		nil,
		now, 0,
	)

	if got.ExecutionRuntimeID != "" || got.ExecutionRuntimeCarrier != "" {
		t.Fatalf("missing task runtime must omit execution fields, got %+v", got)
	}
	if got.ExecutionProfileID != "" || got.ExecutionProfileName != "" {
		t.Fatalf("missing task runtime must omit execution profile, got %+v", got)
	}
	// Current binding and the rest of the chain survive.
	if got.RuntimeCarrier != "prime" {
		t.Fatalf("current carrier must survive, got %q", got.RuntimeCarrier)
	}
	if got.IssueIdentifier != "HIV-940" {
		t.Fatalf("issue must survive, got %q", got.IssueIdentifier)
	}
}
