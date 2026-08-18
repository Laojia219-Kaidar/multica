package handler_e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestWriterLeaseHandlerRemoteTerminalE2E is the C2a control-plane fixture.
// It deliberately uses a separate package from internal/handler so the
// handler package TestMain cannot fall back to localhost:5432. The test only
// proves the HTTP/DB boundary: it does not run a daemon runner or touch a
// checkout, subprocess, filesystem, GPU, or model endpoint.
func TestWriterLeaseHandlerRemoteTerminalE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	pool := openIsolatedE2EPool(t, ctx)
	fixture := seedWriterLeaseE2EFixture(t, ctx, pool)

	queries := db.New(pool)
	hub := realtime.NewHub()
	h := handler.New(queries, pool, hub, events.New(), service.NewEmailService(), nil, nil, analytics.NoopClient{}, handler.Config{AllowSignup: true})
	h.WriterLeaseService = service.NewWriteLeaseService(pool)
	flagsProvider := featureflag.NewStaticProvider()
	flagsProvider.Set(featureflags.WriterLeaseMode, featureflag.Rule{
		Default: true, Variant: string(service.WriterLeaseModeEnforce),
	})
	flags := featureflag.NewService(flagsProvider)
	h.FeatureFlags = flags
	h.TaskService.FeatureFlags = flags

	r := chi.NewRouter()
	r.Use(middleware.DaemonAuth(queries, nil, nil, nil))
	r.Post("/api/daemon/runtimes/{runtimeId}/tasks/claim", h.ClaimTaskByRuntime)
	r.Post("/api/daemon/runtimes/{runtimeId}/tasks/{taskId}/writer-lease/{action}", h.WriterLease)
	r.Post("/api/daemon/tasks/{taskId}/start", h.StartTask)
	r.Post("/api/daemon/tasks/{taskId}/complete", h.CompleteTask)
	server := httptest.NewServer(mediatedLinuxDaemonHandler(r))
	defer server.Close()

	clientA := daemon.NewClient(server.URL)
	clientA.SetToken(fixture.daemonTokenA)
	clientB := daemon.NewClient(server.URL)
	clientB.SetToken(fixture.daemonTokenB)

	task, err := clientA.ClaimTask(ctx, fixture.runtimeID)
	if err != nil {
		t.Fatalf("daemon-A claim: %v", err)
	}
	if task == nil {
		t.Fatal("daemon-A claim returned nil task")
	}
	if task.ID != fixture.taskID || task.WriterLeaseMode != string(service.WriterLeaseModeEnforce) {
		t.Fatalf("claim = id:%s mode:%s, want id:%s mode:enforce", task.ID, task.WriterLeaseMode, fixture.taskID)
	}
	if len(task.WriterLeaseTargets) != 2 {
		t.Fatalf("claim returned %d writer targets, want 2", len(task.WriterLeaseTargets))
	}
	claimJSON, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal claim: %v", err)
	}
	for _, forbidden := range []string{"lease_token", "fence_generation"} {
		if strings.Contains(string(claimJSON), forbidden) {
			t.Fatalf("claim leaked terminal field %q: %s", forbidden, claimJSON)
		}
	}
	if !strings.HasPrefix(task.AuthToken, "mat_") {
		t.Fatalf("claim auth token = %q, want mat_ token", task.AuthToken)
	}
	var tokenHash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM task_token WHERE task_id = $1`, fixture.taskID).Scan(&tokenHash); err != nil {
		t.Fatalf("read task token hash: %v", err)
	}
	if tokenHash != auth.HashToken(task.AuthToken) || tokenHash == task.AuthToken {
		t.Fatalf("task token persistence is not hash-only: %q", tokenHash)
	}
	var persistedMode, persistedDigest string
	var persistedSnapshot []byte
	if err := pool.QueryRow(ctx, `SELECT writer_lease_claim_mode, writer_lease_target_snapshot, writer_lease_target_digest FROM agent_task_queue WHERE id = $1`, fixture.taskID).Scan(&persistedMode, &persistedSnapshot, &persistedDigest); err != nil {
		t.Fatalf("read persisted writer lease snapshot: %v", err)
	}
	if persistedMode != string(service.WriterLeaseModeEnforce) || len(persistedSnapshot) == 0 || len(persistedDigest) != 64 {
		t.Fatalf("persisted writer lease claim = mode:%q snapshot:%s digest:%q", persistedMode, persistedSnapshot, persistedDigest)
	}
	assertTaskStatus(t, ctx, pool, fixture.taskID, "dispatched")

	serviceTargets := make([]service.WriterLeaseTarget, 0, len(task.WriterLeaseTargets))
	for _, target := range task.WriterLeaseTargets {
		serviceTargets = append(serviceTargets, service.WriterLeaseTarget{
			ResourceID: target.ResourceID, MutexKey: target.MutexKey,
			URL: target.URL, Ref: target.Ref,
		})
	}
	storeA := daemon.NewRemoteWriterLeaseStore(clientA, fixture.runtimeID, fixture.taskID, task.WriterLeaseTargets)

	// A second authenticated daemon may share the workspace, but it cannot
	// operate a runtime whose persisted daemon_id belongs to daemon-A.
	storeB := daemon.NewRemoteWriterLeaseStore(clientB, fixture.runtimeID, fixture.taskID, task.WriterLeaseTargets)
	if _, err := storeB.Acquire(ctx, serviceTargets[0].MutexKey, "forged-holder", service.DefaultLeaseDuration); err == nil || !strings.Contains(err.Error(), "returned 403") {
		t.Fatalf("cross-daemon acquire error = %v, want HTTP 403", err)
	}
	assertNoHeldLease(t, ctx, pool, serviceTargets[0].MutexKey)

	leaseService := service.NewWriteLeaseService(pool)

	oldLeases := make(map[string]*service.WriteLease, len(serviceTargets))
	for _, target := range serviceTargets {
		lease, err := storeA.Acquire(ctx, target.MutexKey, "daemon-holder-is-not-sent", service.DefaultLeaseDuration)
		if err != nil {
			t.Fatalf("remote acquire %s: %v", target.ResourceID, err)
		}
		oldLeases[target.ResourceID] = lease
		if lease.HolderID != service.WriterLeaseHolderID(fixture.daemonID, fixture.runtimeID, fixture.taskID) {
			t.Fatalf("server holder = %q, want canonical holder", lease.HolderID)
		}
	}

	if err := clientA.StartTask(ctx, fixture.taskID); err != nil {
		t.Fatalf("daemon-A start: %v", err)
	}
	assertTaskStatus(t, ctx, pool, fixture.taskID, "running")

	// ForceCancel is intentionally direct control-plane recovery: there is no
	// public daemon force-cancel endpoint. This still uses the real migration-262
	// service and invalidates the remote daemon's old token/generation.
	first := serviceTargets[0]
	if _, err := leaseService.ForceCancel(ctx, first.MutexKey, "C2a stale writer recovery"); err != nil {
		t.Fatalf("force-cancel first target: %v", err)
	}
	// Flip the current rollout flag after claim. The persisted enforce snapshot
	// remains authoritative, so stale proof must still be rejected under 412.
	flagsProvider.Set(featureflags.WriterLeaseMode, featureflag.Rule{Default: true, Variant: string(service.WriterLeaseModeOff)})

	oldProof := make([]daemon.WriterLeaseTerminalProof, 0, len(serviceTargets))
	for _, target := range serviceTargets {
		lease := oldLeases[target.ResourceID]
		oldProof = append(oldProof, daemon.WriterLeaseTerminalProof{
			ResourceID: target.ResourceID, LeaseToken: lease.LeaseToken,
			FenceGeneration: lease.FenceGeneration,
		})
	}
	if err := clientA.CompleteTaskWithWriterLeaseProof(ctx, fixture.taskID, `{"output":"stale-result"}`, "", "", "", false, "", oldProof); err == nil || !strings.Contains(err.Error(), "returned 412") {
		t.Fatalf("stale completion error = %v, want HTTP 412", err)
	}
	assertTaskStatus(t, ctx, pool, fixture.taskID, "running")
	var rejectedResult []byte
	if err := pool.QueryRow(ctx, `SELECT result FROM agent_task_queue WHERE id = $1`, fixture.taskID).Scan(&rejectedResult); err != nil {
		t.Fatalf("read rejected result: %v", err)
	}
	if len(rejectedResult) != 0 {
		t.Fatalf("stale completion persisted result: %s", rejectedResult)
	}

	if _, err := storeA.Acquire(ctx, first.MutexKey, "daemon-holder-is-not-sent", service.DefaultLeaseDuration); err == nil || !strings.Contains(err.Error(), "returned 409") {
		t.Fatalf("running-task remote reacquire error = %v, want HTTP 409 safety gate", err)
	}
	assertTaskStatus(t, ctx, pool, fixture.taskID, "running")
	flagsProvider.Set(featureflags.WriterLeaseMode, featureflag.Rule{Default: true, Variant: string(service.WriterLeaseModeEnforce)})

	// Task B is a fresh workspace/runtime/task so the successful terminal path
	// cannot accidentally reuse Task A's lease or daemon credentials.
	fixtureB := seedWriterLeaseE2EFixture(t, ctx, pool)
	clientBTask := daemon.NewClient(server.URL)
	clientBTask.SetToken(fixtureB.daemonTokenA)
	taskB, err := clientBTask.ClaimTask(ctx, fixtureB.runtimeID)
	if err != nil {
		t.Fatalf("daemon-B claim: %v", err)
	}
	if taskB == nil || taskB.ID != fixtureB.taskID || len(taskB.WriterLeaseTargets) != 2 {
		t.Fatalf("task-B claim = %#v, want two-target fresh claim", taskB)
	}
	storeBTask := daemon.NewRemoteWriterLeaseStore(clientBTask, fixtureB.runtimeID, fixtureB.taskID, taskB.WriterLeaseTargets)
	currentProofB := make([]daemon.WriterLeaseTerminalProof, 0, len(taskB.WriterLeaseTargets))
	for _, target := range taskB.WriterLeaseTargets {
		serviceTarget := service.WriterLeaseTarget{ResourceID: target.ResourceID, MutexKey: target.MutexKey, URL: target.URL, Ref: target.Ref}
		lease, err := storeBTask.Acquire(ctx, serviceTarget.MutexKey, "daemon-holder-is-not-sent", service.DefaultLeaseDuration)
		if err != nil {
			t.Fatalf("task-B remote acquire %s: %v", target.ResourceID, err)
		}
		currentProofB = append(currentProofB, daemon.WriterLeaseTerminalProof{
			ResourceID: target.ResourceID, LeaseToken: lease.LeaseToken,
			FenceGeneration: lease.FenceGeneration,
		})
	}
	// The task-level snapshot is the authority after claim: mutate one target,
	// delete another, and add a new project resource. The real HTTP handler must
	// still resolve renew/verify/release against the original two target rows;
	// the newly added resource must not become an implicit completion target.
	if _, err := pool.Exec(ctx, `UPDATE project_resource SET resource_ref = $2::jsonb WHERE id = $1`, fixtureB.resourceIDs[0], `{"url":"https://github.com/hivecosm/c2a-mutated","ref":"changed"}`); err != nil {
		t.Fatalf("mutate task-B first resource: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM project_resource WHERE id = $1`, fixtureB.resourceIDs[1]); err != nil {
		t.Fatalf("delete task-B second resource: %v", err)
	}
	var addedResourceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref, label, position, created_by)
		VALUES ($1, $2, 'github_repo', $3::jsonb, 'C2a added target', 9, $4) RETURNING id
	`, fixtureB.projectID, fixtureB.workspaceID, `{"url":"https://github.com/hivecosm/c2a-added","ref":"new"}`, fixtureB.userID).Scan(&addedResourceID); err != nil {
		t.Fatalf("add task-B resource: %v", err)
	}
	if addedResourceID == "" {
		t.Fatal("added task-B resource id is empty")
	}
	for _, target := range taskB.WriterLeaseTargets {
		serviceTarget := service.WriterLeaseTarget{ResourceID: target.ResourceID, MutexKey: target.MutexKey, URL: target.URL, Ref: target.Ref}
		// The proof list preserves taskB target order, while the lease token is
		// looked up by resource below so this remains correct if target sorting
		// changes.
		var proof daemon.WriterLeaseTerminalProof
		for _, candidate := range currentProofB {
			if candidate.ResourceID == target.ResourceID {
				proof = candidate
				break
			}
		}
		if proof.ResourceID == "" {
			t.Fatalf("missing task-B proof for resource %s", target.ResourceID)
		}
		if _, err := storeBTask.VerifyHeld(ctx, serviceTarget.MutexKey, proof.LeaseToken, proof.FenceGeneration); err != nil {
			t.Fatalf("verify persisted task-B target after resource drift %s: %v", target.ResourceID, err)
		}
	}
	if err := clientBTask.StartTask(ctx, fixtureB.taskID); err != nil {
		t.Fatalf("daemon-B start: %v", err)
	}
	assertTaskStatus(t, ctx, pool, fixtureB.taskID, "running")
	if err := clientBTask.CompleteTaskWithWriterLeaseProof(ctx, fixtureB.taskID, "task-b-result", "", "", "", false, "", currentProofB); err != nil {
		t.Fatalf("task-B current completion: %v", err)
	}
	assertTaskStatus(t, ctx, pool, fixtureB.taskID, "completed")
	var persistedResultB []byte
	if err := pool.QueryRow(ctx, `SELECT result FROM agent_task_queue WHERE id = $1`, fixtureB.taskID).Scan(&persistedResultB); err != nil {
		t.Fatalf("read task-B completed result: %v", err)
	}
	for _, proof := range append(oldProof, currentProofB...) {
		if strings.Contains(string(persistedResultB), proof.LeaseToken.String()) {
			t.Fatalf("task-B result leaked lease token %s: %s", proof.LeaseToken, persistedResultB)
		}
	}
	for _, forbidden := range []string{"lease_token", "fence_generation", "writer_lease_proof"} {
		if strings.Contains(string(persistedResultB), forbidden) {
			t.Fatalf("task-B result leaked proof field %q: %s", forbidden, persistedResultB)
		}
	}
	var decodedB map[string]any
	if err := json.Unmarshal(persistedResultB, &decodedB); err != nil {
		t.Fatalf("task-B result is not JSON: %v", err)
	}
	if decodedB["output"] != "task-b-result" {
		t.Fatalf("task-B output = %#v, want task-b-result", decodedB["output"])
	}
}

type writerLeaseE2EFixture struct {
	workspaceID  string
	userID       string
	runtimeID    string
	agentID      string
	issueID      string
	projectID    string
	taskID       string
	resourceIDs  []string
	daemonID     string
	daemonTokenA string
	daemonTokenB string
}

func openIsolatedE2EPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	required := os.Getenv("HIVECREW_ISOLATED_TEST_REQUIRED") == "1"
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		if required {
			t.Fatalf("DATABASE_URL must explicitly select a temporary non-5432 Postgres")
		}
		t.Skip("DATABASE_URL must explicitly select a temporary non-5432 Postgres")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		t.Fatalf("refusing non-loopback DATABASE_URL host %q", host)
	}
	port := parsed.Port()
	if port == "" {
		t.Fatalf("refusing DATABASE_URL without explicit port")
	}
	if port == "5432" {
		t.Fatalf("refusing shared PostgreSQL port 5432")
	}
	if expected := strings.TrimSpace(os.Getenv("HIVECREW_ISOLATED_TEST_PORT")); expected != "" && expected != port {
		t.Fatalf("DATABASE_URL port %s does not match HIVECREW_ISOLATED_TEST_PORT=%s", port, expected)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		if required {
			t.Fatalf("isolated Postgres unavailable: %v", err)
		}
		t.Skipf("isolated Postgres unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		if required {
			t.Fatalf("isolated Postgres unreachable: %v", err)
		}
		t.Skipf("isolated Postgres unreachable: %v", err)
	}
	var hasLeaseTable bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.write_lease') IS NOT NULL`).Scan(&hasLeaseTable); err != nil {
		pool.Close()
		t.Fatalf("check migration 262: %v", err)
	}
	if !hasLeaseTable {
		pool.Close()
		if required {
			t.Fatalf("migration 262 write_lease table is not applied")
		}
		t.Skip("migration 262 write_lease table is not applied")
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedWriterLeaseE2EFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) writerLeaseE2EFixture {
	t.Helper()
	suffix := uuid.NewString()
	daemonID := "c2a-daemon-a-" + suffix
	var f writerLeaseE2EFixture
	f.daemonID = daemonID
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, "C2a Writer Lease", "c2a-"+suffix+"@multica.test").Scan(&f.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1, $2, $3, $4) RETURNING id`, "C2a Writer Lease", "c2a-"+suffix, "HTTP writer lease fixture", "C2A").Scan(&f.workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, statement := range []string{
			`DELETE FROM daemon_token WHERE workspace_id = $1`,
			`DELETE FROM write_lease WHERE mutex_key LIKE $1`,
			`DELETE FROM task_token WHERE task_id = $1`,
			`DELETE FROM agent_task_queue WHERE id = $1`,
			`DELETE FROM project_resource WHERE project_id = $1`,
			`DELETE FROM project WHERE id = $1`,
			`DELETE FROM issue WHERE id = $1`,
			`DELETE FROM agent WHERE id = $1`,
			`DELETE FROM agent_runtime WHERE id = $1`,
			`DELETE FROM member WHERE workspace_id = $1`,
			`DELETE FROM workspace WHERE id = $1`,
			`DELETE FROM "user" WHERE id = $1`,
		} {
			var args []any
			switch statement {
			case `DELETE FROM write_lease WHERE mutex_key LIKE $1`:
				args = []any{"canonical-worktree:ws/" + f.workspaceID + "/%"}
			case `DELETE FROM task_token WHERE task_id = $1`, `DELETE FROM agent_task_queue WHERE id = $1`:
				args = []any{f.taskID}
			case `DELETE FROM project_resource WHERE project_id = $1`, `DELETE FROM project WHERE id = $1`:
				args = []any{f.projectID}
			case `DELETE FROM issue WHERE id = $1`:
				args = []any{f.issueID}
			case `DELETE FROM agent WHERE id = $1`:
				args = []any{f.agentID}
			case `DELETE FROM agent_runtime WHERE id = $1`:
				args = []any{f.runtimeID}
			case `DELETE FROM member WHERE workspace_id = $1`, `DELETE FROM workspace WHERE id = $1`:
				args = []any{f.workspaceID}
			case `DELETE FROM "user" WHERE id = $1`:
				args = []any{f.userID}
			default:
				args = []any{f.workspaceID}
			}
			if _, err := pool.Exec(cleanupCtx, statement, args...); err != nil {
				t.Errorf("cleanup %s: %v", statement, err)
			}
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, f.workspaceID, f.userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
		VALUES ($1, $2, $3, 'local', 'codex', 'online', 'c2a mediated Linux Codex test runtime', '{}'::jsonb, $4, now()) RETURNING id
	`, f.workspaceID, daemonID, "C2a runtime", f.userID).Scan(&f.runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
		VALUES ($1, 'C2a agent', '', 'local', '{}'::jsonb, $2, 'private', 1, $3) RETURNING id
	`, f.workspaceID, f.runtimeID, f.userID).Scan(&f.agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'C2a issue', 'in_progress', 'none', $2, 'member', (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), 0) RETURNING id
	`, f.workspaceID, f.userID).Scan(&f.issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO project (workspace_id, title, status, lead_type, lead_id) VALUES ($1, 'C2a project', 'in_progress', 'agent', $2) RETURNING id`, f.workspaceID, f.agentID).Scan(&f.projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET project_id = $2 WHERE id = $1`, f.issueID, f.projectID); err != nil {
		t.Fatalf("bind project to issue: %v", err)
	}
	for i, ref := range []string{"main", "release/v1"} {
		var resourceID string
		resourceURL := fmt.Sprintf("https://github.com/hivecosm/c2a-%d", i)
		resourceRef := fmt.Sprintf(`{"url":%q,"ref":%q}`, resourceURL, ref)
		if err := pool.QueryRow(ctx, `
			INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref, label, position, created_by)
			VALUES ($1, $2, 'github_repo', $3::jsonb, 'C2a target', $4, $5) RETURNING id
		`, f.projectID, f.workspaceID, resourceRef, i, f.userID).Scan(&resourceID); err != nil {
			t.Fatalf("seed project resource %d: %v", i, err)
		}
		f.resourceIDs = append(f.resourceIDs, resourceID)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, task_kind)
		VALUES ($1, $2, $3, 'queued', 0, 'work') RETURNING id
	`, f.agentID, f.runtimeID, f.issueID).Scan(&f.taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	for _, id := range []string{daemonID, "c2a-daemon-b-" + suffix} {
		raw, err := auth.GenerateDaemonToken()
		if err != nil {
			t.Fatalf("generate daemon token: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO daemon_token (token_hash, workspace_id, daemon_id, expires_at) VALUES ($1, $2, $3, now() + interval '1 hour')`, auth.HashToken(raw), f.workspaceID, id); err != nil {
			t.Fatalf("seed daemon token %s: %v", id, err)
		}
		if id == daemonID {
			f.daemonTokenA = raw
		} else {
			f.daemonTokenB = raw
		}
	}
	return f
}

