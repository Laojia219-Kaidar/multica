//go:build integration

package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This integration fixture intentionally uses existing Projects from two
// workspaces. It proves the cross-workspace fail-closed boundary without
// manufacturing or modifying Project lifecycle rows.
func TestOperatingProgramProjectAssignmentWorkspaceBoundary(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
		SELECT workspace_id, id FROM project
		ORDER BY workspace_id, id
		LIMIT 2`)
	if err != nil {
		t.Fatalf("projects: %v", err)
	}
	defer rows.Close()
	type projectRef struct{ workspace, project uuid.UUID }
	refs := make([]projectRef, 0, 2)
	for rows.Next() {
		var ref projectRef
		if err := rows.Scan(&ref.workspace, &ref.project); err != nil {
			t.Fatalf("scan project: %v", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("project rows: %v", err)
	}
	if len(refs) < 2 || refs[0].workspace == refs[1].workspace {
		t.Skip("two workspaces with existing projects are required")
	}

	q := db.New(pool)
	programA, programB := uuid.New(), uuid.New()
	for _, fixture := range []struct{ id, workspace uuid.UUID }{{programA, refs[0].workspace}, {programB, refs[1].workspace}} {
		_, err := pool.Exec(ctx, `
			INSERT INTO workflow_operating_program (id, workspace_id, name, description, idempotency_key)
			VALUES ($1, $2, $3, '', $4)`, fixture.id, fixture.workspace,
			fmt.Sprintf("itest-%s", fixture.id), fmt.Sprintf("itest-key-%s", fixture.id))
		if err != nil {
			t.Fatalf("insert program: %v", err)
		}
		defer pool.Exec(ctx, `DELETE FROM workflow_operating_program_project WHERE program_id = $1`, fixture.id)
		defer pool.Exec(ctx, `DELETE FROM workflow_operating_program WHERE id = $1`, fixture.id)
	}

	repo := NewOperatingProgramRepository(q)
	changed, err := repo.AssignExistingProject(ctx, refs[0].workspace.String(), programA.String(), refs[0].project.String())
	if err != nil || !changed {
		t.Fatalf("same-workspace assignment changed=%v err=%v", changed, err)
	}
	changed, err = repo.AssignExistingProject(ctx, refs[0].workspace.String(), programA.String(), refs[0].project.String())
	if err != nil || changed {
		t.Fatalf("idempotent assignment changed=%v err=%v", changed, err)
	}
	if _, err := repo.AssignExistingProject(ctx, refs[1].workspace.String(), programB.String(), refs[0].project.String()); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("cross-workspace project assignment err=%v, want ErrProjectNotFound", err)
	}
}
