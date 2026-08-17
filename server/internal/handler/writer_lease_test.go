package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWriterLeaseActionAllowedByTaskStatus(t *testing.T) {
	if !writerLeaseActionAllowed("acquire", "dispatched") || !writerLeaseActionAllowed("acquire", "waiting_local_directory") {
		t.Fatal("acquire should be allowed before execution")
	}
	if writerLeaseActionAllowed("acquire", "running") || writerLeaseActionAllowed("renew", "completed") {
		t.Fatal("lease action accepted an invalid task status")
	}
	if !writerLeaseActionAllowed("release", "completed") || !writerLeaseActionAllowed("release", "running") {
		t.Fatal("release should support terminal and interrupted active cleanup")
	}
	if writerLeaseActionAllowed("unknown", "running") {
		t.Fatal("unknown lease action accepted")
	}
}

func TestWriterLeaseRequestRejectsTrailingJSONAndUnknownFields(t *testing.T) {
	if _, err := decodeWriterLeaseRequest(strings.NewReader(`{"resource_id":"r"}{"resource_id":"second"}`)); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
	if _, err := decodeWriterLeaseRequest(strings.NewReader(`{"resource_id":"r","holder_id":"caller"}`)); err == nil {
		t.Fatal("holder_id was accepted as a request field")
	}
}

func TestWriterLeaseRequestActionCredentialsAndTTL(t *testing.T) {
	token := uuid.New()
	valid := []struct {
		action string
		req    writerLeaseRequest
	}{
		{"acquire", writerLeaseRequest{TTLMS: 1000}},
		{"renew", writerLeaseRequest{LeaseToken: token, FenceGeneration: 1, TTLMS: 0}},
		{"verify", writerLeaseRequest{LeaseToken: token, FenceGeneration: 1}},
		{"release", writerLeaseRequest{LeaseToken: token, FenceGeneration: 1}},
	}
	for _, tc := range valid {
		if err := validateWriterLeaseRequest(tc.action, tc.req); err != nil {
			t.Errorf("%s rejected: %v", tc.action, err)
		}
	}
	invalid := []struct {
		action string
		req    writerLeaseRequest
	}{
		{"acquire", writerLeaseRequest{LeaseToken: token}},
		{"renew", writerLeaseRequest{FenceGeneration: 1}},
		{"verify", writerLeaseRequest{LeaseToken: token, FenceGeneration: 1, TTLMS: 1000}},
		{"release", writerLeaseRequest{LeaseToken: token, FenceGeneration: 0}},
		{"acquire", writerLeaseRequest{TTLMS: 999}},
		{"renew", writerLeaseRequest{LeaseToken: token, FenceGeneration: 1, TTLMS: service.DefaultLeaseDuration.Milliseconds() + 1}},
	}
	for _, tc := range invalid {
		if err := validateWriterLeaseRequest(tc.action, tc.req); err == nil {
			t.Errorf("%s accepted invalid request %+v", tc.action, tc.req)
		}
	}
}

func TestWriterLeaseDaemonMatchesRuntime(t *testing.T) {
	runtimeID := uuid.New()
	runtime := db.AgentRuntime{ID: pgtype.UUID{Bytes: runtimeID, Valid: true}, DaemonID: pgtype.Text{String: "daemon-a", Valid: true}}
	if !writerLeaseDaemonMatchesRuntime(runtime, "daemon-a") || writerLeaseDaemonMatchesRuntime(runtime, "daemon-b") {
		t.Fatal("valid runtime daemon identity was not enforced")
	}
	runtime.DaemonID = pgtype.Text{}
	if writerLeaseDaemonMatchesRuntime(runtime, "legacy-daemon") {
		t.Fatal("writer lease accepted runtime without stored daemon_id")
	}
	task := db.AgentTaskQueue{RuntimeID: runtime.ID}
	runtime.DaemonID = pgtype.Text{String: "daemon-a", Valid: true}
	if !writerLeaseTaskRuntimeMatchesDaemon(task, runtime, "daemon-a") || writerLeaseTaskRuntimeMatchesDaemon(task, runtime, "daemon-b") {
		t.Fatal("proof accepted a cross-daemon runtime identity")
	}
	runtime.ID = pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if writerLeaseTaskRuntimeMatchesDaemon(task, runtime, "daemon-a") {
		t.Fatal("proof accepted a runtime different from task.RuntimeID")
	}
}

func TestTaskCompleteResultExcludesWriterLeaseProof(t *testing.T) {
	proof := service.WriterLeaseTerminalProof{ResourceID: uuid.New(), LeaseToken: uuid.New(), FenceGeneration: 4}
	encoded, err := json.Marshal(taskCompleteResult{Output: "done"})
	if err != nil {
		t.Fatalf("marshal task result: %v", err)
	}
	if strings.Contains(string(encoded), proof.ResourceID.String()) || strings.Contains(string(encoded), proof.LeaseToken.String()) || strings.Contains(string(encoded), "fence_generation") {
		t.Fatalf("persisted task result shape leaked writer lease proof: %s", encoded)
	}
}

func TestDaemonRuntimeClaimOwnershipDistinguishesLegacyFallback(t *testing.T) {
	runtime := db.AgentRuntime{DaemonID: pgtype.Text{String: "daemon-a", Valid: true}}
	if !daemonRuntimeClaimOwnership(runtime, "daemon-a") || daemonRuntimeClaimOwnership(runtime, "daemon-b") {
		t.Fatal("mdt daemon ownership mismatch was accepted")
	}
	runtime.DaemonID = pgtype.Text{}
	if daemonRuntimeClaimOwnership(runtime, "daemon-a") {
		t.Fatal("mdt token was allowed to claim a NULL runtime daemon_id")
	}
	if !daemonRuntimeClaimOwnership(runtime, "") {
		t.Fatal("legacy auth fallback was rejected")
	}
}

func TestQuickCreateProjectIDIsAuthoritativeResolverContext(t *testing.T) {
	projectID := uuid.New()
	task := db.AgentTaskQueue{Context: []byte(`{"type":"quick_create","project_id":"` + projectID.String() + `"}`)}
	got, ok, err := quickCreateProjectID(task.Context)
	if err != nil || !ok || !got.Valid || got.Bytes != projectID {
		t.Fatalf("project=%v valid=%v ok=%v err=%v", got, got.Valid, ok, err)
	}
	if _, ok, err := quickCreateProjectID([]byte(`{"type":"quick_create"}`)); err != nil || ok {
		t.Fatalf("missing quick-create project accepted: ok=%v err=%v", ok, err)
	}
}
