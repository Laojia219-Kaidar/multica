package orcabridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon"
)

// daemonRecordingServer records the complete/fail bodies the port sends and
// answers 200 so the existing daemon client succeeds.
type daemonRecordingServer struct {
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
}

func (s *daemonRecordingServer) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	s.paths = append(s.paths, r.URL.Path)
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *daemonRecordingServer) lastBody() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		return nil
	}
	return s.bodies[len(s.bodies)-1]
}

const testTaskID = "c05a0000-0000-4000-8000-000000000040"

func TestDaemonPortRedactsCredentialsBeforeSettlement(t *testing.T) {
	recorder := &daemonRecordingServer{}
	server := httptest.NewServer(http.HandlerFunc(recorder.handler))
	defer server.Close()

	port := NewDaemonLifecyclePort(daemon.NewClient(server.URL))
	ctx := context.Background()

	// CompleteTask: output with Bearer and provider keys must be redacted.
	err := port.CompleteTask(ctx, TaskCompletion{
		TaskID: testTaskID,
		Output: "done; used Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig and sk-ant-api03-AAABBBCCCDDDEEE api_key=0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	completed := recorder.lastBody()
	output, _ := completed["output"].(string)
	for _, leaked := range []string{"eyJhbGciOiJIUzI1NiJ9", "sk-ant-api03-AAABBBCCC", "0123456789abcdef"} {
		if strings.Contains(output, leaked) {
			t.Fatalf("credential leaked into daemon complete output: %q", output)
		}
	}
	if !strings.Contains(output, RedactionMarker) {
		t.Fatalf("output missing redaction marker: %q", output)
	}

	// FailTask: error text quoting a password must be redacted.
	err = port.FailTask(ctx, TaskFailure{
		TaskID: testTaskID,
		Error:  `failed after retry with password="hunter2hunter2hunter2"`,
	})
	if err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	failed := recorder.lastBody()
	errText, _ := failed["error"].(string)
	if strings.Contains(errText, "hunter2hunter2hunter2") {
		t.Fatalf("password leaked into daemon fail error: %q", errText)
	}
}

func TestDaemonPortValidatesTaskIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	port := NewDaemonLifecyclePort(daemon.NewClient(server.URL))
	ctx := context.Background()

	for _, err := range []error{
		port.StartTask(ctx, "not-a-uuid"),
		port.CompleteTask(ctx, TaskCompletion{TaskID: "task_9"}),
		port.FailTask(ctx, TaskFailure{TaskID: "--flag"}),
		port.AckTaskCancelled(ctx, ""),
	} {
		if err == nil {
			t.Fatal("invalid hivecrew task id must be rejected")
		}
	}
}
