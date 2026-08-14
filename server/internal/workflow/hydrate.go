package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (e *Engine) hydratePublishedGraphDefinition(version WorkflowDefinitionVersion, scope GraphExecutionScope, ctx ContextRef) (WorkflowDefinition, error) {
	plan, err := CompileDefinitionVersion(version, scope, ctx)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	if err := e.registerGraphPlan(plan); err != nil {
		return WorkflowDefinition{}, err
	}
	return plan.Definition, nil
}

func (e *Engine) hydrateDefinition(ctx context.Context, repo *Repository, workspaceID string, inst WorkflowInstance) (WorkflowDefinition, error) {
	if workspaceID != "" && inst.DefinitionVersion > 0 {
		version, err := repo.LoadPublishedDefinitionVersion(ctx, workspaceID, inst.DefinitionID, inst.DefinitionVersion)
		if err == nil {
			return e.hydratePublishedGraphDefinition(version, GraphExecutionScope{
				WorkspaceID: workspaceID,
				ProjectID:   inst.Context.ProjectID,
			}, inst.Context)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return WorkflowDefinition{}, err
		}
	}
	return repo.LoadDefinition(ctx, inst.DefinitionID)
}

// Hydrate reconstructs the engine's in-memory state for one instance from the
// persistence repository. It is the resume-after-restart path: definition,
// instance and the append-only event log are read back so the engine can keep
// driving the workflow. The last event's observed time approximates the
// current stage entry time for SLA purposes.
func (e *Engine) Hydrate(ctx context.Context, repo *Repository, instanceID string) error {
	inst, err := repo.LoadInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	def, err := e.hydrateDefinition(ctx, repo, inst.WorkspaceID, inst)
	if err != nil {
		return err
	}
	evs, err := repo.ListEvents(ctx, instanceID)
	if err != nil {
		return err
	}

	entered := time.Now()
	if len(evs) > 0 {
		entered = evs[len(evs)-1].ObservedAt
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.definitions[def.ID] = def
	e.instances[instanceID] = &inst
	e.events[instanceID] = evs
	e.enteredAt[instanceID] = entered
	return nil
}

// HydrateInWorkspace is the workspace-scoped restart path used by HTTP
// handlers. It loads the instance and event log through the same workspace
// predicate, so an instance ID cannot hydrate data owned by another workspace.
func (e *Engine) HydrateInWorkspace(ctx context.Context, repo *Repository, workspaceID, instanceID string) error {
	inst, err := repo.LoadInstanceInWorkspace(ctx, workspaceID, instanceID)
	if err != nil {
		return err
	}
	def, err := e.hydrateDefinition(ctx, repo, workspaceID, inst)
	if err != nil {
		return err
	}
	evs, err := repo.ListEventsInWorkspace(ctx, workspaceID, instanceID)
	if err != nil {
		return err
	}

	entered := time.Now()
	if len(evs) > 0 {
		entered = evs[len(evs)-1].ObservedAt
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.definitions[def.ID] = def
	e.instances[instanceID] = &inst
	e.events[instanceID] = evs
	e.enteredAt[instanceID] = entered
	return nil
}
