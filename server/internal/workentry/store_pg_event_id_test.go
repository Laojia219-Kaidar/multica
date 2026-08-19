package workentry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type eventIDExecutor struct {
	insertedID string
	event      EventRecord
	execErr    error
}

func (e *eventIDExecutor) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(sql, "INSERT INTO work_event (id, workspace_id") {
		panic("work_event insert must persist the service-issued event id")
	}
	if len(args) != 12 {
		panic("work_event insert must receive 12 arguments")
	}
	e.insertedID, _ = args[0].(string)
	if e.execErr != nil {
		return pgconn.CommandTag{}, e.execErr
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (e *eventIDExecutor) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("unexpected Query call")
}

func (e *eventIDExecutor) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if !strings.Contains(sql, "SELECT replace(id::text, '-', ''), work_ref") {
		panic("work_event read must normalize PostgreSQL UUID text to the service event-id format")
	}
	return eventIDRow{executor: e}
}

type eventIDRow struct {
	executor *eventIDExecutor
}

func (r eventIDRow) Scan(dest ...any) error {
	e := r.executor.event
	payload, _ := json.Marshal(e.EventPayload)
	sessionID := e.SessionID
	occurredAt := pgtype.Timestamptz{Time: time.Date(2026, 8, 19, 4, 8, 30, 0, time.UTC), Valid: true}
	observedAt := occurredAt

	*dest[0].(*string) = r.executor.insertedID
	*dest[1].(*string) = e.WorkRef
	*dest[2].(**string) = &sessionID
	*dest[3].(**string) = nil
	*dest[4].(*string) = string(e.EventType)
	*dest[5].(*[]byte) = payload
	*dest[6].(**string) = nil
	*dest[7].(**string) = nil
	*dest[8].(*string) = e.IdempotencyKey
	*dest[9].(*pgtype.Timestamptz) = occurredAt
	*dest[10].(*pgtype.Timestamptz) = observedAt
	return nil
}

func TestPGStoreAppendEventPersistsAndReturnsEventID(t *testing.T) {
	event := EventRecord{
		WorkspaceID:    "1b2a1f07-3050-4d47-aca5-6e6fdbd393d9",
		WorkRef:        "hivecrew://1b2a1f07-3050-4d47-aca5-6e6fdbd393d9/work/project/issue",
		SessionID:      "session-1",
		EventType:      EventProgress,
		EventPayload:   map[string]any{"milestone": "live_api"},
		IdempotencyKey: "progress-1",
		OccurredAt:     "2026-08-19T04:08:30Z",
		ObservedAt:     "2026-08-19T04:08:30Z",
	}
	exec := &eventIDExecutor{event: event}
	store := &PGStore{exec: exec}

	stored, err := store.AppendEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if stored.ID == "" || stored.ID != exec.insertedID {
		t.Fatalf("stored event id = %q, inserted id = %q", stored.ID, exec.insertedID)
	}
}

func TestServiceEventDoesNotMisclassifyFirstPGInsertAsReplay(t *testing.T) {
	workRef := "hivecrew://1b2a1f07-3050-4d47-aca5-6e6fdbd393d9/work/project/issue"
	event := WorkEventV1{
		WorkRef:        workRef,
		SessionID:      "session-1",
		EventType:      EventProgress,
		EventPayload:   map[string]any{"milestone": "live_api"},
		IdempotencyKey: "progress-1",
		OccurredAt:     "2026-08-19T04:08:30Z",
		ObservedAt:     "2026-08-19T04:08:30Z",
	}
	exec := &eventIDExecutor{event: EventRecord{
		WorkspaceID:    "1b2a1f07-3050-4d47-aca5-6e6fdbd393d9",
		WorkRef:        workRef,
		SessionID:      event.SessionID,
		EventType:      event.EventType,
		EventPayload:   event.EventPayload,
		IdempotencyKey: event.IdempotencyKey,
		OccurredAt:     event.OccurredAt,
		ObservedAt:     event.ObservedAt,
	}}
	svc := NewService(&PGStore{exec: exec})

	result, err := svc.Event(context.Background(), event)
	if err != nil {
		t.Fatalf("Event: %v", err)
	}
	if result.EventID == "" || result.Replayed {
		t.Fatalf("first PG event = %+v, want non-empty event_id and replayed=false", result)
	}
}

func TestServiceEventRejectsInvalidCallerEventIDBeforeStore(t *testing.T) {
	svc := NewService(&PGStore{exec: &eventIDExecutor{}})
	_, err := svc.Event(context.Background(), WorkEventV1{
		EventID:        "not-a-uuid",
		WorkRef:        "hivecrew://1b2a1f07-3050-4d47-aca5-6e6fdbd393d9/work/project/issue",
		SessionID:      "session-1",
		EventType:      EventProgress,
		EventPayload:   map[string]any{"milestone": "live_api"},
		IdempotencyKey: "progress-1",
		OccurredAt:     "2026-08-19T04:08:30Z",
		ObservedAt:     "2026-08-19T04:08:30Z",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid caller event_id error = %v, want ErrInvalidRequest", err)
	}
}

func TestPGStoreEventIDPrimaryKeyCollisionIsConflict(t *testing.T) {
	event := EventRecord{
		ID:             "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkspaceID:    "1b2a1f07-3050-4d47-aca5-6e6fdbd393d9",
		WorkRef:        "hivecrew://1b2a1f07-3050-4d47-aca5-6e6fdbd393d9/work/project/issue",
		SessionID:      "session-1",
		EventType:      EventProgress,
		EventPayload:   map[string]any{"milestone": "live_api"},
		IdempotencyKey: "different-key",
		OccurredAt:     "2026-08-19T04:08:30Z",
		ObservedAt:     "2026-08-19T04:08:30Z",
	}
	store := &PGStore{exec: &eventIDExecutor{
		event:   event,
		execErr: &pgconn.PgError{Code: "23505", ConstraintName: "work_event_pkey"},
	}}
	if _, err := store.AppendEvent(context.Background(), event); !errors.Is(err, ErrConflict) {
		t.Fatalf("event id collision error = %v, want ErrConflict", err)
	}
}
