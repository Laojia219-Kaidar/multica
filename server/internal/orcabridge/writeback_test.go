package orcabridge

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseWorkerResultAcceptsObservedPayloadShape(t *testing.T) {
	// The observed Orca message carries `payload` as a JSON-encoded string.
	message := OrcaMessage{
		ID:         "msg_577b8448d366",
		RunID:      "run_8b4856eda7b4",
		FromHandle: "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e",
		Type:       "worker_done",
		Subject:    "A1 done",
		Body:       "Implemented the bridge.",
		Payload:    `{"taskId":"task_6cc7b1e49533","dispatchId":"ctx_9ca8c0c81a71","outcome":"succeeded","filesModified":["a.go","b.go"],"reportPath":"/tmp/report.md"}`,
	}
	result, err := ParseWorkerResult(message)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.TaskID != "task_6cc7b1e49533" || result.DispatchID != "ctx_9ca8c0c81a71" ||
		result.Outcome != "succeeded" || result.ReportPath != "/tmp/report.md" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.FilesModified) != 2 || result.FilesModified[0] != "a.go" {
		t.Fatalf("files = %v", result.FilesModified)
	}
}

func TestParseWorkerResultAcceptsCommaSeparatedFiles(t *testing.T) {
	message := OrcaMessage{
		ID:      "msg_577b8448d366",
		Type:    "worker_done",
		Subject: "done",
		Payload: `{"taskId":"task_1","dispatchId":"ctx_1","outcome":"failed","filesModified":"a.go, b.go"}`,
	}
	result, err := ParseWorkerResult(message)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(result.FilesModified) != 2 || result.FilesModified[1] != "b.go" {
		t.Fatalf("files = %v", result.FilesModified)
	}
}

func TestParseWorkerResultAcceptsEmbeddedObjectPayload(t *testing.T) {
	message := OrcaMessage{
		ID:      "msg_577b8448d366",
		Type:    "worker_done",
		Payload: `{"taskId":"task_1","dispatchId":"ctx_1","outcome":"succeeded"}`,
	}
	if _, err := ParseWorkerResult(message); err != nil {
		t.Fatalf("parse embedded object: %v", err)
	}
}

func TestParseWorkerResultAcceptsNullFiles(t *testing.T) {
	message := OrcaMessage{
		ID:      "msg_577b8448d366",
		Type:    "worker_done",
		Payload: `{"taskId":"task_1","dispatchId":"ctx_1","outcome":"succeeded","filesModified":null}`,
	}
	result, err := ParseWorkerResult(message)
	if err != nil {
		t.Fatalf("parse null files: %v", err)
	}
	if result.FilesModified == nil || len(result.FilesModified) != 0 {
		t.Fatalf("files must normalize to empty slice: %v", result.FilesModified)
	}
}

func TestParseWorkerResultRejectsInvalid(t *testing.T) {
	cases := []struct {
		name    string
		message OrcaMessage
		want    error
	}{
		{"wrong type", OrcaMessage{ID: "msg_1", Type: "heartbeat", Payload: "{}"}, ErrNotWorkerDone},
		{"bad message id", OrcaMessage{ID: "1", Type: "worker_done", Payload: `{"taskId":"task_1","dispatchId":"ctx_1","outcome":"succeeded"}`}, ErrInvalidWorkerResult},
		{"empty payload", OrcaMessage{ID: "msg_1", Type: "worker_done"}, ErrInvalidWorkerResult},
		{"non-object payload", OrcaMessage{ID: "msg_1", Type: "worker_done", Payload: `"just a string"`}, ErrInvalidWorkerResult},
		{"broken json string payload", OrcaMessage{ID: "msg_1", Type: "worker_done", Payload: `"{broken`}, ErrInvalidWorkerResult},
		{"bad task handle", OrcaMessage{ID: "msg_1", Type: "worker_done", Payload: `{"taskId":"nope","dispatchId":"ctx_1","outcome":"succeeded"}`}, ErrInvalidWorkerResult},
		{"bad dispatch handle", OrcaMessage{ID: "msg_1", Type: "worker_done", Payload: `{"taskId":"task_1","dispatchId":"nope","outcome":"succeeded"}`}, ErrInvalidWorkerResult},
		{"unknown outcome", OrcaMessage{ID: "msg_1", Type: "worker_done", Payload: `{"taskId":"task_1","dispatchId":"ctx_1","outcome":"maybe"}`}, ErrInvalidWorkerResult},
		{"empty file entry", OrcaMessage{ID: "msg_1", Type: "worker_done", Payload: `{"taskId":"task_1","dispatchId":"ctx_1","outcome":"succeeded","filesModified":[" "]}`}, ErrInvalidWorkerResult},
		{"flag-like report path", OrcaMessage{ID: "msg_1", Type: "worker_done", Payload: `{"taskId":"task_1","dispatchId":"ctx_1","outcome":"succeeded","reportPath":"--evil"}`}, ErrInvalidWorkerResult},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWorkerResult(tc.message)
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestWorkerResultDigestStableAcrossOrdering(t *testing.T) {
	base := OrcaMessage{ID: "msg_1", Type: "worker_done", Subject: "s", Body: "b"}
	one := WorkerResult{TaskID: "task_1", DispatchID: "ctx_1", Outcome: "succeeded", FilesModified: []string{"a", "b"}}
	two := WorkerResult{TaskID: "task_1", DispatchID: "ctx_1", Outcome: "succeeded", FilesModified: []string{"a", "b"}}
	d1, err := WorkerResultDigest(base, one)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := WorkerResultDigest(base, two)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("identical results must digest equal: %s vs %s", d1, d2)
	}
	changed := one
	changed.Outcome = "failed"
	d3, err := WorkerResultDigest(base, changed)
	if err != nil {
		t.Fatal(err)
	}
	if d3 == d1 {
		t.Fatal("outcome change must change the digest")
	}
}

func TestResultDigestInputCanonicalJSON(t *testing.T) {
	// Same content encoded from a generic map must match the struct digest.
	structDigest, err := CanonicalDigest(ResultDigestInput{
		OrcaTaskID:     "task_1",
		OrcaDispatchID: "ctx_1",
		Outcome:        "succeeded",
		Subject:        "s",
		Body:           "b",
		FilesModified:  []string{"a"},
		ReportPath:     "/r",
	})
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(`{"report_path":"/r","files_modified":["a"],"body":"b","subject":"s","outcome":"succeeded","orca_dispatch_id":"ctx_1","orca_task_id":"task_1"}`), &generic); err != nil {
		t.Fatal(err)
	}
	genericDigest, err := CanonicalDigest(generic)
	if err != nil {
		t.Fatal(err)
	}
	if structDigest != genericDigest {
		t.Fatalf("canonical digest must be key-order independent: %s vs %s", structDigest, genericDigest)
	}
}
