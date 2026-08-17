package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/featureflags"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// TestWriterLeaseTerminalPostgresAtomicFence exercises the actual migration-262
// lease rows and the CompleteTask transaction against an isolated PostgreSQL.
// It intentionally uses the existing production CompanyOps fixture only for
// workspace/user/runtime/agent/issue rows; no CompanyOps lineage is attached to
// the task, so a rejected terminal proof has no other terminal side effects to
// mask the task-state assertion.
func TestWriterLeaseTerminalPostgresAtomicFence(t *testing.T) {
	pool := newWriteLeaseTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedProductionCompanyOpsFixture(t, ctx, pool)

	const daemonID = "writer-lease-terminal-test-daemon"
	if _, err := pool.Exec(ctx, `UPDATE agent_runtime SET daemon_id = $2 WHERE id = $1`, fixture.runtimeID, daemonID); err != nil {
		t.Fatalf("bind runtime daemon_id: %v", err)
	}

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, lead_type, lead_id)
		VALUES ($1, $2, 'in_progress', 'agent', $3)
		RETURNING id`, fixture.workspaceID, "writer lease terminal project", fixture.agentID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET project_id = $2 WHERE id = $1`, fixture.issueID, projectID); err != nil {
		t.Fatalf("bind issue project: %v", err)
	}

	resourceIDs := make([]pgtype.UUID, 0, 2)
	refs := []string{"main", "release/v1"}
	for i, ref := range refs {
		var resourceID pgtype.UUID
		resourceURL := fmt.Sprintf("https://github.com/hivecosm/writer-lease-%d", i)
		if err := pool.QueryRow(ctx, `
			INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref, label, position, created_by)
			VALUES ($1, $2, 'github_repo', $3::jsonb, $4, $5, $6)
			RETURNING id`, projectID, fixture.workspaceID,
			fmt.Sprintf(`{"url":%q,"ref":%q}`, resourceURL, ref),
			"writer lease target", i, fixture.userID).Scan(&resourceID); err != nil {
			t.Fatalf("create project resource %d: %v", i, err)
		}
		resourceIDs = append(resourceIDs, resourceID)
	}

	var taskID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, task_kind)
		VALUES ($1, $2, $3, 'running', 0, 'work')
		RETURNING id`, fixture.agentID, fixture.runtimeID, fixture.issueID).Scan(&taskID); err != nil {
		t.Fatalf("create running task: %v", err)
	}

	resources := make([]WriterLeaseResource, 0, len(resourceIDs))
	for i, resourceID := range resourceIDs {
		resources = append(resources, WriterLeaseResource{
			ID: resourceID.Bytes, ResourceType: "github_repo",
			URL: fmt.Sprintf("https://github.com/hivecosm/writer-lease-%d", i), Ref: refs[i],
		})
	}
	targets, err := ResolveWriterLeaseTargets(WriterLeaseModeEnforce,
		fixture.workspaceID.String(), projectID.String(), daemonID, fixture.runtimeID.String(), taskID.String(), resources)
	if err != nil {
		t.Fatalf("resolve fixture targets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("resolved target count = %d, want 2", len(targets))
	}

	leaseService := NewWriteLeaseService(pool)
	holder := WriterLeaseHolderID(daemonID, fixture.runtimeID.String(), taskID.String())
	leases := make([]*WriteLease, 0, len(targets))
	for _, target := range targets {
		lease, acquireErr := leaseService.Acquire(ctx, target.MutexKey, holder, time.Minute)
		if acquireErr != nil {
			t.Fatalf("acquire %s: %v", target.MutexKey, acquireErr)
		}
		leases = append(leases, lease)
		cleanupLease(t, pool, target.MutexKey)
	}

	proof := func(current []*WriteLease) []WriterLeaseTerminalProof {
		out := make([]WriterLeaseTerminalProof, 0, len(current))
		for i, lease := range current {
			resourceID, parseErr := uuid.Parse(targets[i].ResourceID)
			if parseErr != nil {
				t.Fatalf("parse resolved resource id %q: %v", targets[i].ResourceID, parseErr)
			}
			out = append(out, WriterLeaseTerminalProof{
				ResourceID: resourceID, LeaseToken: lease.LeaseToken,
				FenceGeneration: lease.FenceGeneration,
			})
		}
		return out
	}
	oldProof := proof(leases)

	cancelled, err := leaseService.ForceCancel(ctx, targets[0].MutexKey, "integration stale writer")
	if err != nil {
		t.Fatalf("force-cancel first target: %v", err)
	}
	if cancelled.Status != WriteLeaseExpired || cancelled.ExpiresAt != nil {
		t.Fatalf("force-cancel state = status:%s expires:%v, want expired/NULL", cancelled.Status, cancelled.ExpiresAt)
	}

	flags := featureflag.NewStaticProvider()
	flags.Set(featureflags.WriterLeaseMode, featureflag.Rule{Default: true, Variant: string(WriterLeaseModeEnforce)})
	taskService := &TaskService{
		Queries: db.New(pool), TxStarter: pool, Bus: events.New(),
		FeatureFlags: featureflag.NewService(flags),
	}
	result := []byte(`{"output":"terminal integration result"}`)

	_, err = taskService.CompleteTaskWithWriterLeaseProof(ctx, taskID, result, "", "", false, "", oldProof)
	if !errors.Is(err, ErrWriterLeaseFenceRejected) {
		t.Fatalf("stale proof error = %v, want ErrWriterLeaseFenceRejected", err)
	}
	assertWriterLeaseTaskStillRunning(t, ctx, pool, taskID)

	var persistedResult []byte
	if err := pool.QueryRow(ctx, `SELECT result FROM agent_task_queue WHERE id = $1`, taskID).Scan(&persistedResult); err != nil {
		t.Fatalf("read rejected task result: %v", err)
	}
	if len(persistedResult) != 0 {
		t.Fatalf("rejected completion persisted result: %s", persistedResult)
	}

	newLease, err := leaseService.Acquire(ctx, targets[0].MutexKey, holder, time.Minute)
	if err != nil {
		t.Fatalf("reacquire first target: %v", err)
	}
	if newLease.FenceGeneration <= leases[0].FenceGeneration || newLease.LeaseToken == leases[0].LeaseToken {
		t.Fatalf("reacquire did not fence old lease: old gen/token=%d/%s new=%d/%s", leases[0].FenceGeneration, leases[0].LeaseToken, newLease.FenceGeneration, newLease.LeaseToken)
	}
	currentLeases := []*WriteLease{newLease, leases[1]}

	completed, err := taskService.CompleteTaskWithWriterLeaseProof(ctx, taskID, result, "", "", false, "", proof(currentLeases))
	if err != nil {
		t.Fatalf("exact current proof completion: %v", err)
	}
	if completed.Status != "completed" {
		t.Fatalf("completed task status = %q, want completed", completed.Status)
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read completed task status: %v", err)
	}
	if status != "completed" {
		t.Fatalf("persisted task status = %q, want completed", status)
	}
	if err := pool.QueryRow(ctx, `SELECT result FROM agent_task_queue WHERE id = $1`, taskID).Scan(&persistedResult); err != nil {
		t.Fatalf("read completed task result: %v", err)
	}
	for proofName, candidateProof := range map[string][]WriterLeaseTerminalProof{
		"old": oldProof, "current": proof(currentLeases),
	} {
		for _, item := range candidateProof {
			if strings.Contains(string(persistedResult), item.LeaseToken.String()) {
				t.Fatalf("persisted task result leaked %s terminal proof token %s: %s", proofName, item.LeaseToken, persistedResult)
			}
		}
	}
	if strings.Contains(string(persistedResult), "fence_generation") || strings.Contains(string(persistedResult), "lease_token") {
		t.Fatalf("persisted task result leaked terminal proof fields: %s", persistedResult)
	}
	var decoded map[string]any
	if err := json.Unmarshal(persistedResult, &decoded); err != nil {
		t.Fatalf("persisted result is not JSON: %v", err)
	}
	if decoded["output"] != "terminal integration result" {
		t.Fatalf("persisted result output = %#v", decoded["output"])
	}
}

func assertWriterLeaseTaskStillRunning(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID pgtype.UUID) {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read stale task status: %v", err)
	}
	if status != "running" {
		t.Fatalf("stale completion changed task status to %q, want running", status)
	}
}
