package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestMachineTitleForBases(t *testing.T) {
	tests := map[string]string{
		"HiveCosm Mac mini · 2.1.221 (Claude Code)": "HiveCosm Mac mini",
		"HiveCrew MBP M5X · 1.0.0":                   "HiveCrew MBP M5X",
		"":                          "unknown",
		"   ":                       "unknown",
		"plain-host":                "plain-host",
	}
	for input, want := range tests {
		if got := machineTitleForBases(input); got != want {
			t.Fatalf("machineTitleForBases(%q) = %q, want %q", input, got, want)
		}
	}
}

func baseRuntime(id, workspace, name, status, deviceInfo, daemonID string) db.AgentRuntime {
	runtime := db.AgentRuntime{
		ID:          handlerUUID(id),
		WorkspaceID: handlerUUID(workspace),
		Name:        name,
		Status:      status,
		RuntimeMode: "local",
		DeviceInfo:  deviceInfo,
	}
	if daemonID != "" {
		runtime.DaemonID = pgtype.Text{String: daemonID, Valid: true}
	}
	return runtime
}

func baseAgent(id, workspace, runtimeID, status string) db.Agent {
	return db.Agent{
		ID:          handlerUUID(id),
		WorkspaceID: handlerUUID(workspace),
		Name:        "agent-" + id,
		Kind:        "user",
		Status:      status,
		RuntimeID:   handlerUUID(runtimeID),
		RuntimeMode: "local",
	}
}

func TestBuildBaseOverviewsGroupsByMachine(t *testing.T) {
	workspace := "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	runtimes := []db.AgentRuntime{
		baseRuntime("22222222-3333-4444-8555-666666666666", workspace, "mini-a", "online", "HiveCosm Mac mini · 2.1.221", "daemon-mini"),
		baseRuntime("33333333-4444-4555-8666-777777777777", workspace, "mini-b", "offline", "HiveCosm Mac mini · 2.1.221", "daemon-mini"),
		baseRuntime("44444444-5555-4666-8777-888888888888", workspace, "mbp", "online", "HiveCrew MBP M5X · 1.0.0", "daemon-mbp"),
	}
	agents := []db.Agent{
		baseAgent("11111111-2222-4333-8444-555555555555", workspace, "22222222-3333-4444-8555-666666666666", "working"),
		baseAgent("55555555-6666-4777-8888-999999999999", workspace, "44444444-5555-4666-8777-888888888888", "idle"),
	}

	bases := buildBaseOverviews(runtimes, agents)
	if len(bases) != 2 {
		t.Fatalf("bases = %d, want 2", len(bases))
	}

	mini := bases[0]
	if mini.MachineTitle != "HiveCosm Mac mini" ||
		mini.RuntimeRegistered != 2 ||
		mini.RuntimeOnline != 1 ||
		mini.RuntimeOffline != 1 ||
		mini.DaemonCount != 1 ||
		mini.Employees != 1 ||
		mini.LoadRunning != 1 {
		t.Fatalf("unexpected mini base: %#v", mini)
	}

	mbp := bases[1]
	if mbp.MachineTitle != "HiveCrew MBP M5X" ||
		mbp.RuntimeRegistered != 1 ||
		mbp.RuntimeOnline != 1 ||
		mbp.Employees != 1 ||
		mbp.LoadRunning != 0 {
		t.Fatalf("unexpected mbp base: %#v", mbp)
	}
}

func TestValidateRuntimeMigration(t *testing.T) {
	workspace := "aaaaaaaa-bbbb-4ccc-9ddd-eeeeeeeeeeee"
	other := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	faulted := baseRuntime("22222222-3333-4444-8555-666666666666", workspace, "faulted", "offline", "HiveCosm Mac mini", "")
	healthy := baseRuntime("33333333-4444-4555-8666-777777777777", workspace, "healthy", "online", "HiveCrew MBP M5X", "")
	onlineSource := baseRuntime("44444444-5555-4666-8777-888888888888", workspace, "online", "online", "X", "")
	crossWorkspace := baseRuntime("55555555-6666-4777-8888-999999999999", other, "other", "online", "X", "")
	offlineTarget := baseRuntime("66666666-7777-4888-8999-aaaaaaaaaaaa", workspace, "offline-target", "offline", "X", "")

	if err := validateRuntimeMigration(faulted, healthy); err != nil {
		t.Fatalf("valid migration rejected: %v", err)
	}
	if err := validateRuntimeMigration(faulted, faulted); err == nil {
		t.Fatal("same runtime migration must fail")
	}
	if err := validateRuntimeMigration(onlineSource, healthy); err == nil {
		t.Fatal("online source migration must fail")
	}
	if err := validateRuntimeMigration(faulted, offlineTarget); err == nil {
		t.Fatal("offline target migration must fail")
	}
	if err := validateRuntimeMigration(faulted, crossWorkspace); err == nil {
		t.Fatal("cross-workspace migration must fail")
	}
}
