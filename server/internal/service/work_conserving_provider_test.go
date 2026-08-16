package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestReadWorkConservingGoalSourceRequiresExplicitBindingAndUsesContentDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CHECKLIST.yaml")
	const content = "schema_version: hivecosm.goal-graph/v2\nwork_conserving_authority:\n  schema_version: hivecosm.goal-graph/v2\n  goal_id: goal-1\n  workspace_id: 00000000-0000-0000-0000-000000000001\n  project_id: 00000000-0000-0000-0000-000000000002\n  source_ref: /goal/CHECKLIST.yaml\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := readWorkConservingGoalSource(path)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if snapshot.Binding.GoalID != "goal-1" || snapshot.Binding.ProjectID == "" || len(snapshot.Digest) != 64 {
		t.Fatalf("snapshot = %+v, want explicit binding and sha256 digest", snapshot)
	}
	if _, err := readWorkConservingGoalSource(filepath.Join(t.TempDir(), "missing.yaml")); err == nil || !strings.Contains(err.Error(), "source gap") {
		t.Fatal("missing source must fail closed")
	}
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "top-level fixture fallback is forbidden",
			content: "schema_version: hivecosm.goal-graph/v2\ngoal_id: goal-1\nworkspace_id: 00000000-0000-0000-0000-000000000001\nproject_id: 00000000-0000-0000-0000-000000000002\nsource_ref: /goal/CHECKLIST.yaml\n",
			want:    "work_conserving_authority",
		},
		{
			name:    "nested workspace scope is missing",
			content: "schema_version: hivecosm.goal-graph/v2\nwork_conserving_authority:\n  schema_version: hivecosm.goal-graph/v2\n  goal_id: goal-1\n  project_id: 00000000-0000-0000-0000-000000000002\n  source_ref: /goal/CHECKLIST.yaml\n",
			want:    "workspace_id",
		},
		{
			name:    "root schema is missing",
			content: "work_conserving_authority:\n  schema_version: hivecosm.goal-graph/v2\n  goal_id: goal-1\n  workspace_id: 00000000-0000-0000-0000-000000000001\n  project_id: 00000000-0000-0000-0000-000000000002\n  source_ref: /goal/CHECKLIST.yaml\n",
			want:    "schema_version",
		},
		{
			name:    "nested schema drifts from document",
			content: "schema_version: hivecosm.goal-graph/v2\nwork_conserving_authority:\n  schema_version: hivecosm.goal-graph/v3\n  goal_id: goal-1\n  workspace_id: 00000000-0000-0000-0000-000000000001\n  project_id: 00000000-0000-0000-0000-000000000002\n  source_ref: /goal/CHECKLIST.yaml\n",
			want:    "schema mismatch",
		},
		{
			name:    "equal but unsupported schema",
			content: "schema_version: hivecosm.goal-graph/v3\nwork_conserving_authority:\n  schema_version: hivecosm.goal-graph/v3\n  goal_id: goal-1\n  workspace_id: 00000000-0000-0000-0000-000000000001\n  project_id: 00000000-0000-0000-0000-000000000002\n  source_ref: /goal/CHECKLIST.yaml\n",
			want:    "unsupported",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readWorkConservingGoalSource(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want fail-closed reason containing %q", err, tc.want)
			}
		})
	}
}

type providerPaginationStore struct {
	shadowStoreFixture
	pages   [][]db.ListIssuesRow
	offsets []int32
}

func (s *providerPaginationStore) ListIssues(_ context.Context, params db.ListIssuesParams) ([]db.ListIssuesRow, error) {
	s.offsets = append(s.offsets, params.Offset)
	index := int(params.Offset) / workConservingPageSize
	if index >= len(s.pages) {
		return []db.ListIssuesRow{}, nil
	}
	return append([]db.ListIssuesRow(nil), s.pages[index]...), nil
}

func providerIssue(workspaceID, projectID pgtype.UUID) db.ListIssuesRow {
	id := uuid.New()
	return db.ListIssuesRow{ID: pgtype.UUID{Bytes: [16]byte(id), Valid: true}, WorkspaceID: workspaceID, ProjectID: projectID}
}

