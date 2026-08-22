package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newInventoryTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "inventory"}
	addCommonProfileFlags(cmd)
	cmd.Flags().String("output", "table", "")
	return cmd
}

// runInventoryAgainst executes runRuntimeInventory against a test server and
// captures stdout.
func runInventoryAgainst(t *testing.T, handler http.HandlerFunc, args []string, output string) (string, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// The daemon-context guard accepts any mat_-prefixed token; env wins
	// over the flag in resolveToken, so pin it for a deterministic actor.
	t.Setenv("MULTICA_TOKEN", "mat_cli_test_token")

	cmd := newInventoryTestCmd()
	cmd.Flags().Set("server-url", server.URL)
	cmd.Flags().Set("workspace-id", "11111111-2222-4333-8444-555555555555")
	cmd.Flags().Set("output", output)

	var printed strings.Builder
	origOut := runtimeInventoryOut
	runtimeInventoryOut = &printed
	t.Cleanup(func() { runtimeInventoryOut = origOut })

	err := runRuntimeInventory(cmd, args)
	return printed.String(), err
}

func inventoryFixture() string {
	return `{
		"count": 2,
		"employees": [
			{
				"employee": {"employee_id": "9f0a0a00-0000-4000-8000-0000000000a1", "name": "Kai｜后端与全栈工程师"},
				"agent": {"state": "ok", "id": "9f0a0a00-0000-4000-8000-0000000000a1", "name": "Kai｜后端与全栈工程师", "runtime_mode": "local", "status": "idle"},
				"runtime": {"state": "ok", "id": "9f0a0a00-0000-4000-8000-0000000000a2", "daemon_id": "daemon-1", "name": "Mac Studio runtime", "status": "online"},
				"profile": {"state": "ok", "id": "9f0a0a00-0000-4000-8000-0000000000a3", "display_name": "In-house wrapper", "protocol_family": "codex", "enabled": true},
				"provider": "codex",
				"model": "glm-5.3",
				"registration": {"state": "online"}
			},
			{
				"employee": {"employee_id": "9f0a0a00-0000-4000-8000-0000000000b1", "name": "Ghost"},
				"agent": {"state": "missing_agent"},
				"runtime": {"state": "unknown"},
				"profile": {"state": "unknown"},
				"provider": "",
				"model": "",
				"registration": {"state": "unknown"}
			}
		]
	}`
}

func TestRuntimeInventoryCommandRegistered(t *testing.T) {
	cmd, _, err := runtimeCmd.Find([]string{"inventory"})
	if err != nil {
		t.Fatalf("find inventory: %v", err)
	}
	if cmd.Short == "" {
		t.Fatal("inventory command lacks a short description")
	}
	if cmd.Flags().Lookup("output") == nil {
		t.Fatal("inventory command lacks --output flag")
	}
	if cmd.Args == nil {
		t.Fatal("inventory command must validate argument count")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Fatal("expected error for two positional args")
	}
}

func TestRuntimeInventoryJSONOutput(t *testing.T) {
	var gotPath, gotWorkspace, gotAuth string
	out, err := runInventoryAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotWorkspace = r.Header.Get("X-Workspace-ID")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(inventoryFixture()))
	}, nil, "json")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotPath != "/api/runtimes/inventory" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotWorkspace != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("workspace header = %q", gotWorkspace)
	}
	if gotAuth != "Bearer mat_cli_test_token" {
		t.Fatalf("authorization header = %q", gotAuth)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	first := rows[0]
	if first["provider"] != "codex" || first["model"] != "glm-5.3" {
		t.Fatalf("provider/model = %v/%v", first["provider"], first["model"])
	}
	registration, _ := first["registration"].(map[string]any)
	if registration["state"] != "online" {
		t.Fatalf("registration = %v", registration)
	}
}

func TestRuntimeInventoryTableOutput(t *testing.T) {
	out, err := runInventoryAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(inventoryFixture()))
	}, nil, "table")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, header := range []string{"EMPLOYEE", "AGENT", "RUNTIME", "PROFILE", "PROVIDER", "MODEL", "REGISTRATION"} {
		if !strings.Contains(out, header) {
			t.Fatalf("table lacks %s header:\n%s", header, out)
		}
	}
	if !strings.Contains(out, "Kai｜后端与全栈工程师") {
		t.Fatalf("table lacks employee name:\n%s", out)
	}
	if !strings.Contains(out, "Mac Studio runtime") {
		t.Fatalf("table lacks runtime name:\n%s", out)
	}
	if !strings.Contains(out, "In-house wrapper") {
		t.Fatalf("table lacks profile name:\n%s", out)
	}
	if !strings.Contains(out, "missing_agent") {
		t.Fatalf("table lacks missing_agent state:\n%s", out)
	}
}

func TestRuntimeInventoryTableShowsSanitizedReason(t *testing.T) {
	fixture := `{
		"count": 1,
		"employees": [
			{
				"employee": {"employee_id": "9f0a0a00-0000-4000-8000-0000000000a1", "name": "Broken"},
				"agent": {"state": "ok", "id": "9f0a0a00-0000-4000-8000-0000000000a1", "name": "Broken", "runtime_mode": "local", "status": "idle"},
				"runtime": {"state": "ok", "id": "9f0a0a00-0000-4000-8000-0000000000a2", "daemon_id": "daemon-1", "name": "Mac Studio runtime", "status": "offline"},
				"profile": {"state": "missing_profile"},
				"provider": "codex",
				"model": "",
				"registration": {"state": "registration_error", "reason": "runtime_profile_registration_error"}
			}
		]
	}`
	out, err := runInventoryAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture))
	}, nil, "table")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "registration_error: runtime_profile_registration_error") {
		t.Fatalf("table lacks safe reason code:\n%s", out)
	}
	if !strings.Contains(out, "missing_profile") {
		t.Fatalf("table lacks missing_profile:\n%s", out)
	}
}

func TestRuntimeInventoryEmployeeReferenceIsEscaped(t *testing.T) {
	var gotQuery string
	_, err := runInventoryAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("employee")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(inventoryFixture()))
	}, []string{"Kai｜后端与全栈工程师"}, "json")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotQuery != "Kai｜后端与全栈工程师" {
		t.Fatalf("employee query = %q", gotQuery)
	}
}

func TestRuntimeInventoryEmployeeNotFoundFails(t *testing.T) {
	_, err := runInventoryAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error": "employee not found"}`))
	}, []string{"Nobody"}, "json")
	if err == nil {
		t.Fatal("expected non-zero exit for unknown employee")
	}
	if !strings.Contains(err.Error(), "Nobody") {
		t.Fatalf("error should name the employee: %v", err)
	}
}

func TestRuntimeInventoryServerErrorsFail(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		_, err := runInventoryAgainst(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}, nil, "json")
		if err == nil {
			t.Fatalf("expected error for status %d", status)
		}
	}
}

func TestRuntimeInventoryRejectsBadOutputFlag(t *testing.T) {
	_, err := runInventoryAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server must not be called for an invalid --output value")
	}, nil, "yaml")
	if err == nil || !strings.Contains(err.Error(), "--output") {
		t.Fatalf("expected --output validation error, got %v", err)
	}
}
