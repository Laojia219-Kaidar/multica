package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newBaseTestCmd(serverURL string, operationalMode bool) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("output", "json", "")
	if operationalMode {
		cmd.Flags().String("machine-title", "", "")
		cmd.Flags().String("mode", "", "")
	}
	_ = cmd.Flags().Set("server-url", serverURL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	return cmd
}

func TestRunBaseListUsesWorkspaceAndAuthenticatedClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/bases" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "ws-1" {
			t.Fatalf("X-Workspace-ID = %q, want ws-1", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q, want bearer test token", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"machine_title":      "Mac Ultra",
			"runtime_online":     6,
			"runtime_registered": 6,
			"employees":          7,
			"drained":            true,
		}})
	}))
	defer srv.Close()

	cmd := newBaseTestCmd(srv.URL, false)
	out, err := captureRuntimeStdout(t, func() error { return runBaseList(cmd, nil) })
	if err != nil {
		t.Fatalf("runBaseList: %v", err)
	}
	if !strings.Contains(out, `"machine_title": "Mac Ultra"`) {
		t.Fatalf("stdout = %q, want base JSON", out)
	}
}

func TestRunBaseOperationalModePostsExactMachineAndMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/bases" {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"machine_title": "HiveCrew Mac Studio Ultra Primary"}})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/bases/operational-mode" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			MachineTitle string `json:"machine_title"`
			Mode         string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.MachineTitle != "HiveCrew Mac Studio Ultra Primary" || body.Mode != "active" {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"machine_title":  body.MachineTitle,
			"mode":           body.Mode,
			"agents_updated": 7,
		})
	}))
	defer srv.Close()

	cmd := newBaseTestCmd(srv.URL, true)
	_ = cmd.Flags().Set("machine-title", "HiveCrew Mac Studio Ultra Primary")
	_ = cmd.Flags().Set("mode", "active")
	out, err := captureRuntimeStdout(t, func() error { return runBaseOperationalMode(cmd, nil) })
	if err != nil {
		t.Fatalf("runBaseOperationalMode: %v", err)
	}
	if !strings.Contains(out, `"agents_updated": 7`) {
		t.Fatalf("stdout = %q, want mutation result", out)
	}
}

func TestRunBaseOperationalModeRejectsUnknownMachineBeforeMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	postCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/bases" {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"machine_title": "Mac Ultra"}})
			return
		}
		postCount++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cmd := newBaseTestCmd(srv.URL, true)
	_ = cmd.Flags().Set("machine-title", "Mac Ulrta")
	_ = cmd.Flags().Set("mode", "active")
	err := runBaseOperationalMode(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want not found", err)
	}
	if postCount != 0 {
		t.Fatalf("postCount = %d, want 0", postCount)
	}
}

func TestRunBaseOperationalModeRejectsInvalidModeBeforeRequest(t *testing.T) {
	cmd := newBaseTestCmd("http://127.0.0.1:1", true)
	_ = cmd.Flags().Set("machine-title", "Mac Ultra")
	_ = cmd.Flags().Set("mode", "disabled")
	if err := runBaseOperationalMode(cmd, nil); err == nil || !strings.Contains(err.Error(), "active or resting") {
		t.Fatalf("error = %v, want mode validation", err)
	}
}
