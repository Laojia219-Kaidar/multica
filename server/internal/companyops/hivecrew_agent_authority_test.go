package companyops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	authorityTestWorkspaceID = "11111111-1111-4111-8111-111111111111"
	authorityTestAgentID     = "22222222-2222-4222-8222-222222222222"
	authorityTestRuntimeID   = "33333333-3333-4333-8333-333333333333"
)

func TestBuildHiveCrewAgentAuthoritySnapshotExact(t *testing.T) {
	agent := authorityTestAgent()
	got, err := BuildHiveCrewAgentAuthoritySnapshot(agent, authorityTestWorkspaceID, authorityTestAgentID)
	if err != nil {
		t.Fatalf("BuildHiveCrewAgentAuthoritySnapshot() error = %v", err)
	}

	identity := hiveCrewAgentAuthorityDigest{
		ID: "22222222-2222-4222-8222-222222222222", WorkspaceID: "11111111-1111-4111-8111-111111111111",
		RuntimeID: "33333333-3333-4333-8333-333333333333", RuntimeMode: "local", Model: "gpt-5.6",
		MaxConcurrentTasks: 6, PermissionMode: "private", Kind: "worker", SystemKey: "hivecrew-auditor",
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(identityJSON)
	want := AuthoritySnapshot{
		Kind:          "Agent",
		SourceRef:     "/api/agents/22222222-2222-4222-8222-222222222222",
		Revision:      "sha256:" + hex.EncodeToString(sum[:]),
		ContentDigest: "sha256:" + hex.EncodeToString(sum[:]),
		Freshness:     "current",
		DisplayName:   "Audit Worker",
		Model:         "gpt-5.6",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
}

func TestBuildHiveCrewAgentAuthoritySnapshotFailsClosed(t *testing.T) {
	tests := []struct {
		name              string
		mutate            func(*db.Agent)
		expectedWorkspace string
		expectedAgent     string
	}{
		{
			name:              "wrong workspace",
			expectedWorkspace: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		},
		{
			name:          "wrong agent id",
			expectedAgent: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		},
		{
			name:              "malformed expected workspace",
			expectedWorkspace: "not-a-uuid",
		},
		{
			name:          "malformed expected agent",
			expectedAgent: "not-a-uuid",
		},
		{
			name:   "archived",
			mutate: func(agent *db.Agent) { agent.ArchivedAt = authorityTimestamp(time.Now()) },
		},
		{
			name:   "missing runtime",
			mutate: func(agent *db.Agent) { agent.RuntimeID = pgtype.UUID{} },
		},
		{
			name:   "nil runtime uuid",
			mutate: func(agent *db.Agent) { agent.RuntimeID = pgtype.UUID{Valid: true} },
		},
		{
			name:   "non executable status",
			mutate: func(agent *db.Agent) { agent.Status = "offline" },
		},
		{
			name:   "missing updated at",
			mutate: func(agent *db.Agent) { agent.UpdatedAt = pgtype.Timestamptz{} },
		},
		{
			name:   "zero updated at",
			mutate: func(agent *db.Agent) { agent.UpdatedAt = pgtype.Timestamptz{Valid: true} },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := authorityTestAgent()
			if test.mutate != nil {
				test.mutate(&agent)
			}
			workspaceID := test.expectedWorkspace
			if workspaceID == "" {
				workspaceID = authorityTestWorkspaceID
			}
			agentID := test.expectedAgent
			if agentID == "" {
				agentID = authorityTestAgentID
			}
			if _, err := BuildHiveCrewAgentAuthoritySnapshot(agent, workspaceID, agentID); err == nil {
				t.Fatal("BuildHiveCrewAgentAuthoritySnapshot() error = nil, want fail closed")
			}
		})
	}
}

func TestBuildHiveCrewAgentAuthoritySnapshotDigestTracksExecutionFields(t *testing.T) {
	base := authorityTestAgent()
	baseSnapshot := mustBuildAuthoritySnapshot(t, base, authorityTestWorkspaceID, authorityTestAgentID)
	tests := []struct {
		name              string
		mutate            func(*db.Agent)
		expectedWorkspace string
		expectedAgent     string
	}{
		{
			name:          "id",
			mutate:        func(agent *db.Agent) { agent.ID = util.MustParseUUID("44444444-4444-4444-8444-444444444444") },
			expectedAgent: "44444444-4444-4444-8444-444444444444",
		},
		{
			name:              "workspace id",
			mutate:            func(agent *db.Agent) { agent.WorkspaceID = util.MustParseUUID("55555555-5555-4555-8555-555555555555") },
			expectedWorkspace: "55555555-5555-4555-8555-555555555555",
		},
		{name: "runtime id", mutate: func(agent *db.Agent) { agent.RuntimeID = util.MustParseUUID("66666666-6666-4666-8666-666666666666") }},
		{name: "runtime mode", mutate: func(agent *db.Agent) { agent.RuntimeMode = "remote" }},
		{name: "model", mutate: func(agent *db.Agent) { agent.Model = authorityTextValue("gpt-5.7") }},
		// status and updated_at are execution liveness, not identity: the
		// digest intentionally no longer tracks them so a real run cannot
		// invalidate its own sealed assignment receipt.
		{name: "max concurrency", mutate: func(agent *db.Agent) { agent.MaxConcurrentTasks++ }},
		{name: "permission mode", mutate: func(agent *db.Agent) { agent.PermissionMode = "public_to" }},
		{name: "kind", mutate: func(agent *db.Agent) { agent.Kind = "manager" }},
		{name: "system key", mutate: func(agent *db.Agent) { agent.SystemKey = authorityTextValue("hivecrew-manager") }},

	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			workspaceID := test.expectedWorkspace
			if workspaceID == "" {
				workspaceID = authorityTestWorkspaceID
			}
			agentID := test.expectedAgent
			if agentID == "" {
				agentID = authorityTestAgentID
			}
			got := mustBuildAuthoritySnapshot(t, changed, workspaceID, agentID)
			if got.ContentDigest == baseSnapshot.ContentDigest {
				t.Fatalf("execution field change did not change digest %q", got.ContentDigest)
			}
		})
	}
}

