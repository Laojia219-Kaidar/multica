package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestReadDaemonHealth_Running(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q, want /health", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status":            "running",
			"daemon_id":         "d-test",
			"cli_version":       "9.9.9",
			"active_task_count": 2,
			"workspaces": []map[string]any{
				{"id": "ws-1", "runtimes": []string{"rt-1"}},
			},
		})
	}))
	defer ts.Close()

	// Extract port from test server URL.
	port := extractPort(t, ts.URL)
	snap := readDaemonHealth(t.Context(), port)

	if snap.Status != "running" {
		t.Errorf("status = %q, want running", snap.Status)
	}
	if snap.DaemonID != "d-test" {
		t.Errorf("daemon_id = %q, want d-test", snap.DaemonID)
	}
	if snap.ActiveTaskCount != 2 {
		t.Errorf("active_count = %d, want 2", snap.ActiveTaskCount)
	}
	if len(snap.Workspaces) != 1 || len(snap.Workspaces[0].Runtimes) != 1 {
		t.Fatalf("unexpected workspaces: %+v", snap.Workspaces)
	}
}

func TestReadDaemonHealth_Unreachable(t *testing.T) {
	// Port 1 is almost certainly not listening.
	snap := readDaemonHealth(t.Context(), 1)
	if snap.Status != "" {
		t.Errorf("status = %q, want empty (unreachable)", snap.Status)
	}
}

func TestWIPTruthEndToEnd_LocalAuthPath(t *testing.T) {
	// Mock daemon health endpoint.
	daemonTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(cli.DaemonHealthSnapshot{
			Status:          "running",
			DaemonID:        "d-e2e",
			CLIVersion:      "1.0.0",
			ActiveTaskCount: 2,
			Workspaces: []struct {
				ID       string   `json:"id"`
				Runtimes []string `json:"runtimes"`
			}{
				{ID: "ws-1", Runtimes: []string{"rt-1"}},
			},
		})
	}))
	defer daemonTS.Close()

	// Mock server pending tasks endpoint.
	serverTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/daemon/runtimes/rt-1/tasks/pending" {
			json.NewEncoder(w).Encode([]cli.ServerPendingTask{
				{ID: "t1", Status: "dispatched", RuntimeID: "rt-1"},
				{ID: "t2", Status: "running", RuntimeID: "rt-1"},
				{ID: "t3", Status: "queued", RuntimeID: "rt-1"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer serverTS.Close()

	// Read daemon health.
	daemonPort := extractPort(t, daemonTS.URL)
	snap := readDaemonHealth(t.Context(), daemonPort)

	// Query server (using the CLI APIClient directly to test the auth path).
	client := cli.NewAPIClient(serverTS.URL, "ws-1", "test-token")
	var tasks []cli.ServerPendingTask
	if err := client.GetJSON(t.Context(), "/api/daemon/runtimes/rt-1/tasks/pending", &tasks); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}

	// Compute report.
	report := cli.ComputeWIPTruth(snap, tasks, time.Now())

	if report.Daemon.Status != "running" {
		t.Errorf("status = %q, want running", report.Daemon.Status)
	}
	if report.Server.Queued != 1 {
		t.Errorf("queued = %d, want 1", report.Server.Queued)
	}
	// The legacy ServerPendingTask input carries no agent_id, so the R5-R8
	// fail-closed engine reports every active task (dispatched/running) as
	// UNKNOWN rather than fabricating claimed/running counts.
	if report.Server.Claimed != 0 || report.Server.Running != 0 {
		t.Errorf("claimed = %d, running = %d, want 0/0 (fail-closed legacy input)", report.Server.Claimed, report.Server.Running)
	}
	for _, want := range []string{"MISSING_AGENT_ID", "AGENT_PROJECTION_ABSENT"} {
		found := false
		for _, got := range report.UnknownReasons {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("unknown_reasons = %v, want to contain %s", report.UnknownReasons, want)
		}
	}
}

// extractPort parses the port from a httptest.URL like "http://127.0.0.1:PORT".
func extractPort(t *testing.T, rawURL string) int {
	t.Helper()
	// rawURL is "http://127.0.0.1:NNNNN"
	var port int
	if _, err := fmt.Sscanf(rawURL, "http://127.0.0.1:%d", &port); err != nil {
		t.Fatalf("extractPort(%q): %v", rawURL, err)
	}
	return port
}
