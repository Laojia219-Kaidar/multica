package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// stageJSON is the persisted form of a Stage (SLA stored as nanoseconds).
type stageJSON struct {
	Name  string `json:"name"`
	SLANs int64  `json:"sla_ns"`
}

func marshalStages(stages []Stage) ([]byte, error) {
	out := make([]stageJSON, 0, len(stages))
	for _, s := range stages {
		out = append(out, stageJSON{Name: s.Name, SLANs: int64(s.SLA)})
	}
	return json.Marshal(out)
}

func unmarshalStages(b []byte) ([]Stage, error) {
	var in []stageJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	out := make([]Stage, 0, len(in))
	for _, s := range in {
		out = append(out, Stage{Name: s.Name, SLA: time.Duration(s.SLANs)})
	}
	return out, nil
}

func marshalContext(c ContextRef) ([]byte, error) { return json.Marshal(c) }

func unmarshalContext(b []byte) (ContextRef, error) {
	var c ContextRef
	if len(b) == 0 {
		return c, nil
	}
	return c, json.Unmarshal(b, &c)
}

func tsToPG(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()} }
func pgToTS(t pgtype.Timestamptz) time.Time {
	if t.Valid {
		return t.Time
	}
	return time.Time{}
}

// Repository persists workflow engine state through the sqlc queries.
type Repository struct {
	Q *db.Queries
}

func NewRepository(q *db.Queries) *Repository { return &Repository{Q: q} }

func marshalGraph(g WorkflowGraph) ([]byte, error) { return json.Marshal(g) }

func unmarshalGraph(b []byte) (WorkflowGraph, error) {
	var g WorkflowGraph
	if err := json.Unmarshal(b, &g); err != nil {
		return WorkflowGraph{}, err
	}
	return g, nil
}

