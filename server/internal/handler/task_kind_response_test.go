package handler

import (
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskToResponseCarriesAuthoritativeTaskKind(t *testing.T) {
	got := taskToResponse(db.AgentTaskQueue{TaskKind: "repair"}, "workspace-1")
	if got.TaskKind != "repair" {
		t.Fatalf("task_kind = %q, want repair", got.TaskKind)
	}
}