func mediatedLinuxDaemonHandler(next http.Handler) http.Handler {
	// Keep the control-plane fixture host-independent while explicitly modeling
	// the only runtime allowed to claim writer-lease enforce tasks.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Client-OS", "linux")
		capabilities := strings.TrimSpace(r.Header.Get("X-Client-Capabilities"))
		if !strings.Contains(","+capabilities+",", ","+protocol.DaemonCapabilityMediatedOverlayV1+",") {
			if capabilities != "" {
				capabilities += ","
			}
			capabilities += protocol.DaemonCapabilityMediatedOverlayV1
			r.Header.Set("X-Client-Capabilities", capabilities)
		}
		next.ServeHTTP(w, r)
	})
}

func assertTaskStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&got); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if got != want {
		t.Fatalf("task status = %q, want %q", got, want)
	}
}

func assertNoHeldLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mutexKey string) {
	t.Helper()
	var status, holder string
	err := pool.QueryRow(ctx, `SELECT status, COALESCE(holder_id, '') FROM write_lease WHERE mutex_key = $1`, mutexKey).Scan(&status, &holder)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		t.Fatalf("read lease %s: %v", mutexKey, err)
	}
	if status == string(service.WriteLeaseHeld) {
		t.Fatalf("lease %s remains held by %q", mutexKey, holder)
	}
}