func versionDigest(v WorkflowDefinitionVersion, stages, graph []byte) string {
	// The digest covers the immutable semantic payload, not timestamps or the
	// idempotency key, so the same published graph is reproducible.
	payload := struct {
		DefinitionID string          `json:"definition_id"`
		WorkspaceID  string          `json:"workspace_id"`
		Version      int             `json:"version"`
		Risk         RiskTier        `json:"risk"`
		Stages       json.RawMessage `json:"stages"`
		Graph        json.RawMessage `json:"graph"`
	}{v.DefinitionID, v.WorkspaceID, v.Version, v.Risk, stages, graph}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func workflowVersionFromRow(row db.WorkflowDefinitionVersion) (WorkflowDefinitionVersion, error) {
	stages, err := unmarshalStages(row.Stages)
	if err != nil {
		return WorkflowDefinitionVersion{}, fmt.Errorf("decode workflow version stages: %w", err)
	}
	graph, err := unmarshalGraph(row.Graph)
	if err != nil {
		return WorkflowDefinitionVersion{}, fmt.Errorf("decode workflow version graph: %w", err)
	}
	return WorkflowDefinitionVersion{
		DefinitionID: row.DefinitionID,
		WorkspaceID:  uuidString(row.WorkspaceID),
		Version:      int(row.Version),
		Risk:         RiskTier(row.Risk),
		Stages:       stages,
		Graph:        graph,
		Digest:       row.Digest,
		CreatedAt:    pgToTS(row.CreatedAt),
		PublishedAt:  pgToTS(row.PublishedAt),
	}, nil
}

// PublishDefinitionVersion persists one immutable version. Existing
// idempotency keys return the original row, while a new key receives the next
// version number within the workspace/definition. The endpoint validates the
// graph before calling this method; the repository repeats validation so other
// callers cannot bypass the publication gate.
func (r *Repository) PublishDefinitionVersion(ctx context.Context, v WorkflowDefinitionVersion, idempotencyKey string) (WorkflowDefinitionVersion, bool, error) {
	if idempotencyKey == "" {
		return WorkflowDefinitionVersion{}, false, fmt.Errorf("idempotency key is required")
	}
	if err := v.ValidatePublishedGraph(); err != nil {
		return WorkflowDefinitionVersion{}, false, err
	}
	ws, err := workspaceUUID(v.WorkspaceID)
	if err != nil {
		return WorkflowDefinitionVersion{}, false, err
	}
	if row, err := r.Q.GetWorkflowDefinitionVersionByIdempotency(ctx, db.GetWorkflowDefinitionVersionByIdempotencyParams{WorkspaceID: ws, IdempotencyKey: idempotencyKey}); err == nil {
		out, convErr := workflowVersionFromRow(row)
		return out, false, convErr
	}
	latest, err := r.Q.GetLatestWorkflowDefinitionVersion(ctx, db.GetLatestWorkflowDefinitionVersionParams{WorkspaceID: ws, DefinitionID: v.DefinitionID})
	next := 1
	if err == nil {
		next = int(latest.Version) + 1
	}
	v.Version = next
	stages, err := marshalStages(v.Stages)
	if err != nil {
		return WorkflowDefinitionVersion{}, false, err
	}
	graph, err := marshalGraph(v.Graph)
	if err != nil {
		return WorkflowDefinitionVersion{}, false, err
	}
	v.Digest = versionDigest(v, stages, graph)
	row, err := r.Q.InsertWorkflowDefinitionVersion(ctx, db.InsertWorkflowDefinitionVersionParams{
		DefinitionID: v.DefinitionID, WorkspaceID: ws, Version: int32(v.Version),
		Risk: string(v.Risk), Stages: stages, Graph: graph, Digest: v.Digest,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		// A concurrent publisher may have won the idempotency key. Return its
		// durable receipt rather than exposing a transient duplicate failure.
		if existing, lookupErr := r.Q.GetWorkflowDefinitionVersionByIdempotency(ctx, db.GetWorkflowDefinitionVersionByIdempotencyParams{WorkspaceID: ws, IdempotencyKey: idempotencyKey}); lookupErr == nil {
			out, convErr := workflowVersionFromRow(existing)
			return out, false, convErr
		}
		return WorkflowDefinitionVersion{}, false, err
	}
	out, err := workflowVersionFromRow(row)
	return out, true, err
}

func (r *Repository) ListPublishedDefinitionVersions(ctx context.Context, workspaceID string, latestOnly bool) ([]WorkflowDefinitionVersion, error) {
	ws, err := workspaceUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	var rows []db.WorkflowDefinitionVersion
	if latestOnly {
		rows, err = r.Q.ListLatestWorkflowDefinitionVersions(ctx, ws)
	} else {
		rows, err = r.Q.ListWorkflowDefinitionVersions(ctx, ws)
	}
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowDefinitionVersion, 0, len(rows))
	for _, row := range rows {
		v, convErr := workflowVersionFromRow(row)
		if convErr != nil {
			return nil, convErr
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *Repository) LoadPublishedDefinitionVersion(ctx context.Context, workspaceID, definitionID string, version int) (WorkflowDefinitionVersion, error) {
	ws, err := workspaceUUID(workspaceID)
	if err != nil {
		return WorkflowDefinitionVersion{}, err
	}
	row, err := r.Q.GetWorkflowDefinitionVersion(ctx, db.GetWorkflowDefinitionVersionParams{WorkspaceID: ws, DefinitionID: definitionID, Version: int32(version)})
	if err != nil {
		return WorkflowDefinitionVersion{}, err
	}
	return workflowVersionFromRow(row)
}

func workspaceUUID(workspaceID string) (pgtype.UUID, error) {
	id, err := uuid.Parse(workspaceID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid workflow workspace id %q: %w", workspaceID, err)
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func (r *Repository) SaveDefinition(ctx context.Context, d WorkflowDefinition) error {
	stages, err := marshalStages(d.Stages)
	if err != nil {
		return err
	}
	return r.Q.InsertWorkflowDefinition(ctx, db.InsertWorkflowDefinitionParams{
		ID: d.ID, Version: int32(d.Version), Risk: string(d.Risk), Stages: stages,
	})
}

func (r *Repository) LoadDefinition(ctx context.Context, id string) (WorkflowDefinition, error) {
	row, err := r.Q.GetWorkflowDefinition(ctx, id)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	stages, err := unmarshalStages(row.Stages)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	return WorkflowDefinition{ID: row.ID, Version: int(row.Version), Risk: RiskTier(row.Risk), Stages: stages}, nil
}

func (r *Repository) SaveInstance(ctx context.Context, i WorkflowInstance) error {
	ctxJSON, err := marshalContext(i.Context)
	if err != nil {
		return err
	}
	return r.Q.InsertWorkflowInstance(ctx, db.InsertWorkflowInstanceParams{
		ID: i.ID, DefinitionID: i.DefinitionID, DefinitionVersion: int32(i.DefinitionVersion),
		Context: ctxJSON, StageIndex: int32(i.StageIndex), Status: string(i.Status),
	})
}

// SaveInstanceInWorkspace persists a new or existing instance while requiring
// an explicit workspace scope. The upsert predicate prevents an instance ID
// from being updated through a different workspace.
func (r *Repository) SaveInstanceInWorkspace(ctx context.Context, workspaceID string, i WorkflowInstance) error {
	ws, err := workspaceUUID(workspaceID)
	if err != nil {
		return err
	}
	ctxJSON, err := marshalContext(i.Context)
	if err != nil {
		return err
	}
	i.WorkspaceID = workspaceID
	return r.Q.InsertWorkflowInstanceInWorkspace(ctx, db.InsertWorkflowInstanceInWorkspaceParams{
		WorkspaceID: ws, ID: i.ID, DefinitionID: i.DefinitionID,
		DefinitionVersion: int32(i.DefinitionVersion), Context: ctxJSON,
		StageIndex: int32(i.StageIndex), Status: string(i.Status),
	})
}

func (r *Repository) UpdateInstance(ctx context.Context, i WorkflowInstance) error {
	return r.Q.UpdateWorkflowInstance(ctx, db.UpdateWorkflowInstanceParams{
		ID: i.ID, StageIndex: int32(i.StageIndex), Status: string(i.Status),
	})
}

func (r *Repository) UpdateInstanceInWorkspace(ctx context.Context, workspaceID string, i WorkflowInstance) error {
	ws, err := workspaceUUID(workspaceID)
	if err != nil {
		return err
	}
	return r.Q.UpdateWorkflowInstanceInWorkspace(ctx, db.UpdateWorkflowInstanceInWorkspaceParams{
		WorkspaceID: ws, ID: i.ID, StageIndex: int32(i.StageIndex), Status: string(i.Status),
	})
}

func (r *Repository) LoadInstance(ctx context.Context, id string) (WorkflowInstance, error) {
	row, err := r.Q.GetWorkflowInstance(ctx, id)
	if err != nil {
		return WorkflowInstance{}, err
	}
	ctxRef, err := unmarshalContext(row.Context)
	if err != nil {
		return WorkflowInstance{}, err
	}
	return WorkflowInstance{
		ID: row.ID, WorkspaceID: uuidString(row.WorkspaceID), DefinitionID: row.DefinitionID, DefinitionVersion: int(row.DefinitionVersion),
		Context: ctxRef, StageIndex: int(row.StageIndex), Status: InstanceStatus(row.Status),
	}, nil
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

func workflowInstanceFromRow(row db.WorkflowInstance) (WorkflowInstance, error) {
	ctxRef, err := unmarshalContext(row.Context)
	if err != nil {
		return WorkflowInstance{}, err
	}
	return WorkflowInstance{
		ID: row.ID, WorkspaceID: uuidString(row.WorkspaceID), DefinitionID: row.DefinitionID,
		DefinitionVersion: int(row.DefinitionVersion), Context: ctxRef,
		StageIndex: int(row.StageIndex), Status: InstanceStatus(row.Status),
	}, nil
}

// LoadInstanceInWorkspace is the only scoped instance read used by HTTP
// handlers. A matching ID in another workspace is intentionally invisible.
func (r *Repository) LoadInstanceInWorkspace(ctx context.Context, workspaceID, id string) (WorkflowInstance, error) {
	ws, err := workspaceUUID(workspaceID)
	if err != nil {
		return WorkflowInstance{}, err
	}
	row, err := r.Q.GetWorkflowInstanceInWorkspace(ctx, db.GetWorkflowInstanceInWorkspaceParams{WorkspaceID: ws, ID: id})
	if err != nil {
		return WorkflowInstance{}, err
	}
	return workflowInstanceFromRow(row)
}

func (r *Repository) ListInstances(ctx context.Context, workspaceID string) ([]WorkflowInstance, error) {
	ws, err := workspaceUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := r.Q.ListWorkflowInstances(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowInstance, 0, len(rows))
	for _, row := range rows {
		inst, err := workflowInstanceFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, nil
}

// AppendEvent inserts an event; duplicate (instance_id, idempotency_key) is a
// no-op (idempotent append), matching the engine's in-memory dedup.
func (r *Repository) AppendEvent(ctx context.Context, ev Event) error {
	return r.Q.InsertWorkflowEvent(ctx, db.InsertWorkflowEventParams{
		InstanceID: ev.InstanceID, Kind: ev.Kind, SourceRef: ev.SourceRef, Actor: ev.Actor,
		OccurredAt: tsToPG(ev.OccurredAt), ObservedAt: tsToPG(ev.ObservedAt),
		IdempotencyKey: ev.IdempotencyKey,
	})
}

func (r *Repository) ListEvents(ctx context.Context, instanceID string) ([]Event, error) {
	rows, err := r.Q.ListWorkflowEvents(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, Event{
			Sequence: row.ID, InstanceID: row.InstanceID, Kind: row.Kind, SourceRef: row.SourceRef,
			Actor: row.Actor, OccurredAt: pgToTS(row.OccurredAt), ObservedAt: pgToTS(row.ObservedAt),
			IdempotencyKey: row.IdempotencyKey,
		})
	}
	return out, nil
}

func (r *Repository) ListEventsInWorkspace(ctx context.Context, workspaceID, instanceID string) ([]Event, error) {
	ws, err := workspaceUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := r.Q.ListWorkflowEventsInWorkspace(ctx, db.ListWorkflowEventsInWorkspaceParams{WorkspaceID: ws, InstanceID: instanceID})
	if err != nil {
		return nil, err
	}
	return workflowEventsFromRows(rows), nil
}

func workflowEventsFromRows(rows []db.WorkflowEvent) []Event {
	out := make([]Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, Event{
			Sequence: row.ID, InstanceID: row.InstanceID, Kind: row.Kind, SourceRef: row.SourceRef,
			Actor: row.Actor, OccurredAt: pgToTS(row.OccurredAt), ObservedAt: pgToTS(row.ObservedAt),
			IdempotencyKey: row.IdempotencyKey,
		})
	}
	return out
}
