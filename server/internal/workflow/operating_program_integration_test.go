//go:build integration

package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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

func integrationDatabasePool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func integrationExistingProject(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var workspaceID, projectID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT workspace_id, id FROM project ORDER BY workspace_id, id LIMIT 1`).Scan(&workspaceID, &projectID); err != nil {
		t.Skipf("an existing Project is required: %v", err)
	}
	return workspaceID, projectID
}

// Assignment and Program deletion use the same Program row lock. Regardless
// of which transaction wins, no mapping may remain for the deleted Program.
func TestOperatingProgramAssignmentDeleteRaceNoOrphan(t *testing.T) {
	pool, ctx := integrationDatabasePool(t)
	workspaceID, projectID := integrationExistingProject(t, ctx, pool)
	programID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_operating_program (id, workspace_id, name, description, idempotency_key)
		VALUES ($1, $2, 'race-test', '', $3)`, programID, workspaceID, "race-"+programID.String()); err != nil {
		t.Fatalf("insert program: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM workflow_operating_program_project WHERE program_id = $1`, programID)
		_, _ = pool.Exec(ctx, `DELETE FROM workflow_operating_program WHERE id = $1`, programID)
	})

	start := make(chan struct{})
	type result struct {
		name    string
		changed bool
		err     error
	}
	results := make(chan result, 2)
	go func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			results <- result{name: "assign", err: err}
			return
		}
		defer tx.Rollback(ctx)
		<-start
		changed, err := NewOperatingProgramRepository(db.New(tx)).AssignExistingProject(ctx, workspaceID.String(), programID.String(), projectID.String())
		if err == nil {
			err = tx.Commit(ctx)
		}
		results <- result{name: "assign", changed: changed, err: err}
	}()
	go func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			results <- result{name: "delete", err: err}
			return
		}
		defer tx.Rollback(ctx)
		<-start
		err = NewOperatingProgramRepository(db.New(tx)).Delete(ctx, workspaceID.String(), programID.String())
		if err == nil {
			err = tx.Commit(ctx)
		}
		results <- result{name: "delete", err: err}
	}()
	close(start)
	first, second := <-results, <-results
	for _, got := range []result{first, second} {
		if got.name == "assign" && got.err != nil && !errors.Is(got.err, ErrOperatingProgramNotFound) {
			t.Fatalf("assignment race error: %v", got.err)
		}
		if got.name == "delete" && got.err != nil && !errors.Is(got.err, ErrOperatingProgramNotFound) {
			t.Fatalf("delete race error: %v", got.err)
		}
	}
	var mappingCount, programCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_operating_program_project WHERE program_id = $1`, programID).Scan(&mappingCount); err != nil {
		t.Fatalf("mapping count: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_operating_program WHERE id = $1`, programID).Scan(&programCount); err != nil {
		t.Fatalf("program count: %v", err)
	}
	if mappingCount != 0 || programCount != 0 {
		t.Fatalf("race left state: mapping_count=%d program_count=%d", mappingCount, programCount)
	}
}

func TestOperatingProgramConcurrentDeleteReturnsNotFound(t *testing.T) {
	pool, ctx := integrationDatabasePool(t)
	workspaceID, _ := integrationExistingProject(t, ctx, pool)
	programID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_operating_program (id, workspace_id, name, description, idempotency_key)
		VALUES ($1, $2, 'double-delete-test', '', $3)`, programID, workspaceID, "double-delete-"+programID.String()); err != nil {
		t.Fatalf("insert program: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM workflow_operating_program_project WHERE program_id = $1`, programID)
		_, _ = pool.Exec(ctx, `DELETE FROM workflow_operating_program WHERE id = $1`, programID)
	})

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			tx, err := pool.Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			defer tx.Rollback(ctx)
			<-start
			err = NewOperatingProgramRepository(db.New(tx)).Delete(ctx, workspaceID.String(), programID.String())
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- err
		}()
	}
	close(start)
	resultsSeen := []error{<-results, <-results}
	successes, notFound := 0, 0
	for _, err := range resultsSeen {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrOperatingProgramNotFound):
			notFound++
		default:
			t.Fatalf("concurrent delete error: %v", err)
		}
	}
	if successes != 1 || notFound != 1 {
		t.Fatalf("concurrent delete outcomes: successes=%d not_found=%d", successes, notFound)
	}
}

// This mirrors the native Project delete transaction: lock the Project,
// clear HiveCrew's mapping, delete the Project, then commit together.
func TestNativeProjectDeleteCleanupLeavesNoOperatingProgramMapping(t *testing.T) {
	pool, ctx := integrationDatabasePool(t)
	workspaceID, _ := integrationExistingProject(t, ctx, pool)
	var projectID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id`, workspaceID, "operating-program-delete-test").Scan(&projectID); err != nil {
		t.Fatalf("insert temporary project: %v", err)
	}
	programID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_operating_program (id, workspace_id, name, description, idempotency_key)
		VALUES ($1, $2, 'project-delete-test', '', $3)`, programID, workspaceID, "project-delete-"+programID.String()); err != nil {
		t.Fatalf("insert program: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM workflow_operating_program_project WHERE program_id = $1`, programID)
		_, _ = pool.Exec(ctx, `DELETE FROM workflow_operating_program WHERE id = $1`, programID)
		_, _ = pool.Exec(ctx, `DELETE FROM project WHERE id = $1 AND workspace_id = $2`, projectID, workspaceID)
	})

	if changed, err := NewOperatingProgramRepository(db.New(pool)).AssignExistingProject(ctx, workspaceID.String(), programID.String(), projectID.String()); err != nil || !changed {
		t.Fatalf("assign temporary project changed=%v err=%v", changed, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin native delete: %v", err)
	}
	qtx := db.New(tx)
	if _, err := qtx.LockProjectForDelete(ctx, db.LockProjectForDeleteParams{ID: pgUUID(projectID), WorkspaceID: pgUUID(workspaceID)}); err != nil {
		t.Fatalf("lock temporary project: %v", err)
	}
	if err := qtx.DeleteWorkflowOperatingProgramProjectsByProject(ctx, db.DeleteWorkflowOperatingProgramProjectsByProjectParams{WorkspaceID: pgUUID(workspaceID), ProjectID: pgUUID(projectID)}); err != nil {
		t.Fatalf("clear project mapping: %v", err)
	}
	if err := qtx.DeleteProject(ctx, db.DeleteProjectParams{ID: pgUUID(projectID), WorkspaceID: pgUUID(workspaceID)}); err != nil {
		t.Fatalf("delete temporary project: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit native delete: %v", err)
	}
	var mappingCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow_operating_program_project WHERE project_id = $1`, projectID).Scan(&mappingCount); err != nil {
		t.Fatalf("mapping count: %v", err)
	}
	if mappingCount != 0 {
		t.Fatalf("project delete left %d operating program mappings", mappingCount)
	}
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}
