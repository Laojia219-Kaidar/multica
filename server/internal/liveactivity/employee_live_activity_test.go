package liveactivity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildDTO_AllowsOnlySafeFields(t *testing.T) {
	now := time.Now().UTC()
	tokens := int64(12345)
	cost := 0.42

	in := SnapshotInput{
		WorkspaceID: "ws-1",
		EmployeeID:  "EMP-01",
		AgentID:     "AGT-01",
		DisplayName: "Emory",
		AvatarURL:   "https://cdn/emory.png",
		Derivation: Inputs{
			RuntimeOnline:  true,
			HasOpenTask:    true,
			RunStarted:     true,
			RunExecuting:   true,
			HeartbeatFresh: true,
		},
		ActivityKind:  "test.result",
		ActivityNotes: "32 passed, 1 failed",
		RecentEvents: []RecentEvent{
			{EventID: "ev-1", Kind: "run.started", SafeSummary: "run started", OccurredAt: now},
		},
		TokenUsage:     &tokens,
		CostAmount:     &cost,
		SourceRefs:     []string{"task://t1", "run://r1"},
		FreshnessState: FreshnessFresh,

		// Unsafe internals that must never leak.
		RawStdout:         "DB_URL=postgres://user:secret@host/db\nAPI_KEY=abc123",
		RawChainOfThought: "the model is thinking about the secret password hunter2",
		EnvVars:           map[string]string{"DATABASE_URL": "postgres://u:p@h/db", "JWT": "jwt-token"},
	}

	dto := BuildDTO(in, now)
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(b)

	for _, forbidden := range []string{
		"API_KEY", "api_key", "abc123", "hunter2", "postgres://u:p@h/db",
		"jwt-token", "JWT", "DATABASE_URL", "DB_URL", "password", "RawStdout", "RawChainOfThought", "EnvVars",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("DTO leaked forbidden content %q in: %s", forbidden, raw)
		}
	}
}

func TestBuildDTO_DerivesPresenceAndStage(t *testing.T) {
	now := time.Now().UTC()
	in := SnapshotInput{
		WorkspaceID: "ws-1",
		EmployeeID:  "EMP-01",
		AgentID:     "AGT-01",
		DisplayName: "Emory",
		Derivation: Inputs{
			RuntimeOnline: true,
			HasOpenTask:   true,
			TaskQueued:    true,
			RunStarted:    false,
		},
		StageHint: StageTesting,
	}
	dto := BuildDTO(in, now)
	if dto.PresenceState != PresenceQueued {
		t.Fatalf("presence = %q, want %q", dto.PresenceState, PresenceQueued)
	}
	if dto.WorkStage != StageTesting {
		t.Fatalf("stage = %q, want %q", dto.WorkStage, StageTesting)
	}
}

func TestBuildDTO_ClosedEnumsAndSchemaVersion(t *testing.T) {
	dto := BuildDTO(SnapshotInput{WorkspaceID: "w", EmployeeID: "e", AgentID: "a"}, time.Now().UTC())
	if dto.SchemaVersion != SchemaVersionV1 {
		t.Fatalf("schema_version = %q", dto.SchemaVersion)
	}
	if dto.PresenceState != PresenceOffline {
		t.Fatalf("empty inputs -> runtime offline, got %q", dto.PresenceState)
	}
	if dto.FreshnessState != FreshnessFresh {
		t.Fatalf("default freshness = %q", dto.FreshnessState)
	}
}

func TestBuildDTO_CopiesExecutionChainFields(t *testing.T) {
	now := time.Now().UTC()
	in := SnapshotInput{
		WorkspaceID:            "ws-1",
		EmployeeID:             "EMP-01",
		AgentID:                "AGT-01",
		DisplayName:            "Kai",
		IssueID:                "issue-797",
		IssueIdentifier:        "HIV-797",
		IssueTitle:             "[DEV] Work Wall complete execution-chain projection",
		ProjectID:              "proj-1",
		ProjectTitle:           "HIVECREW 自我开发项目",
		TaskID:                 "task-1",
		RunID:                  "run-1",
		RuntimeProfileID:       "profile-1",
		RuntimeProfileName:     "glm-5.3 运行档案",
		ExecutionReceiptRef:    "receipt://task-1",
		ExecutionReceiptStatus: "completed",
		SourceRefs:             []string{"agent://AGT-01"},
	}
	dto := BuildDTO(in, now)

	if dto.IssueIdentifier != "HIV-797" {
		t.Fatalf("issue_identifier = %q", dto.IssueIdentifier)
	}
	if dto.IssueTitle != in.IssueTitle || dto.ProjectID != in.ProjectID || dto.ProjectTitle != in.ProjectTitle {
		t.Fatalf("issue/project projection = %+v", dto)
	}
	if dto.RuntimeProfileID != "profile-1" || dto.RuntimeProfileName != in.RuntimeProfileName {
		t.Fatalf("runtime profile projection = %+v", dto)
	}
	if dto.RunID != "run-1" {
		t.Fatalf("run_id = %q", dto.RunID)
	}
	if dto.ExecutionReceiptRef != "receipt://task-1" || dto.ExecutionReceiptStatus != "completed" {
		t.Fatalf("receipt projection = %+v", dto)
	}

	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(b)
	for _, key := range []string{`"issue_identifier":"HIV-797"`, `"runtime_profile_id":"profile-1"`, `"execution_receipt_ref":"receipt://task-1"`, `"execution_receipt_status":"completed"`} {
		if !strings.Contains(raw, key) {
			t.Fatalf("wire JSON missing %s in: %s", key, raw)
		}
	}
}

