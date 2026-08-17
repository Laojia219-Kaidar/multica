package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestFilterProjectResourcesForWorkspaceRejectsForeignRows(t *testing.T) {
	projectID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	workspaceID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	rows := []db.ProjectResource{
		{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, ProjectID: projectID, WorkspaceID: workspaceID},
		{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, ProjectID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, WorkspaceID: workspaceID},
		{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, ProjectID: projectID, WorkspaceID: pgtype.UUID{Bytes: uuid.New(), Valid: true}},
	}
	filtered := filterProjectResourcesForWorkspace(rows, projectID, workspaceID)
	if len(filtered) != 1 || filtered[0].ID != rows[0].ID {
		t.Fatalf("filtered resources=%+v, want only authoritative row", filtered)
	}
}
