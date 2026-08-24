package handler_e2e_test

// A2 Work Wall handler E2E fixture (HIV-1031 R1). Real handler.New, real
// chi router with the production workspace-member middleware, real
// httptest server, real PostgreSQL — the only stubs are the transport-level
// identity headers the auth middleware would set upstream. The fixture
// proves the HTTP/DB boundary of GET /api/work-wall/a2/snapshot and
// /api/work-wall/a2/stream: snapshot happy path, hidden-pane non-leak, SSE
// initial retry+snapshot, membership-revocation close, and the R1 fix —
// visibility revocation closes the stream with the fixed redacted
// workwall_a2_access_revoked code instead of silently streaming a shrunken
// wall.
//
// It deliberately lives in a separate package from internal/handler so the
// handler package TestMain can never fall back to localhost:5432, and it
// reuses the handler_e2e isolated-DB rules: loopback host only, explicit
// non-5432 port, closed dedicated db-name allowlist, fail-closed when
// HIVECREW_ISOLATED_TEST_REQUIRED=1, and migration-401 work_event present.
// Without a dedicated database the fixture skips (or fails closed), and the
// issue reports BLOCKED-on-live-DB-evidence — never PASS on skips alone.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// a2E2EDBNames is the closed allowlist of dedicated throwaway database names
// this fixture may write into — the same exact-name set as the workwall
// package guard. Substring lookalikes (contest, latest, testing_prod) are
// refused by the exact map lookup.
var a2E2EDBNames = map[string]bool{
	"a2b1_itest":   true,
	"a2b1_test":    true,
	"hivetest":     true,
	"multica_test": true,
}

// openA2WorkWallE2EPool applies the handler_e2e isolated-DB rules to
// DATABASE_URL: fail-closed when HIVECREW_ISOLATED_TEST_REQUIRED=1, loopback
// host only, explicit port only, port must not normalize to the shared
// production 5432, database name must be exactly one of the dedicated
// throwaway names, and the migration-401 work_event ledger must exist.
// The DSN itself is never logged.
func openA2WorkWallE2EPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	required := os.Getenv("HIVECREW_ISOLATED_TEST_REQUIRED") == "1"
	fail := func(format string, args ...any) {
		if required {
			t.Fatalf(format, args...)
		}
		t.Skipf(format, args...)
	}
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		fail("DATABASE_URL must explicitly select a dedicated temporary non-5432 Postgres")
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
	// Numeric-only, no signs/separators, and not the production port after
	// normalization ("05432" is as shared as "5432").
	normalizedPort, err := strconv.Atoi(port)
	if err != nil || normalizedPort < 1 || normalizedPort > 65535 || normalizedPort == 5432 {
		t.Fatalf("refusing shared/invalid PostgreSQL port %q", port)
	}
	if expected := strings.TrimSpace(os.Getenv("HIVECREW_ISOLATED_TEST_PORT")); expected != "" && expected != port {
		t.Fatalf("DATABASE_URL port %s does not match HIVECREW_ISOLATED_TEST_PORT=%s", port, expected)
	}
	name := strings.ToLower(strings.TrimPrefix(parsed.Path, "/"))
	if !a2E2EDBNames[name] {
		t.Fatalf("refusing non-dedicated DATABASE_URL database name %q (exact-name allowlist: %v)", name, a2E2EDBNameList())
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fail("dedicated Postgres unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		fail("dedicated Postgres unreachable: %v", err)
	}
	var hasLedger bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.work_event') IS NOT NULL`).Scan(&hasLedger); err != nil {
		pool.Close()
		t.Fatalf("check migration 401: %v", err)
	}
	if !hasLedger {
		pool.Close()
		fail("migration 401 work_event ledger is not applied")
	}
	t.Cleanup(pool.Close)
	return pool
}

func a2E2EDBNameList() string {
	names := []string{"a2b1_itest", "a2b1_test", "hivetest", "multica_test"}
	return strings.Join(names, ", ")
}

