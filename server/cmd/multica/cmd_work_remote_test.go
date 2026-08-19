package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/workentry"
)

func newWorkRemoteTestCmd(serverURL, workspaceID string) *cobra.Command {
	cmd := &cobra.Command{Use: "work-test"}
	cmd.Flags().String("state", "", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("request", "", "")
	cmd.Flags().Bool("request-stdin", false, "")
	cmd.Flags().String("request-file", "", "")
	cmd.Flags().Bool("allow-external-file", false, "")
	cmd.Flags().Bool("confirm-create", false, "")
	cmd.Flags().String("session-id", "", "")
	cmd.Flags().String("run-id", "", "")
	cmd.Flags().String("actor-id", "", "")
	cmd.Flags().String("host", "", "")
	cmd.Flags().String("session-name", "", "")
	cmd.Flags().Int("window-index", 0, "")
	cmd.Flags().Int("pane-index", 0, "")
	cmd.Flags().String("current-command", "", "")
	cmd.Flags().String("agent-hint", "", "")
	cmd.Flags().String("inbox-id", "", "")
	cmd.Flags().String("project-id", "", "")
	cmd.Flags().String("issue-id", "", "")
	cmd.Flags().String("reason", "", "")
	cmd.Flags().String("idempotency-key", "", "")
	cmd.Flags().String("kind", "receipt", "")
	cmd.Flags().String("work-ref", "", "")
	_ = cmd.Flags().Set("server-url", serverURL)
	_ = cmd.Flags().Set("workspace-id", workspaceID)
	return cmd
}

func TestRemainingWorkVerbsUseLiveAPI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		seen[key]++
		switch key {
		case "POST /api/work/heartbeat":
			_ = json.NewEncoder(w).Encode(workentry.HeartbeatResult{Accepted: true})
		case "POST /api/work/event":
			var event workentry.WorkEventV1
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Fatalf("decode event request: %v", err)
			}
			if event.RunID != "run-1" {
				t.Fatalf("event run_id = %q, want run-1", event.RunID)
			}
			_ = json.NewEncoder(w).Encode(workentry.EventResult{EventID: "event-1"})
		case "POST /api/work/handoff":
			_ = json.NewEncoder(w).Encode(workentry.HandoffResult{HandoffID: "handoff-1"})
		case "POST /api/work/finish":
			_ = json.NewEncoder(w).Encode(workentry.CompletionResult{ReviewRouted: true})
		case "GET /api/work/reconcile":
			_ = json.NewEncoder(w).Encode([]workentry.InboxItem{})
		case "POST /api/work/attach":
			_ = json.NewEncoder(w).Encode(workentry.AttachResult{Linked: true})
		case "POST /api/work/ignore":
			_ = json.NewEncoder(w).Encode(workentry.IgnoreResult{Ignored: true})
		case "GET /api/work/replay":
			if r.URL.Query().Get("key") != "replay-key" || r.URL.Query().Get("kind") != "event" {
				t.Fatalf("replay query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(workentry.ReplayResult{})
		default:
			t.Fatalf("unexpected request %s", key)
		}
	}))
	defer srv.Close()

	workRef := "hivecrew://ws-1/work/project-1/issue-1"
	heartbeatCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = heartbeatCmd.Flags().Set("actor-id", "codex-primary")
	_ = heartbeatCmd.Flags().Set("session-id", "session-1")
	if _, err := captureRuntimeStdout(t, func() error { return runWorkHeartbeat(heartbeatCmd, nil) }); err != nil {
		t.Fatalf("runWorkHeartbeat: %v", err)
	}

	event := workentry.WorkEventV1{
		WorkRef: workRef, SessionID: "session-1", RunID: "run-1", EventType: workentry.EventProgress,
		EventPayload: map[string]any{"step": "verify"}, IdempotencyKey: "event-1",
		OccurredAt: "2026-08-19T04:00:00Z", ObservedAt: "2026-08-19T04:00:00Z",
	}
	eventBody, _ := json.Marshal(event)
	eventCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = eventCmd.Flags().Set("request", string(eventBody))
	if _, err := captureRuntimeStdout(t, func() error { return runWorkEvent(eventCmd, nil) }); err != nil {
		t.Fatalf("runWorkEvent: %v", err)
	}

	handoff := workentry.WorkHandoffV1{WorkRef: workRef, Revision: "revision-1"}
	handoffBody, _ := json.Marshal(handoff)
	handoffCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = handoffCmd.Flags().Set("request", string(handoffBody))
	if _, err := captureRuntimeStdout(t, func() error { return runWorkHandoff(handoffCmd, nil) }); err != nil {
		t.Fatalf("runWorkHandoff: %v", err)
	}

	completion := workentry.WorkCompletionV1{
		WorkRef: workRef,
		CompletionCandidate: workentry.CompletionCandidate{
			ArtifactRef: "artifact://candidate/1", Digest: "sha256:abcd", Revision: "revision-1",
		},
		ProjectLifecycleConsequence: workentry.LifecycleContinue,
	}
	completionBody, _ := json.Marshal(completion)
	finishCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = finishCmd.Flags().Set("request", string(completionBody))
	if _, err := captureRuntimeStdout(t, func() error { return runWorkFinish(finishCmd, nil) }); err != nil {
		t.Fatalf("runWorkFinish: %v", err)
	}

	doctorCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	if _, err := captureRuntimeStdout(t, func() error { return runWorkDoctor(doctorCmd, nil) }); err != nil {
		t.Fatalf("runWorkDoctor: %v", err)
	}

	attachCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = attachCmd.Flags().Set("inbox-id", "inbox-1")
	_ = attachCmd.Flags().Set("issue-id", "issue-1")
	if _, err := captureRuntimeStdout(t, func() error { return runWorkDoctorAttach(attachCmd, nil) }); err != nil {
		t.Fatalf("runWorkDoctorAttach: %v", err)
	}

	ignoreCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = ignoreCmd.Flags().Set("inbox-id", "inbox-1")
	_ = ignoreCmd.Flags().Set("reason", "classified elsewhere")
	if _, err := captureRuntimeStdout(t, func() error { return runWorkDoctorIgnore(ignoreCmd, nil) }); err != nil {
		t.Fatalf("runWorkDoctorIgnore: %v", err)
	}

	replayCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = replayCmd.Flags().Set("idempotency-key", "replay-key")
	_ = replayCmd.Flags().Set("kind", "event")
	_ = replayCmd.Flags().Set("work-ref", workRef)
	if _, err := captureRuntimeStdout(t, func() error { return runWorkReplay(replayCmd, nil) }); err != nil {
		t.Fatalf("runWorkReplay: %v", err)
	}

	for _, endpoint := range []string{
		"POST /api/work/heartbeat", "POST /api/work/event", "POST /api/work/handoff",
		"POST /api/work/finish", "GET /api/work/reconcile", "POST /api/work/attach",
		"POST /api/work/ignore", "GET /api/work/replay",
	} {
		if seen[endpoint] != 1 {
			t.Fatalf("%s requests = %d, want 1", endpoint, seen[endpoint])
		}
	}
}