func TestBuildDTO_EmptyChainFieldsOmitFromWire(t *testing.T) {
	dto := BuildDTO(SnapshotInput{WorkspaceID: "w", EmployeeID: "e", AgentID: "a"}, time.Now().UTC())
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(b)
	for _, key := range []string{"issue_identifier", "runtime_profile_id", "runtime_profile_name", "execution_receipt_ref", "execution_receipt_status", "run_id", "project_id", "execution_runtime_id", "execution_runtime_carrier", "execution_model_name", "execution_profile_id", "execution_profile_name"} {
		if strings.Contains(raw, key) {
			t.Fatalf("absent chain evidence must be omitted from the wire, found %q in: %s", key, raw)
		}
	}
}

func TestBuildDTO_CopiesExecutionRuntimeFields(t *testing.T) {
	now := time.Now().UTC()
	in := SnapshotInput{
		WorkspaceID:             "ws-1",
		EmployeeID:              "EMP-01",
		AgentID:                 "AGT-01",
		DisplayName:             "Raven",
		RuntimeID:               "rt-current",
		RuntimeCarrier:          "prime",
		ExecutionRuntimeID:      "rt-task-orig",
		ExecutionRuntimeCarrier: "codex",
		ExecutionModelName:      "o3",
		ExecutionProfileID:      "profile-exec",
		ExecutionProfileName:    "Codex 执行档案",
		SourceRefs:              []string{"agent://AGT-01"},
	}
	dto := BuildDTO(in, now)

	// Current binding stays.
	if dto.RuntimeID != "rt-current" || dto.RuntimeCarrier != "prime" {
		t.Fatalf("current binding changed: runtime_id=%q carrier=%q", dto.RuntimeID, dto.RuntimeCarrier)
	}
	// Execution-runtime projection.
	if dto.ExecutionRuntimeID != "rt-task-orig" {
		t.Fatalf("execution_runtime_id = %q", dto.ExecutionRuntimeID)
	}
	if dto.ExecutionRuntimeCarrier != "codex" {
		t.Fatalf("execution_runtime_carrier = %q", dto.ExecutionRuntimeCarrier)
	}
	if dto.ExecutionModelName != "o3" {
		t.Fatalf("execution_model_name = %q", dto.ExecutionModelName)
	}
	if dto.ExecutionProfileID != "profile-exec" || dto.ExecutionProfileName != "Codex 执行档案" {
		t.Fatalf("execution profile = %+v", dto)
	}

	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(b)
	for _, key := range []string{`"execution_runtime_id":"rt-task-orig"`, `"execution_runtime_carrier":"codex"`, `"execution_model_name":"o3"`, `"execution_profile_id":"profile-exec"`} {
		if !strings.Contains(raw, key) {
			t.Fatalf("wire JSON missing %s in: %s", key, raw)
		}
	}
}

func TestBuildDTO_RuntimeCarrierNotLLMProvider(t *testing.T) {
	now := time.Now().UTC()
	in := SnapshotInput{
		WorkspaceID:    "ws-1",
		EmployeeID:     "EMP-01",
		AgentID:        "AGT-01",
		DisplayName:    "Emory",
		RuntimeID:      "rt-1",
		RuntimeCarrier: "prime",
		ModelName:      "deepseek-v4",
	}
	dto := BuildDTO(in, now)

	if dto.RuntimeCarrier != "prime" {
		t.Fatalf("runtime_carrier = %q, want %q", dto.RuntimeCarrier, "prime")
	}
	if dto.LLMProvider != "" {
		t.Fatalf("llm_provider = %q, want empty (no authoritative source)", dto.LLMProvider)
	}

	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(b)
	if !strings.Contains(raw, `"runtime_provider":"prime"`) {
		t.Fatalf("compatibility wire key must carry the runtime carrier: %s", raw)
	}
	if strings.Contains(raw, `"llm_provider"`) {
		t.Fatalf("absent llm_provider must be omitted from the wire: %s", raw)
	}
}

func TestBuildDTO_LLMProviderOnlyFromAuthoritativeSource(t *testing.T) {
	now := time.Now().UTC()
	in := SnapshotInput{
		WorkspaceID:    "ws-1",
		EmployeeID:     "EMP-01",
		AgentID:        "AGT-01",
		DisplayName:    "Emory",
		RuntimeCarrier: "volcengine",
		ModelName:      "doubao-seed-2.1-turbo",
		LLMProvider:    "volcengine-ark",
	}
	dto := BuildDTO(in, now)

	if dto.RuntimeCarrier != "volcengine" {
		t.Fatalf("runtime_carrier = %q, want %q", dto.RuntimeCarrier, "volcengine")
	}
	if dto.LLMProvider != "volcengine-ark" {
		t.Fatalf("llm_provider = %q, want %q", dto.LLMProvider, "volcengine-ark")
	}
	if dto.RuntimeCarrier == dto.LLMProvider {
		t.Fatalf("runtime_carrier and llm_provider must carry independent semantics")
	}

	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(b)
	if !strings.Contains(raw, `"runtime_provider":"volcengine"`) {
		t.Fatalf("compatibility wire key must carry the runtime carrier: %s", raw)
	}
	if !strings.Contains(raw, `"llm_provider":"volcengine-ark"`) {
		t.Fatalf("wire JSON missing llm_provider: %s", raw)
	}
}