// a2E2EDigest satisfies the sha256 digest CHECK constraints of the evidence
// tables (same shape as the workwall integration fixture).
const a2E2EDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// a2WorkWallE2EFixture is one hermetic workspace: an owner, a plain viewer
// member, one shared employee agent the viewer is allow-listed to see, and
// one hidden private employee agent the viewer can never see. Each agent
// carries the full canonical evidence chain (runtime, issue, running task,
// ledger progress event, receipt claim + dispatch receipt, session-matched
// terminal heartbeat) so its work_ref projects as one pane.
type a2WorkWallE2EFixture struct {
	workspaceID string
	ownerID     string
	viewerID    string

	sharedAgentID  string
	sharedWorkRef  string
	sharedRuntime  string
	sharedIssue    string
	sharedTask     string
	sharedSession  string
	sharedReceipt  string
	sharedDispatch string

	hiddenAgentID string
	hiddenWorkRef string
	hiddenRuntime string
	hiddenIssue   string
	hiddenTask    string
	hiddenSession string
}

func seedA2WorkWallE2EFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) a2WorkWallE2EFixture {
	t.Helper()
	suffix := uuid.NewString()
	var f a2WorkWallE2EFixture

	// Seed order (dependencies first); cleanup below runs the exact reverse.
	if err := pool.QueryRow(ctx,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id::text`,
		"A2 E2E Owner", "a2e2e-owner-"+suffix+"@multica.test").Scan(&f.ownerID); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id::text`,
		"A2 E2E Viewer", "a2e2e-viewer-"+suffix+"@multica.test").Scan(&f.viewerID); err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1, $2, $3, 'A2E') RETURNING id::text`,
		"A2 E2E "+suffix, "a2e2e-"+suffix, "A2 work wall handler E2E fixture").Scan(&f.workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, f.workspaceID, f.ownerID); err != nil {
		t.Fatalf("seed owner member: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, f.workspaceID, f.viewerID); err != nil {
		t.Fatalf("seed viewer member: %v", err)
	}

	seedEmployee := func(label string) (agentID, runtimeID, issueID, taskID, workRef, session string) {
		if err := pool.QueryRow(ctx,
			`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider) VALUES ($1, $2, 'local', 'prime') RETURNING id::text`,
			f.workspaceID, "A2E2E runtime "+label+" "+suffix).Scan(&runtimeID); err != nil {
			t.Fatalf("seed runtime %s: %v", label, err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO agent (workspace_id, name, runtime_mode, kind, runtime_id, owner_id, permission_mode)
			 VALUES ($1, $2, 'local', 'user', $3, $4, CASE WHEN $5::text = 'shared' THEN 'public_to' ELSE 'private' END) RETURNING id::text`,
			f.workspaceID, "A2E2E employee "+label, runtimeID, f.ownerID, label).Scan(&agentID); err != nil {
			t.Fatalf("seed agent %s: %v", label, err)
		}
		if label == "shared" {
			// The viewer is on the shared agent's invocation allow-list, so a
			// plain member resolves it as visible; everyone else stays off.
			if _, err := pool.Exec(ctx,
				`INSERT INTO agent_invocation_target (agent_id, target_type, target_id, created_by)
				 VALUES ($1, 'member', $2, $3)`, agentID, f.viewerID, f.ownerID); err != nil {
				t.Fatalf("seed invocation target: %v", err)
			}
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO issue (workspace_id, title, status, creator_id, creator_type, number, position)
			 VALUES ($1, $2, 'in_progress', $3, 'member',
			         (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), 0)
			 RETURNING id::text`,
			f.workspaceID, "A2E2E issue "+label, f.ownerID).Scan(&issueID); err != nil {
			t.Fatalf("seed issue %s: %v", label, err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status) VALUES ($1, $2, $3, 'running') RETURNING id::text`,
			agentID, issueID, runtimeID).Scan(&taskID); err != nil {
			t.Fatalf("seed task %s: %v", label, err)
		}
		workRef = fmt.Sprintf("hivecrew://%s/work/inbox/%s/%s", f.workspaceID, issueID, taskID)
		session = "a2e2e-" + label + "-" + suffix
		now := time.Now().UTC()
		if _, err := pool.Exec(ctx,
			`INSERT INTO work_event (workspace_id, work_ref, session_id, event_type, event_payload, idempotency_key, occurred_at, observed_at)
			 VALUES ($1, $2, $3, 'progress', '{"stage":"e2e"}'::jsonb, $4, $5, $5)`,
			f.workspaceID, workRef, session, "a2e2e-"+label+"-"+suffix, now); err != nil {
			t.Fatalf("seed work_event %s: %v", label, err)
		}
		var commandID string
		if err := pool.QueryRow(ctx,
			`INSERT INTO execution_receipt (task_id, workspace_id, issue_id, assignment_command_id,
			   work_order_ref, work_order_revision, work_order_digest, input_digest,
			   employee_ref, employee_revision, employee_digest,
			   binding_ref, binding_revision, binding_digest,
			   agent_ref, agent_revision, agent_digest,
			   runtime_snapshot, runtime_digest, claimed_at)
			 VALUES ($1,$2,$3,gen_random_uuid(),
			   'wo://a2e2e','r1',$4,$4,'emp://a2e2e','r1',$4,'bind://a2e2e','r1',$4,'agent://a2e2e','r1',$4,
			   '{}'::json,$4,$5) RETURNING assignment_command_id::text`,
			taskID, f.workspaceID, issueID, a2E2EDigest, now).Scan(&commandID); err != nil {
			t.Fatalf("seed execution receipt %s: %v", label, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO assignment_dispatch_receipt (command_id, workspace_id, issue_id, local_agent_id, initial_task_id,
			   work_order_ref, work_order_revision, work_order_digest, input_digest,
			   employee_ref, employee_revision, employee_digest,
			   binding_ref, binding_revision, binding_digest,
			   agent_ref, agent_revision, agent_digest)
			 VALUES ($1,$2,$3,$4,$5,'wo://a2e2e','r1',$6,$6,'emp://a2e2e','r1',$6,'bind://a2e2e','r1',$6,'agent://a2e2e','r1',$6)`,
			commandID, f.workspaceID, issueID, agentID, taskID, a2E2EDigest); err != nil {
			t.Fatalf("seed dispatch receipt %s: %v", label, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO terminal_presence (workspace_id, host, session_name, current_command, agent_hint)
			 VALUES ($1, 'a2e2e-host', $2, 'go test', $3)`,
			f.workspaceID, session, agentID); err != nil {
			t.Fatalf("seed terminal presence %s: %v", label, err)
		}
		return agentID, runtimeID, issueID, taskID, workRef, session
	}

	f.sharedAgentID, f.sharedRuntime, f.sharedIssue, f.sharedTask, f.sharedWorkRef, f.sharedSession = seedEmployee("shared")
	f.hiddenAgentID, f.hiddenRuntime, f.hiddenIssue, f.hiddenTask, f.hiddenWorkRef, f.hiddenSession = seedEmployee("hidden")

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// Exact reverse of the seed order. work_event is append-only by
		// trigger (migration 401), so its rows are removed under a scoped
		// disable/re-enable of that trigger — the same dedicated-test-DB
		// recovery pattern as the companyops outcome-center suite; never
		// against a shared database (the pool guard above refuses one).
		for _, step := range []struct {
			stmt string
			args []any
		}{
			{`DELETE FROM terminal_presence WHERE workspace_id = $1 AND session_name = ANY($2)`, []any{f.workspaceID, []string{f.sharedSession, f.hiddenSession}}},
			{`DELETE FROM assignment_dispatch_receipt WHERE workspace_id = $1 AND local_agent_id = ANY($2)`, []any{f.workspaceID, []string{f.sharedAgentID, f.hiddenAgentID}}},
			{`DELETE FROM execution_receipt WHERE workspace_id = $1 AND task_id = ANY($2)`, []any{f.workspaceID, []string{f.sharedTask, f.hiddenTask}}},
			{`ALTER TABLE work_event DISABLE TRIGGER work_event_reject_mutation`, nil},
			{`DELETE FROM work_event WHERE workspace_id = $1 AND work_ref = ANY($2)`, []any{f.workspaceID, []string{f.sharedWorkRef, f.hiddenWorkRef}}},
			{`ALTER TABLE work_event ENABLE TRIGGER work_event_reject_mutation`, nil},
			{`DELETE FROM agent_task_queue WHERE id = ANY($1)`, []any{[]string{f.sharedTask, f.hiddenTask}}},
			{`DELETE FROM issue WHERE id = ANY($1)`, []any{[]string{f.sharedIssue, f.hiddenIssue}}},
			{`DELETE FROM agent_invocation_target WHERE agent_id = ANY($1)`, []any{[]string{f.sharedAgentID, f.hiddenAgentID}}},
			{`DELETE FROM agent WHERE id = ANY($1)`, []any{[]string{f.sharedAgentID, f.hiddenAgentID}}},
			{`DELETE FROM agent_runtime WHERE id = ANY($1)`, []any{[]string{f.sharedRuntime, f.hiddenRuntime}}},
			{`DELETE FROM member WHERE workspace_id = $1`, []any{f.workspaceID}},
			{`DELETE FROM workspace WHERE id = $1`, []any{f.workspaceID}},
			{`DELETE FROM "user" WHERE id = ANY($1)`, []any{[]string{f.ownerID, f.viewerID}}},
		} {
			if _, err := pool.Exec(cleanupCtx, step.stmt, step.args...); err != nil {
				t.Errorf("cleanup %s: %v", step.stmt, err)
			}
		}
	})
	return f
}

