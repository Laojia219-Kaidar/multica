package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrOperatingProgramNotFound = errors.New("operating program not found")
	ErrProjectNotFound          = errors.New("project not found in workspace")
	ErrProjectAlreadyAssigned   = errors.New("project is already assigned to another operating program")
	ErrOperatingProgramConflict = errors.New("operating program idempotency key conflicts with existing payload")
)

// OperatingProgram is the L3 workflow organization object. ProjectIDs are
// resolved from the mapping ledger and do not duplicate Project fields.
type OperatingProgram struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ProjectIDs  []string  `json:"project_ids"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// OperatingProgramRepository persists only HiveCrew's workflow organization
// projection. It is intentionally backed by sqlc queries so the handler does
// not gain a parallel hand-written SQL authority.
type OperatingProgramRepository struct {
	Q *db.Queries
}

func NewOperatingProgramRepository(q *db.Queries) *OperatingProgramRepository {
	return &OperatingProgramRepository{Q: q}
}

func (r *OperatingProgramRepository) List(ctx context.Context, workspaceID string) ([]OperatingProgram, error) {
	ws, err := operatingProgramUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := r.Q.ListWorkflowOperatingPrograms(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]OperatingProgram, 0, len(rows))
	for _, row := range rows {
		program, err := r.fromRow(ctx, ws, row)
		if err != nil {
			return nil, err
		}
		out = append(out, program)
	}
	return out, nil
}

func (r *OperatingProgramRepository) Get(ctx context.Context, workspaceID, programID string) (OperatingProgram, error) {
	ws, err := operatingProgramUUID(workspaceID)
	if err != nil {
		return OperatingProgram{}, err
	}
	id, err := operatingProgramUUID(programID)
	if err != nil {
		return OperatingProgram{}, err
	}
	row, err := r.Q.GetWorkflowOperatingProgramInWorkspace(ctx, db.GetWorkflowOperatingProgramInWorkspaceParams{WorkspaceID: ws, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatingProgram{}, ErrOperatingProgramNotFound
	}
	if err != nil {
		return OperatingProgram{}, err
	}
	return r.fromRow(ctx, ws, row)
}

func (r *OperatingProgramRepository) Create(ctx context.Context, workspaceID, name, description, idempotencyKey string) (OperatingProgram, bool, error) {
	ws, err := operatingProgramUUID(workspaceID)
	if err != nil {
		return OperatingProgram{}, false, err
	}
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if name == "" || idempotencyKey == "" {
		return OperatingProgram{}, false, fmt.Errorf("name and idempotency_key are required")
	}
	if existing, lookupErr := r.Q.GetWorkflowOperatingProgramByIdempotency(ctx, db.GetWorkflowOperatingProgramByIdempotencyParams{WorkspaceID: ws, IdempotencyKey: idempotencyKey}); lookupErr == nil {
		if existing.Name != name || existing.Description != description {
			return OperatingProgram{}, false, ErrOperatingProgramConflict
		}
		program, convErr := r.fromRow(ctx, ws, existing)
		return program, false, convErr
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return OperatingProgram{}, false, lookupErr
	}

	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	row, err := r.Q.InsertWorkflowOperatingProgram(ctx, db.InsertWorkflowOperatingProgramParams{
		ID: id, WorkspaceID: ws, Name: name, Description: description, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		// A concurrent request may have won the idempotency key. Replay the
		// durable row, but never silently accept a different payload.
		if existing, lookupErr := r.Q.GetWorkflowOperatingProgramByIdempotency(ctx, db.GetWorkflowOperatingProgramByIdempotencyParams{WorkspaceID: ws, IdempotencyKey: idempotencyKey}); lookupErr == nil {
			if existing.Name != name || existing.Description != description {
				return OperatingProgram{}, false, ErrOperatingProgramConflict
			}
			program, convErr := r.fromRow(ctx, ws, existing)
			return program, false, convErr
		}
		return OperatingProgram{}, false, err
	}
	program, err := r.fromRow(ctx, ws, row)
	return program, true, err
}

func (r *OperatingProgramRepository) Update(ctx context.Context, workspaceID, programID, name, description string) (OperatingProgram, error) {
	ws, err := operatingProgramUUID(workspaceID)
	if err != nil {
		return OperatingProgram{}, err
	}
	id, err := operatingProgramUUID(programID)
	if err != nil {
		return OperatingProgram{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return OperatingProgram{}, fmt.Errorf("name is required")
	}
	row, err := r.Q.UpdateWorkflowOperatingProgram(ctx, db.UpdateWorkflowOperatingProgramParams{
		WorkspaceID: ws, ID: id, Name: name, Description: strings.TrimSpace(description),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatingProgram{}, ErrOperatingProgramNotFound
	}
	if err != nil {
		return OperatingProgram{}, err
	}
	return r.fromRow(ctx, ws, row)
}

// AssignExistingProject validates both the Program and Project in the same
// workspace and enforces one Program per Project. Callers should provide a
// transaction-backed repository for the write path.
func (r *OperatingProgramRepository) AssignExistingProject(ctx context.Context, workspaceID, programID, projectID string) (bool, error) {
	ws, err := operatingProgramUUID(workspaceID)
	if err != nil {
		return false, err
	}
	program, err := operatingProgramUUID(programID)
	if err != nil {
		return false, err
	}
	project, err := operatingProgramUUID(projectID)
	if err != nil {
		return false, err
	}
	if _, err := r.Q.GetWorkflowOperatingProgramInWorkspace(ctx, db.GetWorkflowOperatingProgramInWorkspaceParams{WorkspaceID: ws, ID: program}); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrOperatingProgramNotFound
	} else if err != nil {
		return false, err
	}
	if _, err := r.Q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: project, WorkspaceID: ws}); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrProjectNotFound
	} else if err != nil {
		return false, err
	}
	if existing, err := r.Q.GetWorkflowOperatingProgramProject(ctx, db.GetWorkflowOperatingProgramProjectParams{WorkspaceID: ws, ProjectID: project}); err == nil {
		if existing.ProgramID == program {
			return false, nil
		}
		return false, ErrProjectAlreadyAssigned
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if _, err := r.Q.InsertWorkflowOperatingProgramProject(ctx, db.InsertWorkflowOperatingProgramProjectParams{ProgramID: program, WorkspaceID: ws, ProjectID: project}); err != nil {
		if existing, lookupErr := r.Q.GetWorkflowOperatingProgramProject(ctx, db.GetWorkflowOperatingProgramProjectParams{WorkspaceID: ws, ProjectID: project}); lookupErr == nil {
			if existing.ProgramID == program {
				return false, nil
			}
			return false, ErrProjectAlreadyAssigned
		}
		return false, err
	}
	return true, nil
}

func (r *OperatingProgramRepository) UnassignExistingProject(ctx context.Context, workspaceID, programID, projectID string) (bool, error) {
	ws, err := operatingProgramUUID(workspaceID)
	if err != nil {
		return false, err
	}
	program, err := operatingProgramUUID(programID)
	if err != nil {
		return false, err
	}
	project, err := operatingProgramUUID(projectID)
	if err != nil {
		return false, err
	}
	if _, err := r.Q.GetWorkflowOperatingProgramInWorkspace(ctx, db.GetWorkflowOperatingProgramInWorkspaceParams{WorkspaceID: ws, ID: program}); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrOperatingProgramNotFound
	} else if err != nil {
		return false, err
	}
	before, err := r.Q.GetWorkflowOperatingProgramProject(ctx, db.GetWorkflowOperatingProgramProjectParams{WorkspaceID: ws, ProjectID: project})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if before.ProgramID != program {
		return false, ErrProjectNotFound
	}
	if err := r.Q.DeleteWorkflowOperatingProgramProject(ctx, db.DeleteWorkflowOperatingProgramProjectParams{WorkspaceID: ws, ProgramID: program, ProjectID: project}); err != nil {
		return false, err
	}
	return true, nil
}

func (r *OperatingProgramRepository) Delete(ctx context.Context, workspaceID, programID string) error {
	ws, err := operatingProgramUUID(workspaceID)
	if err != nil {
		return err
	}
	id, err := operatingProgramUUID(programID)
	if err != nil {
		return err
	}
	if _, err := r.Q.GetWorkflowOperatingProgramInWorkspace(ctx, db.GetWorkflowOperatingProgramInWorkspaceParams{WorkspaceID: ws, ID: id}); errors.Is(err, pgx.ErrNoRows) {
		return ErrOperatingProgramNotFound
	} else if err != nil {
		return err
	}
	if err := r.Q.DeleteWorkflowOperatingProgramProjects(ctx, db.DeleteWorkflowOperatingProgramProjectsParams{WorkspaceID: ws, ProgramID: id}); err != nil {
		return err
	}
	return r.Q.DeleteWorkflowOperatingProgram(ctx, db.DeleteWorkflowOperatingProgramParams{WorkspaceID: ws, ID: id})
}

func (r *OperatingProgramRepository) fromRow(ctx context.Context, ws pgtype.UUID, row db.WorkflowOperatingProgram) (OperatingProgram, error) {
	projectIDs, err := r.Q.ListWorkflowOperatingProgramProjectIDs(ctx, db.ListWorkflowOperatingProgramProjectIDsParams{WorkspaceID: ws, ProgramID: row.ID})
	if err != nil {
		return OperatingProgram{}, err
	}
	projects := make([]string, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		projects = append(projects, uuidString(projectID))
	}
	return OperatingProgram{
		ID: uuidString(row.ID), WorkspaceID: uuidString(row.WorkspaceID), Name: row.Name,
		Description: row.Description, ProjectIDs: projects, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func operatingProgramUUID(value string) (pgtype.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil || id.String() != value {
		return pgtype.UUID{}, fmt.Errorf("canonical UUID required: %q", value)
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}