func TestRunWorkStartOmitsServerOwnedRunIDForLiveAPI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/work/start" {
			t.Fatalf("request = %s %s, want POST /api/work/start", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode start request: %v", err)
		}
		if _, exists := body["run_id"]; exists {
			t.Fatalf("live start request must omit server-owned run_id: %#v", body)
		}
		if body["workspace_id"] != "ws-1" || body["actor_id"] != "codex-primary" {
			t.Fatalf("start request = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(workentry.EventResult{EventID: "event-1", Sequence: 1})
	}))
	defer srv.Close()

	cmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = cmd.Flags().Set("session-id", "session-1")
	_ = cmd.Flags().Set("actor-id", "codex-primary")
	if _, err := captureRuntimeStdout(t, func() error {
		return runWorkStart(cmd, []string{"hivecrew://ws-1/work/project-1/issue-1"})
	}); err != nil {
		t.Fatalf("runWorkStart: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}

	_ = cmd.Flags().Set("run-id", "caller-forged-run")
	if err := runWorkStart(cmd, []string{"hivecrew://ws-1/work/project-1/issue-1"}); err == nil {
		t.Fatal("live start with caller-supplied run_id should fail closed")
	}
	if requests != 1 {
		t.Fatalf("rejected live start made %d requests, want 1 total", requests)
	}
}

func TestRunWorkRegisterAndStatusUseLiveAPI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	var registerRequests, statusRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/work/register":
			registerRequests++
			var req workentry.RegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode register request: %v", err)
			}
			if req.Actor.WorkspaceID != "ws-1" || !req.ConfirmCreate {
				t.Fatalf("register request = %#v", req)
			}
			_ = json.NewEncoder(w).Encode(workentry.WorkRegistrationReceiptV1{
				WorkRef:            "hivecrew://ws-1/work/project-1/issue-1",
				ResolutionDecision: workentry.DecisionContinued,
				Continued:          true,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/work/status":
			statusRequests++
			if got := r.URL.Query().Get("work_ref"); got != "hivecrew://ws-1/work/project-1/issue-1" {
				t.Fatalf("status work_ref = %q", got)
			}
			_ = json.NewEncoder(w).Encode(workentry.StatusResult{
				WorkRef: "hivecrew://ws-1/work/project-1/issue-1",
				Found:   true,
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	resolveReq := validWorkResolveRequest()
	registerReq := workentry.RegisterRequest{ResolveRequest: resolveReq}
	body, err := json.Marshal(registerReq)
	if err != nil {
		t.Fatalf("marshal register request: %v", err)
	}
	registerCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = registerCmd.Flags().Set("request", string(body))
	_ = registerCmd.Flags().Set("confirm-create", "true")
	if _, err := captureRuntimeStdout(t, func() error { return runWorkRegister(registerCmd, nil) }); err != nil {
		t.Fatalf("runWorkRegister: %v", err)
	}

	statusCmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	if _, err := captureRuntimeStdout(t, func() error {
		return runWorkStatus(statusCmd, []string{"hivecrew://ws-1/work/project-1/issue-1"})
	}); err != nil {
		t.Fatalf("runWorkStatus: %v", err)
	}

	if registerRequests != 1 || statusRequests != 1 {
		t.Fatalf("register/status requests = %d/%d, want 1/1", registerRequests, statusRequests)
	}
}

func TestRunWorkSyncWrapsEntriesForLiveAPI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/work/sync" {
			t.Fatalf("request = %s %s, want POST /api/work/sync", r.Method, r.URL.Path)
		}
		var body struct {
			Entries []workentry.SyncEntry `json:"entries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode sync request: %v", err)
		}
		if body.Entries == nil || len(body.Entries) != 0 {
			t.Fatalf("entries = %#v, want present empty array", body.Entries)
		}
		_ = json.NewEncoder(w).Encode(workentry.SyncResult{})
	}))
	defer srv.Close()

	cmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = cmd.Flags().Set("request", "[]")
	if _, err := captureRuntimeStdout(t, func() error { return runWorkSync(cmd, nil) }); err != nil {
		t.Fatalf("runWorkSync: %v", err)
	}
}

func validWorkResolveRequest() workentry.ResolveRequest {
	return workentry.ResolveRequest{
		Actor: workentry.WorkActorIdentityV1{
			ActorType:   workentry.ActorExternalAgent,
			ActorID:     "codex-primary",
			CarrierID:   "Codex",
			SessionID:   "session-1",
			ObservedAt:  "2026-08-19T00:00:00Z",
			WorkspaceID: "",
		},
		Intent: workentry.WorkIntentV1{
			OwnerIntent:             "Continue HiveCrew development",
			GoalRef:                 "HIVECREW-OWNER-OPERATING-WORKBENCH-V1",
			Objective:               "Connect the CLI work entry to the live API",
			ExpectedHumanResult:     "One formal idempotent work_ref",
			Repo:                    "repo://hivecrew",
			BaselineRevision:        "4ab2c72c",
			BranchOrWorktree:        "owner/william/ultra/work-entry-live",
			ReadScope:               []string{"work entry API"},
			WriteScope:              []string{"CLI work client"},
			ExpectedOutcomes:        []string{"formal work_ref"},
			CandidateFormalBoundary: workentry.BoundaryCandidate,
		},
	}
}

func TestRunWorkResolveUsesLiveAPIWhenStateIsOmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/work/resolve" {
			t.Fatalf("request = %s %s, want POST /api/work/resolve", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "ws-1" {
			t.Fatalf("X-Workspace-ID = %q, want ws-1", got)
		}
		var req workentry.ResolveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Actor.WorkspaceID != "ws-1" {
			t.Fatalf("body workspace_id = %q, want ws-1", req.Actor.WorkspaceID)
		}
		_ = json.NewEncoder(w).Encode(workentry.ResolveResult{
			ResolutionDecision: workentry.DecisionContinued,
			DedupeKey:          "dedupe-1",
			DedupeDigest:       "digest-1",
			Matches: []workentry.Match{{
				Kind:    workentry.MatchWorkOrder,
				WorkRef: "hivecrew://ws-1/work/project-1/issue-1",
			}},
		})
	}))
	defer srv.Close()

	req := validWorkResolveRequest()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	cmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = cmd.Flags().Set("request", string(body))

	out, err := captureRuntimeStdout(t, func() error { return runWorkResolve(cmd, nil) })
	if err != nil {
		t.Fatalf("runWorkResolve: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	var got workentry.ResolveResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got.ResolutionDecision != workentry.DecisionContinued || len(got.Matches) != 1 {
		t.Fatalf("output = %#v", got)
	}
}

func TestRunWorkResolveKeepsExplicitStateOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "unexpected remote request", http.StatusInternalServerError)
	}))
	defer srv.Close()

	req := validWorkResolveRequest()
	req.Actor.WorkspaceID = "ws-1"
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	cmd := newWorkRemoteTestCmd(srv.URL, "ws-1")
	_ = cmd.Flags().Set("request", string(body))
	_ = cmd.Flags().Set("state", filepath.Join(t.TempDir(), "work-state.json"))

	if _, err := captureRuntimeStdout(t, func() error { return runWorkResolve(cmd, nil) }); err != nil {
		t.Fatalf("offline runWorkResolve: %v", err)
	}
	if requests != 0 {
		t.Fatalf("remote requests = %d, want 0", requests)
	}
}