// newA2WorkWallE2EServer wires the real handler with the production
// workspace-member middleware over the isolated pool.
func newA2WorkWallE2EServer(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()
	queries := db.New(pool)
	h := handler.New(queries, pool, realtime.NewHub(), events.New(), service.NewEmailService(), nil, nil, analytics.NoopClient{}, handler.Config{AllowSignup: true})
	r := chi.NewRouter()
	r.Use(middleware.RequireWorkspaceMember(queries))
	r.Get("/api/work-wall/a2/snapshot", h.GetWorkWallA2Snapshot)
	r.Get("/api/work-wall/a2/stream", h.GetWorkWallA2Stream)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	return server
}

type a2E2EEnvelope struct {
	SchemaVersion string `json:"schema_version"`
	WorkspaceID   string `json:"workspace_id"`
	Cursor        string `json:"cursor"`
	EventLimit    int32  `json:"event_limit"`
	Panes         []struct {
		WorkRef       string `json:"work_ref"`
		EmployeeID    string `json:"employee_id"`
		EmployeeName  string `json:"employee_name"`
		SurfaceKind   string `json:"surface_kind"`
		ExecutionStat string `json:"execution_state"`
	} `json:"panes"`
}

func getA2E2ESnapshot(t *testing.T, ctx context.Context, server, userID, workspaceID string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/api/work-wall/a2/snapshot", nil)
	if err != nil {
		t.Fatalf("build snapshot request: %v", err)
	}
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("snapshot request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read snapshot body: %v", err)
	}
	return resp.StatusCode, body
}

