package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const (
	invWorkspace = "9f0a0a00-0000-4000-8000-000000000001"
	invOtherWS   = "9f0a0a00-0000-4000-8000-000000000002"
	invAgentID   = "9f0a0a00-0000-4000-8000-0000000000a1"
	invRuntimeID = "9f0a0a00-0000-4000-8000-0000000000a2"
	invProfileID = "9f0a0a00-0000-4000-8000-0000000000a3"
	invSecret    = "sk-SECRET1234567890abcdef"
)

func invUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		panic(err)
	}
	return id
}

// invStore is the fake runtimeInventoryStore. Every lookup is scoped: rows
// are only visible to the workspace they belong to, mirroring the generated
// SQL so workspace isolation is actually exercised.
type invStore struct {
	agents   []db.Agent // active employees (ListAgents result)
	byID     map[string]db.Agent
	runtimes map[string]db.AgentRuntime
	profiles map[string]db.RuntimeProfile
	failWith error // returned by runtime/profile lookups when set

	seenWorkspaces []pgtype.UUID
}

func newInvStore() *invStore {
	return &invStore{
		byID:     map[string]db.Agent{},
		runtimes: map[string]db.AgentRuntime{},
		profiles: map[string]db.RuntimeProfile{},
	}
}

func (s *invStore) ListAgents(ctx context.Context, workspaceID pgtype.UUID) ([]db.Agent, error) {
	s.seenWorkspaces = append(s.seenWorkspaces, workspaceID)
	out := []db.Agent{}
	for _, agent := range s.agents {
		if agent.WorkspaceID == workspaceID {
			out = append(out, agent)
		}
	}
	return out, nil
}

func (s *invStore) GetAgentInWorkspace(ctx context.Context, arg db.GetAgentInWorkspaceParams) (db.Agent, error) {
	s.seenWorkspaces = append(s.seenWorkspaces, arg.WorkspaceID)
	agent, ok := s.byID[uuidToString(arg.ID)]
	if !ok || agent.WorkspaceID != arg.WorkspaceID {
		return db.Agent{}, pgx.ErrNoRows
	}
	return agent, nil
}

func (s *invStore) GetAgentRuntimeForWorkspace(ctx context.Context, arg db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error) {
	s.seenWorkspaces = append(s.seenWorkspaces, arg.WorkspaceID)
	if s.failWith != nil {
		return db.AgentRuntime{}, s.failWith
	}
	runtime, ok := s.runtimes[uuidToString(arg.ID)]
	if !ok || runtime.WorkspaceID != arg.WorkspaceID {
		return db.AgentRuntime{}, pgx.ErrNoRows
	}
	return runtime, nil
}

func (s *invStore) GetRuntimeProfileForWorkspace(ctx context.Context, arg db.GetRuntimeProfileForWorkspaceParams) (db.RuntimeProfile, error) {
	s.seenWorkspaces = append(s.seenWorkspaces, arg.WorkspaceID)
	if s.failWith != nil {
		return db.RuntimeProfile{}, s.failWith
	}
	profile, ok := s.profiles[uuidToString(arg.ID)]
	if !ok || profile.WorkspaceID != arg.WorkspaceID {
		return db.RuntimeProfile{}, pgx.ErrNoRows
	}
	return profile, nil
}

func invAgent(name string, runtimeID *string, model string, archived ...bool) db.Agent {
	agent := db.Agent{
		ID:          invUUID(invAgentID),
		WorkspaceID: invUUID(invWorkspace),
		Name:        name,
		Kind:        "user",
		Status:      "idle",
		RuntimeMode: "local",
		// The inventory must never echo these blobs even though the walk
		// loads the whole row.
		RuntimeConfig: []byte(`{"api_key":"` + invSecret + `"}`),
		CustomEnv:     []byte(`{"TOKEN":"` + invSecret + `"}`),
		CustomArgs:    []byte(`["--secret","` + invSecret + `"]`),
		McpConfig:     []byte(`{"mcp":{"url":"http://127.0.0.1:9"}}`),
	}
	if model != "" {
		agent.Model = pgtype.Text{String: model, Valid: true}
	}
	if runtimeID != nil {
		agent.RuntimeID = invUUID(*runtimeID)
	}
	if len(archived) > 0 && archived[0] {
		agent.ArchivedAt = pgtype.Timestamptz{Valid: true}
	}
	return agent
}

