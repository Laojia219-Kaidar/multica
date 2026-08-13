package workflow

import (
	"context"
	"encoding/json"
	"time"

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

func (r *Repository) UpdateInstance(ctx context.Context, i WorkflowInstance) error {
	return r.Q.UpdateWorkflowInstance(ctx, db.UpdateWorkflowInstanceParams{
		ID: i.ID, StageIndex: int32(i.StageIndex), Status: string(i.Status),
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
		ID: row.ID, DefinitionID: row.DefinitionID, DefinitionVersion: int(row.DefinitionVersion),
		Context: ctxRef, StageIndex: int(row.StageIndex), Status: InstanceStatus(row.Status),
	}, nil
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