// a2SSEFrame is one parsed server-sent event frame.
type a2SSEFrame struct {
	ID      string
	Event   string
	Data    string
	Comment string
	Retry   string
}

// openA2E2EStream connects an SSE client that forwards every parsed frame
// on the returned channel; the channel closes when the server closes the
// stream (EOF). The request context lets the caller abort the connection.
func openA2E2EStream(t *testing.T, server, userID, workspaceID string) (<-chan a2SSEFrame, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/api/work-wall/a2/stream", nil)
	if err != nil {
		cancel()
		t.Fatalf("build stream request: %v", err)
	}
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	req.Header.Set("Accept", "text/event-stream")
	frames := make(chan a2SSEFrame, 16)
	go func() {
		defer close(frames)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		reader := bufio.NewReader(resp.Body)
		for {
			var frame a2SSEFrame
			sawField := false
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				if line == "" {
					if sawField {
						frames <- frame
						sawField = false
						frame = a2SSEFrame{}
					}
					continue
				}
				sawField = true
				switch {
				case strings.HasPrefix(line, "id:"):
					frame.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
				case strings.HasPrefix(line, "event:"):
					frame.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				case strings.HasPrefix(line, "data:"):
					frame.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				case strings.HasPrefix(line, "retry:"):
					frame.Retry = strings.TrimSpace(strings.TrimPrefix(line, "retry:"))
				case strings.HasPrefix(line, ":"):
					frame.Comment = strings.TrimSpace(strings.TrimPrefix(line, ":"))
				}
			}
		}
	}()
	return frames, cancel
}