func TestBuildHiveCrewAgentAuthoritySnapshotDisplayFieldsDoNotChangeIdentityDigest(t *testing.T) {
	base := authorityTestAgent()
	want := mustBuildAuthoritySnapshot(t, base, authorityTestWorkspaceID, authorityTestAgentID)

	changed := base
	changed.Name = "Renamed presentation hint"
	changed.AvatarUrl = authorityTextValue("https://example.invalid/new.png")
	changed.Description = "new presentation-only description"
	got := mustBuildAuthoritySnapshot(t, changed, authorityTestWorkspaceID, authorityTestAgentID)
	if got.ContentDigest != want.ContentDigest {
		t.Fatalf("display-only fields changed identity digest: got=%q want=%q", got.ContentDigest, want.ContentDigest)
	}
	if got.SourceRef != want.SourceRef || got.Revision != want.Revision {
		t.Fatalf("display-only fields changed identity: got=%+v want=%+v", got, want)
	}
}

func TestBuildHiveCrewAgentAuthoritySnapshotCustomEnvIsExcluded(t *testing.T) {
	base := authorityTestAgent()
	want := mustBuildAuthoritySnapshot(t, base, authorityTestWorkspaceID, authorityTestAgentID)

	const secret = "secret-value-must-never-leak"
	changed := base
	changed.CustomEnv = []byte(`{"API_TOKEN":"` + secret + `"}`)
	got := mustBuildAuthoritySnapshot(t, changed, authorityTestWorkspaceID, authorityTestAgentID)
	if got.ContentDigest != want.ContentDigest {
		t.Fatalf("CustomEnv changed digest: got=%q want=%q", got.ContentDigest, want.ContentDigest)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("snapshot leaked CustomEnv secret: %s", encoded)
	}
}

func authorityTestAgent() db.Agent {
	updatedAt := time.Date(2026, 8, 11, 10, 11, 12, 123456789, time.FixedZone("CST", 8*60*60))
	return db.Agent{
		ID:                 util.MustParseUUID(authorityTestAgentID),
		WorkspaceID:        util.MustParseUUID(authorityTestWorkspaceID),
		Name:               "Audit Worker",
		AvatarUrl:          authorityTextValue("https://example.invalid/avatar.png"),
		RuntimeMode:        "local",
		Status:             "idle",
		MaxConcurrentTasks: 6,
		UpdatedAt:          authorityTimestamp(updatedAt),
		Description:        "presentation only",
		RuntimeID:          util.MustParseUUID(authorityTestRuntimeID),
		CustomEnv:          []byte(`{"SAFE":"initial"}`),
		Model:              authorityTextValue("gpt-5.6"),
		PermissionMode:     "private",
		Kind:               "worker",
		SystemKey:          authorityTextValue("hivecrew-auditor"),
	}
}

func mustBuildAuthoritySnapshot(t *testing.T, agent db.Agent, workspaceID string, agentID string) AuthoritySnapshot {
	t.Helper()
	snapshot, err := BuildHiveCrewAgentAuthoritySnapshot(agent, workspaceID, agentID)
	if err != nil {
		t.Fatalf("BuildHiveCrewAgentAuthoritySnapshot() error = %v", err)
	}
	return snapshot
}

func authorityTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

func authorityTextValue(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}