func invRuntime(provider, status string, profileID *string, metadata []byte) db.AgentRuntime {
	runtime := db.AgentRuntime{
		ID:          invUUID(invRuntimeID),
		WorkspaceID: invUUID(invWorkspace),
		DaemonID:    pgtype.Text{String: "daemon-1", Valid: true},
		Name:        "Mac Studio runtime",
		RuntimeMode: "local",
		Provider:    provider,
		Status:      status,
		DeviceInfo:  "/Users/secret/host-machine-name",
	}
	if metadata != nil {
		runtime.Metadata = metadata
	}
	if profileID != nil {
		runtime.ProfileID = invUUID(*profileID)
	}
	return runtime
}

func invProfile(enabled bool) db.RuntimeProfile {
	return db.RuntimeProfile{
		ID:             invUUID(invProfileID),
		WorkspaceID:    invUUID(invWorkspace),
		DisplayName:    "In-house wrapper",
		ProtocolFamily: "codex",
		Enabled:        enabled,
	}
}

func ptrString(value string) *string { return &value }

func invProfileIDPtr() *string {
	value := invProfileID
	return &value
}

func invRuntimeIDPtr() *string {
	value := invRuntimeID
	return &value
}

// invHandler installs the fake store on the seam and restores it after the
// test.
func invHandler(t *testing.T, store *invStore) *Handler {
	t.Helper()
	previous := runtimeInventoryStoreFor
	runtimeInventoryStoreFor = func(*Handler) runtimeInventoryStore { return store }
	t.Cleanup(func() { runtimeInventoryStoreFor = previous })
	return &Handler{}
}

func invRequest(query string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/runtimes/inventory"+query, nil)
	request.Header.Set("X-Workspace-ID", invWorkspace)
	return request
}

func invCall(t *testing.T, handler *Handler, query string) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.GetRuntimeInventory(recorder, invRequest(query))
	body := map[string]json.RawMessage{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not a JSON object: %v body=%s", err, recorder.Body.String())
	}
	return recorder, body
}

func invRows(t *testing.T, body map[string]json.RawMessage) []map[string]any {
	t.Helper()
	raw, ok := body["employees"]
	if !ok {
		t.Fatalf("missing employees key: %v", body)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("employees is not an array: %v", err)
	}
	return rows
}

func invLink(t *testing.T, row map[string]any, link string) map[string]any {
	t.Helper()
	object, ok := row[link].(map[string]any)
	if !ok {
		t.Fatalf("row lacks %s object: %v", link, row)
	}
	return object
}

func invCount(t *testing.T, body map[string]json.RawMessage) int {
	t.Helper()
	raw, ok := body["count"]
	if !ok {
		t.Fatalf("missing count key: %v", body)
	}
	var count int
	if err := json.Unmarshal(raw, &count); err != nil {
		t.Fatalf("count is not a number: %v", err)
	}
	return count
}

// ---------------------------------------------------------------------------
// Chain projection
// ---------------------------------------------------------------------------