// nextA2E2EFrame reads the next frame or fails the test after the deadline.
func nextA2E2EFrame(t *testing.T, frames <-chan a2SSEFrame, what string) a2SSEFrame {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if !ok {
			t.Fatalf("stream closed while waiting for %s", what)
		}
		return frame
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return a2SSEFrame{}
	}
}

// waitForA2E2EClose asserts the stream delivers no further frame and the
// server closes the connection (channel close) within the deadline.
func waitForA2E2EClose(t *testing.T, frames <-chan a2SSEFrame) {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if ok {
			t.Fatalf("stream delivered another frame after the terminal error (%+v); want close", frame)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not close after the terminal error frame")
	}
}

func decodeA2E2EEnvelope(t *testing.T, data string) a2E2EEnvelope {
	t.Helper()
	var env a2E2EEnvelope
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", data, err)
	}
	return env
}

// TestWorkWallA2HandlerE2E covers the five required live-DB scenarios.
func TestWorkWallA2HandlerE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool := openA2WorkWallE2EPool(t, ctx)
	fixture := seedA2WorkWallE2EFixture(t, ctx, pool)
	server := newA2WorkWallE2EServer(t, pool)

	// 1. Snapshot happy path: the workspace owner sees both employee panes
	// with the fixed envelope shape.
	t.Run("snapshot happy path", func(t *testing.T) {
		status, body := getA2E2ESnapshot(t, ctx, server.URL, fixture.ownerID, fixture.workspaceID)
		if status != http.StatusOK {
			t.Fatalf("owner snapshot status = %d, body %s", status, body)
		}
		env := decodeA2E2EEnvelope(t, string(body))
		if env.SchemaVersion != "hivecrew.workwall.a2-snapshot.v1" {
			t.Fatalf("schema_version = %q", env.SchemaVersion)
		}
		if env.WorkspaceID != fixture.workspaceID {
			t.Fatalf("workspace_id = %q, want %q", env.WorkspaceID, fixture.workspaceID)
		}
		if len(env.Panes) != 2 {
			t.Fatalf("owner must see both panes, got %d (%s)", len(env.Panes), body)
		}
		byRef := map[string]string{}
		for _, p := range env.Panes {
			byRef[p.WorkRef] = p.EmployeeID
		}
		if byRef[fixture.sharedWorkRef] != fixture.sharedAgentID {
			t.Fatalf("shared pane employee = %q, want %q", byRef[fixture.sharedWorkRef], fixture.sharedAgentID)
		}
		if byRef[fixture.hiddenWorkRef] != fixture.hiddenAgentID {
			t.Fatalf("hidden pane employee = %q, want %q", byRef[fixture.hiddenWorkRef], fixture.hiddenAgentID)
		}
	})

	// 2. Hidden pane non-leak: the plain viewer resolves only the shared
	// employee; the private employee's pane leaves no trace in the response.
	t.Run("hidden pane non-leak", func(t *testing.T) {
		status, body := getA2E2ESnapshot(t, ctx, server.URL, fixture.viewerID, fixture.workspaceID)
		if status != http.StatusOK {
			t.Fatalf("viewer snapshot status = %d, body %s", status, body)
		}
		env := decodeA2E2EEnvelope(t, string(body))
		if len(env.Panes) != 1 {
			t.Fatalf("viewer must see exactly the shared pane, got %d (%s)", len(env.Panes), body)
		}
		if env.Panes[0].WorkRef != fixture.sharedWorkRef || env.Panes[0].EmployeeID != fixture.sharedAgentID {
			t.Fatalf("viewer pane = %+v, want the shared pane", env.Panes[0])
		}
		for _, forbidden := range []string{fixture.hiddenAgentID, fixture.hiddenWorkRef, "A2E2E employee hidden"} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("viewer snapshot leaked hidden pane trace %q: %s", forbidden, body)
			}
		}
	})

	// 3. SSE connect: fixed retry hint first, then the immediate full
	// snapshot frame carrying the cursor id.
	t.Run("sse initial retry and snapshot", func(t *testing.T) {
		frames, cancelStream := openA2E2EStream(t, server.URL, fixture.viewerID, fixture.workspaceID)
		defer cancelStream()
		retry := nextA2E2EFrame(t, frames, "retry hint")
		if retry.Retry != "5000" || retry.ID != "" || retry.Event != "" || retry.Data != "" || retry.Comment != "" {
			t.Fatalf("first frame must be the fixed retry: 5000 hint, got %+v", retry)
		}
		snapshot := nextA2E2EFrame(t, frames, "initial snapshot")
		if snapshot.Event != "snapshot" || snapshot.ID == "" {
			t.Fatalf("second frame must be the snapshot event with id, got %+v", snapshot)
		}
		env := decodeA2E2EEnvelope(t, snapshot.Data)
		if len(env.Panes) != 1 || env.Panes[0].WorkRef != fixture.sharedWorkRef {
			t.Fatalf("initial snapshot must carry the shared pane, got %s", snapshot.Data)
		}
		if env.Cursor != snapshot.ID {
			t.Fatalf("snapshot id %q must equal envelope cursor %q", snapshot.ID, env.Cursor)
		}
	})

	// 4. Visibility revocation mid-stream (the R1 fix): the shared employee
	// becomes private; the pane still projects, so the stream must emit the
	// redacted workwall_a2_access_revoked error and close — never a shrunken
	// snapshot and never the hidden pane's content.
	t.Run("visibility revoke closes stream", func(t *testing.T) {
		frames, cancelStream := openA2E2EStream(t, server.URL, fixture.viewerID, fixture.workspaceID)
		defer cancelStream()
		retry := nextA2E2EFrame(t, frames, "retry hint")
		if retry.Retry != "5000" {
			t.Fatalf("first frame = %+v, want retry: 5000", retry)
		}
		snapshot := nextA2E2EFrame(t, frames, "initial snapshot")
		if snapshot.Event != "snapshot" || len(decodeA2E2EEnvelope(t, snapshot.Data).Panes) != 1 {
			t.Fatalf("initial snapshot = %+v, want the visible shared pane", snapshot)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE agent SET permission_mode = 'private' WHERE id = $1 AND workspace_id = $2`,
			fixture.sharedAgentID, fixture.workspaceID); err != nil {
			t.Fatalf("revoke shared agent visibility: %v", err)
		}
		terminal := nextA2E2EFrame(t, frames, "access-revoked error")
		if terminal.Event != "error" || terminal.Data != `{"error":"workwall_a2_access_revoked"}` {
			t.Fatalf("terminal frame = %+v, want fixed redacted access-revoked code", terminal)
		}
		waitForA2E2EClose(t, frames)
		// Restore visibility for the membership scenario below.
		if _, err := pool.Exec(ctx,
			`UPDATE agent SET permission_mode = 'public_to' WHERE id = $1 AND workspace_id = $2`,
			fixture.sharedAgentID, fixture.workspaceID); err != nil {
			t.Fatalf("restore shared agent visibility: %v", err)
		}
	})

	// 5. Membership revocation mid-stream: the viewer's member row is
	// removed; the stream must emit workwall_a2_membership_revoked and close.
	t.Run("membership revoke closes stream", func(t *testing.T) {
		frames, cancelStream := openA2E2EStream(t, server.URL, fixture.viewerID, fixture.workspaceID)
		defer cancelStream()
		retry := nextA2E2EFrame(t, frames, "retry hint")
		if retry.Retry != "5000" {
			t.Fatalf("first frame = %+v, want retry: 5000", retry)
		}
		snapshot := nextA2E2EFrame(t, frames, "initial snapshot")
		if snapshot.Event != "snapshot" {
			t.Fatalf("initial frame = %+v, want snapshot event", snapshot)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`,
			fixture.workspaceID, fixture.viewerID); err != nil {
			t.Fatalf("revoke viewer membership: %v", err)
		}
		terminal := nextA2E2EFrame(t, frames, "membership-revoked error")
		if terminal.Event != "error" || terminal.Data != `{"error":"workwall_a2_membership_revoked"}` {
			t.Fatalf("terminal frame = %+v, want fixed redacted membership-revoked code", terminal)
		}
		waitForA2E2EClose(t, frames)
	})
}
