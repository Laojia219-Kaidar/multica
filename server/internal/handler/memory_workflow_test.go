package handler

import (
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/memory"
	"github.com/multica-ai/multica/server/internal/workflow"
)

func TestToMemoryCandidateDTO(t *testing.T) {
	c := memory.MemoryCandidate{
		ID:         "c-1",
		EmployeeID: "e-1",
		PositionID: "p-1",
		Kind:       memory.KindExperience,
		Content:    "reflection",
		Evidence:   []memory.EvidenceRef{{Type: "run", ID: "r-1"}, {Type: "task", ID: "t-1"}},
		SourceRefs: []string{"agent://e-1"},
		AuthorID:   "a-1",
		CreatedAt:  time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC),
		Status:     memory.StatusPending,
	}
	d := toMemoryCandidateDTO(c)
	if d.ID != "c-1" || d.Kind != "experience" || d.Status != "pending" {
		t.Fatalf("unexpected DTO: %+v", d)
	}
	if len(d.Evidence) != 2 || d.Evidence[0].Type != "run" {
		t.Fatalf("evidence not converted: %+v", d.Evidence)
	}
	if d.CreatedAt != "2026-08-13T00:00:00Z" {
		t.Fatalf("created_at not RFC3339: %q", d.CreatedAt)
	}
}

func TestToWorkflowInstanceDTO(t *testing.T) {
	i := workflow.WorkflowInstance{
		ID:                "i-1",
		DefinitionID:      "hivecrew.project-lifecycle",
		DefinitionVersion: 1,
		Context:           workflow.ContextRef{ProjectID: "proj-1", IssueID: "iss-1"},
		StageIndex:        2,
		Status:            workflow.StatusRunning,
	}
	d := toWorkflowInstanceDTO(i)
	if d.ID != "i-1" || d.DefinitionID != "hivecrew.project-lifecycle" || d.Status != "running" {
		t.Fatalf("unexpected DTO: %+v", d)
	}
	if d.Context.ProjectID != "proj-1" || d.Context.IssueID != "iss-1" {
		t.Fatalf("context not converted: %+v", d.Context)
	}
}