func TestRuntimeInventoryCompleteChain(t *testing.T) {
	store := newInvStore()
	store.agents = []db.Agent{invAgent("Kai｜后端与全栈工程师", invRuntimeIDPtr(), "glm-5.3")}
	store.byID[invAgentID] = store.agents[0]
	store.runtimes[invRuntimeID] = invRuntime("codex", "online", invProfileIDPtr(), nil)
	store.profiles[invProfileID] = invProfile(true)

	recorder, body := invCall(t, invHandler(t, store), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if invCount(t, body) != 1 {
		t.Fatalf("count = %v", body["count"])
	}
	rows := invRows(t, body)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	row := rows[0]

	if got := invLink(t, row, "agent")["state"]; got != "ok" {
		t.Fatalf("agent state = %v", got)
	}
	if got := invLink(t, row, "agent")["runtime_mode"]; got != "local" {
		t.Fatalf("agent runtime_mode = %v", got)
	}
	if got := invLink(t, row, "runtime")["state"]; got != "ok" {
		t.Fatalf("runtime state = %v", got)
	}
	if got := invLink(t, row, "runtime")["status"]; got != "online" {
		t.Fatalf("runtime status = %v", got)
	}
	profile := invLink(t, row, "profile")
	if profile["state"] != "ok" || profile["display_name"] != "In-house wrapper" || profile["protocol_family"] != "codex" {
		t.Fatalf("profile = %v", profile)
	}
	if profile["enabled"] != true {
		t.Fatalf("profile enabled = %v", profile["enabled"])
	}
	if row["provider"] != "codex" || row["model"] != "glm-5.3" {
		t.Fatalf("provider/model = %v/%v", row["provider"], row["model"])
	}
	if got := invLink(t, row, "registration")["state"]; got != "online" {
		t.Fatalf("registration state = %v", got)
	}
	employee := invLink(t, row, "employee")
	if employee["employee_id"] != invAgentID || employee["name"] != "Kai｜后端与全栈工程师" {
		t.Fatalf("employee = %v", employee)
	}
}

func TestRuntimeInventoryMissingRuntimeVariants(t *testing.T) {
	unbound := invAgent("Unbound", nil, "m1")
	dangling := invAgent("Dangling", invRuntimeIDPtr(), "m2")
	dangling.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000a2")

	store := newInvStore()
	store.agents = []db.Agent{unbound, dangling}
	store.byID[uuidToString(unbound.ID)] = unbound
	store.byID[uuidToString(dangling.ID)] = dangling
	// No runtime row for dangling's runtime_id.

	_, body := invCall(t, invHandler(t, store), "")
	rows := invRows(t, body)
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	for _, row := range rows {
		if got := invLink(t, row, "runtime")["state"]; got != "missing_runtime" {
			t.Fatalf("runtime state = %v row=%v", got, row)
		}
		if got := invLink(t, row, "profile")["state"]; got != "unknown" {
			t.Fatalf("profile state = %v row=%v", got, row)
		}
		if got := invLink(t, row, "registration")["state"]; got != "unknown" {
			t.Fatalf("registration state = %v row=%v", got, row)
		}
		if row["provider"] != "" {
			t.Fatalf("provider should be empty, got %v", row["provider"])
		}
	}
}

func TestRuntimeInventoryProfileVariants(t *testing.T) {
	builtin := invAgent("Builtin", invRuntimeIDPtr(), "")
	danglingProfile := invAgent("DanglingProfile", ptrString("9f0a0a00-0000-4000-8000-0000000000c2"), "")
	disabled := invAgent("Disabled", ptrString("9f0a0a00-0000-4000-8000-0000000000c3"), "")
	builtin.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000b1")
	danglingProfile.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000b2")
	disabled.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000b3")

	goneProfile := "9f0a0a00-0000-4000-8000-0000000000d9"

	store := newInvStore()
	store.agents = []db.Agent{builtin, danglingProfile, disabled}
	for _, agent := range store.agents {
		store.byID[uuidToString(agent.ID)] = agent
	}
	store.runtimes[invRuntimeID] = invRuntime("codex", "offline", nil, nil) // builtin
	store.runtimes["9f0a0a00-0000-4000-8000-0000000000c2"] = func() db.AgentRuntime {
		rt := invRuntime("codex", "offline", &goneProfile, nil)
		rt.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000c2")
		return rt
	}()
	store.runtimes["9f0a0a00-0000-4000-8000-0000000000c3"] = func() db.AgentRuntime {
		rt := invRuntime("codex", "offline", invProfileIDPtr(), nil)
		rt.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000c3")
		return rt
	}()
	store.profiles[invProfileID] = invProfile(false)

	_, body := invCall(t, invHandler(t, store), "")
	rows := invRows(t, body)
	states := map[string]string{}
	for _, row := range rows {
		employee := invLink(t, row, "employee")
		states[employee["name"].(string)] = invLink(t, row, "profile")["state"].(string)
	}
	if states["Builtin"] != "builtin" {
		t.Fatalf("builtin profile state = %v", states)
	}
	if states["DanglingProfile"] != "missing_profile" {
		t.Fatalf("dangling profile state = %v", states)
	}
	if states["Disabled"] != "disabled" {
		t.Fatalf("disabled profile state = %v", states)
	}
	for _, row := range rows {
		if got := invLink(t, row, "registration")["state"]; got != "offline" {
			t.Fatalf("registration state = %v row=%v", got, row)
		}
	}
}

func TestRuntimeInventoryRegistrationErrorIsSanitized(t *testing.T) {
	metadata := []byte(`{
		"runtime_profile_registration_error": true,
		"runtime_profile_failure_reason": "spawn /Users/jiawei/hivecosm/secret/wrapper failed: bearer sk-SECRET1234567890abcdef rejected by https://example.internal/api/v1/long/opaque/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA/check",
		"command_name": "/Users/jiawei/hivecosm/secret/wrapper"
	}`)
	store := newInvStore()
	store.agents = []db.Agent{invAgent("Broken", invRuntimeIDPtr(), "")}
	store.byID[invAgentID] = store.agents[0]
	store.runtimes[invRuntimeID] = invRuntime("codex", "offline", invProfileIDPtr(), metadata)
	store.profiles[invProfileID] = invProfile(true)

	_, body := invCall(t, invHandler(t, store), "")
	rows := invRows(t, body)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	registration := invLink(t, rows[0], "registration")
	if registration["state"] != "registration_error" {
		t.Fatalf("registration state = %v", registration)
	}
	if reason := registration["reason"]; reason != runtimeInventoryRegistrationErrorCode {
		t.Fatalf("reason = %v, want fixed safe code", reason)
	}
	encoded := recorderBodyString(body)
	for _, leak := range []string{
		invSecret,
		"/Users/jiawei",
		"example.internal",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"runtime_config",
		"custom_env",
		"custom_args",
		"mcp_config",
		"metadata",
		"127.0.0.1:9",
	} {
		if strings.Contains(encoded, leak) {
			t.Fatalf("response leaks %q: %s", leak, encoded)
		}
	}
}

// recorderBodyString re-encodes the parsed body so leak assertions cover
// escaped forms too.
func recorderBodyString(body map[string]json.RawMessage) string {
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestRuntimeInventoryOfflineIsNotProviderUnavailability(t *testing.T) {
	store := newInvStore()
	store.agents = []db.Agent{invAgent("Off", invRuntimeIDPtr(), "m")}
	store.byID[invAgentID] = store.agents[0]
	store.runtimes[invRuntimeID] = invRuntime("kimi", "offline", nil, nil)

	_, body := invCall(t, invHandler(t, store), "")
	rows := invRows(t, body)
	registration := invLink(t, rows[0], "registration")
	if registration["state"] != "offline" || registration["reason"] != nil {
		t.Fatalf("registration = %v", registration)
	}
	// The provider is still echoed verbatim: offline is a runtime-daemon
	// state and must not be re-interpreted as provider unavailability.
	if rows[0]["provider"] != "kimi" {
		t.Fatalf("provider = %v", rows[0]["provider"])
	}
}

func TestRuntimeInventoryUnknownRuntimeStatusIsNormalized(t *testing.T) {
	store := newInvStore()
	store.agents = []db.Agent{invAgent("Future", invRuntimeIDPtr(), "m")}
	store.byID[invAgentID] = store.agents[0]
	store.runtimes[invRuntimeID] = invRuntime("provider", "connecting", nil, nil)

	_, body := invCall(t, invHandler(t, store), "")
	registration := invLink(t, invRows(t, body)[0], "registration")
	if registration["state"] != runtimeInventoryStateUnknown {
		t.Fatalf("registration = %v", registration)
	}
}

// ---------------------------------------------------------------------------
// Employee reference handling
// ---------------------------------------------------------------------------

func TestRuntimeInventoryEmployeeReferenceVariants(t *testing.T) {
	store := newInvStore()
	active := invAgent("Raven", invRuntimeIDPtr(), "qwen3-coder")
	active.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000c1")
	archived := invAgent("Ghost", nil, "")
	archived.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000c2")
	archived.ArchivedAt = pgtype.Timestamptz{Valid: true}
	store.agents = []db.Agent{active}
	store.byID[uuidToString(active.ID)] = active
	store.byID[uuidToString(archived.ID)] = archived
	store.runtimes[invRuntimeID] = invRuntime("qwen", "online", nil, nil)
	handler := invHandler(t, store)

	// By exact name.
	recorder, body := invCall(t, handler, "?employee="+strings.ReplaceAll("Raven", " ", "%20"))
	if recorder.Code != http.StatusOK || len(invRows(t, body)) != 1 {
		t.Fatalf("name lookup status = %d", recorder.Code)
	}
	if got := invLink(t, invRows(t, body)[0], "employee")["name"]; got != "Raven" {
		t.Fatalf("name lookup returned %v", got)
	}

	// By agent UUID.
	recorder, body = invCall(t, handler, "?employee="+uuidToString(active.ID))
	if recorder.Code != http.StatusOK || invCount(t, body) != 1 {
		t.Fatalf("uuid lookup status = %d", recorder.Code)
	}

	// Archived agent: identity resolves, active agent link is missing.
	recorder, body = invCall(t, handler, "?employee="+uuidToString(archived.ID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("archived lookup status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	rows := invRows(t, body)
	if len(rows) != 1 {
		t.Fatalf("archived rows = %d", len(rows))
	}
	if got := invLink(t, rows[0], "agent")["state"]; got != "missing_agent" {
		t.Fatalf("archived agent state = %v", got)
	}
	if got := invLink(t, rows[0], "registration")["state"]; got != "unknown" {
		t.Fatalf("archived registration state = %v", got)
	}
	if got := invLink(t, rows[0], "employee")["name"]; got != "Ghost" {
		t.Fatalf("archived employee name = %v", got)
	}

	// Unknown UUID and unknown name are 404s.
	for _, ref := range []string{"9f0a0a00-0000-4000-8000-00000000dead", "Nobody"} {
		recorder, _ = invCall(t, handler, "?employee="+ref)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("ref %q status = %d", ref, recorder.Code)
		}
	}
}

func TestRuntimeInventoryEmployeeReferenceFromOtherWorkspaceIsNotFound(t *testing.T) {
	store := newInvStore()
	foreign := invAgent("Foreign", nil, "")
	foreign.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000f1")
	foreign.WorkspaceID = invUUID(invOtherWS)
	store.byID[uuidToString(foreign.ID)] = foreign
	handler := invHandler(t, store)

	recorder, _ := invCall(t, handler, "?employee="+uuidToString(foreign.ID))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace ref status = %d", recorder.Code)
	}
}

func TestRuntimeInventoryWorkspaceIsolation(t *testing.T) {
	store := newInvStore()
	local := invAgent("Local", invRuntimeIDPtr(), "")
	local.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000e1")
	foreign := invAgent("Foreign", invRuntimeIDPtr(), "")
	foreign.ID = invUUID("9f0a0a00-0000-4000-8000-0000000000e2")
	foreign.WorkspaceID = invUUID(invOtherWS)
	foreign.RuntimeID = invUUID(invRuntimeID)
	store.agents = []db.Agent{local, foreign}
	store.byID[uuidToString(local.ID)] = local
	store.byID[uuidToString(foreign.ID)] = foreign
	store.runtimes[invRuntimeID] = invRuntime("codex", "online", nil, nil)

	recorder, body := invCall(t, invHandler(t, store), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	rows := invRows(t, body)
	if len(rows) != 1 || invLink(t, rows[0], "employee")["name"] != "Local" {
		t.Fatalf("isolation broken: %s", recorder.Body.String())
	}
	for _, seen := range store.seenWorkspaces {
		if uuidToString(seen) != invWorkspace {
			t.Fatalf("query escaped workspace scope: %v", seen)
		}
	}
}

func TestRuntimeInventoryRequestValidation(t *testing.T) {
	handler := invHandler(t, newInvStore())

	tooLong := strings.Repeat("x", 129)
	recorder, _ := invCall(t, handler, "?employee="+tooLong)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("long ref status = %d", recorder.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/runtimes/inventory", nil)
	request.Header.Set("X-Workspace-ID", "not-a-uuid")
	recorder = httptest.NewRecorder()
	handler.GetRuntimeInventory(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad workspace status = %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/runtimes/inventory", nil)
	recorder = httptest.NewRecorder()
	handler.GetRuntimeInventory(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing workspace status = %d", recorder.Code)
	}
}

func TestRuntimeInventoryStorageFailureIsServerError(t *testing.T) {
	store := newInvStore()
	store.agents = []db.Agent{invAgent("Boom", invRuntimeIDPtr(), "")}
	store.byID[invAgentID] = store.agents[0]
	store.failWith = errors.New("connection reset")
	handler := invHandler(t, store)

	recorder, _ := invCall(t, handler, "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Allowlist and output stability
// ---------------------------------------------------------------------------

func TestRuntimeInventoryEmitsAllowlistedFieldsOnly(t *testing.T) {
	store := newInvStore()
	store.agents = []db.Agent{invAgent("Kai", invRuntimeIDPtr(), "glm-5.3")}
	store.byID[invAgentID] = store.agents[0]
	store.runtimes[invRuntimeID] = invRuntime("codex", "online", invProfileIDPtr(), []byte(`{"version":"1.0","cli_version":"0.1"}`))
	store.profiles[invProfileID] = invProfile(true)

	recorder, body := invCall(t, invHandler(t, store), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}

	expected := map[string]map[string]bool{
		"employee":     {"employee_id": true, "name": true},
		"agent":        {"state": true, "id": true, "name": true, "runtime_mode": true, "status": true},
		"runtime":      {"state": true, "id": true, "daemon_id": true, "name": true, "status": true},
		"profile":      {"state": true, "id": true, "display_name": true, "protocol_family": true, "enabled": true},
		"registration": {"state": true},
	}
	row := invRows(t, body)[0]
	rowKeys := map[string]bool{}
	for key := range row {
		rowKeys[key] = true
	}
	for _, key := range []string{"employee", "agent", "runtime", "profile", "provider", "model", "registration"} {
		if !rowKeys[key] {
			t.Fatalf("row lacks %s: %v", key, rowKeys)
		}
		delete(rowKeys, key)
	}
	if len(rowKeys) != 0 {
		t.Fatalf("row has unexpected top-level keys: %v", rowKeys)
	}
	for link, allowed := range expected {
		object := invLink(t, row, link)
		if len(object) != len(allowed) {
			t.Fatalf("%s keys = %v", link, object)
		}
		for key := range allowed {
			if _, exists := object[key]; !exists {
				t.Fatalf("%s lacks %s: %v", link, key, object)
			}
		}
	}

	if strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("body leaks secret material: %s", recorder.Body.String())
	}
}

func TestRuntimeInventoryJSONShapeIsStable(t *testing.T) {
	store := newInvStore()
	store.agents = []db.Agent{invAgent("Kai", nil, "")}
	store.byID[invAgentID] = store.agents[0]
	handler := invHandler(t, store)

	recorder, _ := invCall(t, handler, "")
	raw := recorder.Body.String()

	// Envelope keys keep a fixed order: count, employees.
	if !(strings.Index(raw, "\"count\"") < strings.Index(raw, "\"employees\"")) {
		t.Fatalf("envelope key order unstable: %s", raw)
	}
	// Entry keys keep struct order employee..registration.
	order := []string{"\"employee\"", "\"agent\"", "\"runtime\"", "\"profile\"", "\"provider\"", "\"model\"", "\"registration\""}
	for i := 1; i < len(order); i++ {
		if !(strings.Index(raw, order[i-1]) < strings.Index(raw, order[i])) {
			t.Fatalf("entry key order unstable near %s: %s", order[i], raw)
		}
	}

	// Empty workspaces still produce a stable empty array, not null.
	emptyRecorder, _ := invCall(t, invHandler(t, newInvStore()), "")
	if !strings.Contains(emptyRecorder.Body.String(), "\"employees\":[]") {
		t.Fatalf("employees must be [] when empty: %s", emptyRecorder.Body.String())
	}
}
