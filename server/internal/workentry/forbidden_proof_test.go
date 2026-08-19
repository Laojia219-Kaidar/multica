package workentry

import (
	"errors"
	"testing"
)

func TestRejectForbiddenProofFields(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		found bool
	}{
		{"top-level", `{"task_id":"x"}`, true},
		{"nested intent", `{"intent":{"run_id":"x"}}`, true},
		{"deep event payload", `{"event":{"event_payload":{"artifact":{"formal_artifact_ref":"x"}}}}`, true},
		{"array element", `{"items":[{"candidate_id":"x"}]}`, true},
		{"clean body", `{"actor_identity":{"actor_type":"external_agent"},"intent":{"goal_ref":"G1"}}`, false},
		{"not json", `not-json`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := RejectForbiddenProofFields([]byte(c.body))
			got := errors.Is(err, ErrForbiddenProofField)
			if got != c.found {
				t.Fatalf("body=%s -> found=%v (err=%v), want %v", c.body, got, err, c.found)
			}
		})
	}
}

func TestForbiddenProofFieldsList(t *testing.T) {
	if len(ForbiddenProofFields) != 12 {
		t.Fatalf("want 12 forbidden fields, got %d", len(ForbiddenProofFields))
	}
	seen := map[string]bool{}
	for _, k := range ForbiddenProofFields {
		if seen[k] {
			t.Fatalf("duplicate forbidden field %q", k)
		}
		seen[k] = true
	}
}

func TestRejectForbiddenProofFieldsForEventAllowsOnlyRootRunID(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "typed root run id", body: `{"run_id":"run-1","event_payload":{"step":"verify"}}`},
		{name: "nested run id", body: `{"run_id":"run-1","event_payload":{"run_id":"forged"}}`, wantErr: true},
		{name: "other proof remains forbidden", body: `{"run_id":"run-1","event_payload":{"task_id":"forged"}}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RejectForbiddenProofFieldsForEvent([]byte(tc.body))
			if got := errors.Is(err, ErrForbiddenProofField); got != tc.wantErr {
				t.Fatalf("forbidden error = %v, want %v; err=%v", got, tc.wantErr, err)
			}
		})
	}
}

func TestRejectForbiddenProofFieldsForSyncAllowsOnlyTypedEventRunID(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "CLI event entry", body: `[{"verb":"event","canonical_payload":{"event":{"run_id":"run-1"}}}]`},
		{name: "HTTP event entry", body: `{"entries":[{"verb":"event","canonical_payload":{"event":{"run_id":"run-1"}}}]}`},
		{name: "register smuggling", body: `{"entries":[{"verb":"register","canonical_payload":{"event":{"run_id":"forged"}}}]}`, wantErr: true},
		{name: "event payload smuggling", body: `{"entries":[{"verb":"event","canonical_payload":{"event":{"event_payload":{"run_id":"forged"}}}}]}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RejectForbiddenProofFieldsForSync([]byte(tc.body))
			if got := errors.Is(err, ErrForbiddenProofField); got != tc.wantErr {
				t.Fatalf("forbidden error = %v, want %v; err=%v", got, tc.wantErr, err)
			}
		})
	}
}

func TestRejectForbiddenProofFieldsForMCPCallAllowsOnlyTypedEventRunID(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "work event", body: `{"name":"work.event","arguments":{"run_id":"run-1"}}`},
		{name: "work sync event", body: `{"name":"work.sync","arguments":{"entries":[{"verb":"event","canonical_payload":{"event":{"run_id":"run-1"}}}]}}`},
		{name: "wrong tool", body: `{"name":"work.register","arguments":{"run_id":"forged"}}`, wantErr: true},
		{name: "nested payload", body: `{"name":"work.event","arguments":{"event_payload":{"run_id":"forged"}}}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RejectForbiddenProofFieldsForMCPCall([]byte(tc.body))
			if got := errors.Is(err, ErrForbiddenProofField); got != tc.wantErr {
				t.Fatalf("forbidden error = %v, want %v; err=%v", got, tc.wantErr, err)
			}
		})
	}
}