func TestWorkConservingProviderReadsAllPagesAndRejectsDuplicatePageIdentity(t *testing.T) {
	workspaceID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	firstPage := make([]db.ListIssuesRow, workConservingPageSize)
	for i := range firstPage {
		firstPage[i] = providerIssue(workspaceID, projectID)
	}
	secondPage := []db.ListIssuesRow{providerIssue(workspaceID, projectID)}
	store := &providerPaginationStore{pages: [][]db.ListIssuesRow{firstPage, secondPage}}
	shadow := NewContinuousDispatchShadowService(store, shadowDirectoryFixture{}, nil, nil)
	provider := NewFileWorkConservingProjectionProvider(shadow, "unused-by-readAllIssues")
	issues, err := provider.readAllIssues(context.Background(), workspaceID, projectID)
	if err != nil || len(issues) != workConservingPageSize+1 {
		t.Fatalf("issues = %v, err=%v; want complete 200+1 pagination", len(issues), err)
	}
	if len(store.offsets) != 2 || store.offsets[0] != 0 || store.offsets[1] != workConservingPageSize {
		t.Fatalf("offsets = %v, want [0 200]", store.offsets)
	}

	exactStore := &providerPaginationStore{pages: [][]db.ListIssuesRow{firstPage, {}}}
	provider = NewFileWorkConservingProjectionProvider(NewContinuousDispatchShadowService(exactStore, shadowDirectoryFixture{}, nil, nil), "unused-by-readAllIssues")
	issues, err = provider.readAllIssues(context.Background(), workspaceID, projectID)
	if err != nil || len(issues) != workConservingPageSize || len(exactStore.offsets) != 2 {
		t.Fatalf("exact-boundary issues/offsets = %d/%v, err=%v; want 200 rows plus empty boundary read", len(issues), exactStore.offsets, err)
	}

	duplicateStore := &providerPaginationStore{pages: [][]db.ListIssuesRow{firstPage, {firstPage[0]}}}
	duplicate := NewContinuousDispatchShadowService(duplicateStore, shadowDirectoryFixture{}, nil, nil)
	provider = NewFileWorkConservingProjectionProvider(duplicate, "unused-by-readAllIssues")
	if _, err := provider.readAllIssues(context.Background(), workspaceID, projectID); err == nil || !strings.Contains(err.Error(), "duplicated identity") {
		t.Fatal("duplicate page identity must fail closed")
	}
}

func TestWorkConservingProviderRejectsGoalSourceScopeDriftBeforeReadingProject(t *testing.T) {
	workspaceID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	foreignProjectID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	path := filepath.Join(t.TempDir(), "CHECKLIST.yaml")
	content := "schema_version: " + workConservingGoalSchemaV2 + "\nwork_conserving_authority:\n  schema_version: " + workConservingGoalSchemaV2 + "\n  goal_id: goal-1\n  workspace_id: " + uuid.UUID(workspaceID.Bytes).String() + "\n  project_id: " + uuid.UUID(foreignProjectID.Bytes).String() + "\n  source_ref: /goal/checklist\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := NewFileWorkConservingProjectionProvider(
		NewContinuousDispatchShadowService(&providerPaginationStore{}, shadowDirectoryFixture{}, nil, nil),
		path,
	)
	_, err := provider.ProjectWorkConserving(context.Background(), WorkConservingProjectionRequest{WorkspaceID: workspaceID, ProjectID: projectID, Limit: 50})
	if err == nil || !strings.Contains(err.Error(), "scope does not match request") {
		t.Fatalf("error = %v, want source scope drift to fail before project reads", err)
	}
}

func TestWorkConservingProjectionValidationRejectsExpiredSourceAndAcceptsBlockedPlan(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 0, 0, 0, time.UTC)
	workspaceID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	p := WorkConservingProjection{SchemaVersion: WorkConservingProjectionSchemaV1, State: WorkConservingProjectionBlocked, GoalID: "goal-1", Authority: WorkConservingAuthoritySnapshot{WorkspaceID: uuid.UUID(workspaceID.Bytes).String(), ProjectID: uuid.UUID(projectID.Bytes).String(), SourceRef: "/goal/checklist", Revision: "sha256:" + strings.Repeat("a", 64), ObservedAt: now.Add(-time.Minute).Format(time.RFC3339), ExpiresAt: now.Add(10 * time.Minute).Format(time.RFC3339)}, BlockedBacklog: []continuousdispatch.WorkConservingBlockedIssue{{IssueID: "issue-1", GoalID: "goal-1", Reasons: []continuousdispatch.Reason{"source_gap"}, Receiver: "dispatch-coordinator", WakeCondition: "source repaired"}}, Mismatch: continuousdispatch.WorkConservingMismatch{OpenIssues: 1, BlockedBacklog: 1}, Total: 1, Limit: 50, Offset: 0}
	if err := ValidateWorkConservingProjectionAt(p, WorkConservingProjectionRequest{WorkspaceID: workspaceID, ProjectID: projectID, Limit: 50}, now); err != nil {
		t.Fatalf("blocked plan should validate: %v", err)
	}
	for _, malformed := range []string{
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("g", 64),
		strings.Repeat("a", 64),
	} {
		candidate := p
		candidate.Authority.Revision = malformed
		if err := ValidateWorkConservingProjectionAt(candidate, WorkConservingProjectionRequest{WorkspaceID: workspaceID, ProjectID: projectID, Limit: 50}, now); err == nil || !errors.Is(err, ErrWorkConservingProjectionSourceGap) {
			t.Fatalf("revision %q error = %v, want source_gap", malformed, err)
		}
	}
	p.Authority.ExpiresAt = now.Add(-time.Second).Format(time.RFC3339)
	if err := ValidateWorkConservingProjectionAt(p, WorkConservingProjectionRequest{WorkspaceID: workspaceID, ProjectID: projectID, Limit: 50}, now); err == nil {
		t.Fatal("expired source must fail closed")
	}
	if got := now.Add(workConservingProjectionTTL); !got.After(now) {
		t.Fatal("provider TTL must be positive")
	}
}
