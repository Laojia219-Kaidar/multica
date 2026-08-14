package handler

import (
	"net/url"
	"testing"

	"github.com/multica-ai/multica/server/internal/workflow"
)

func TestWorkflowReplayReceiptPreservesClosedControlResult(t *testing.T) {
	reason := "graph stage requires explicit review evidence"
	cases := []struct {
		name     string
		event    workflow.Event
		command  string
		accepted bool
		reason   string
	}{
		{
			name: "started", event: workflow.Event{InstanceID: "instance-1", IdempotencyKey: "start-1", Kind: "workflow.started"},
			command: "start", accepted: true,
		},
		{
			name: "advanced", event: workflow.Event{InstanceID: "instance-1", IdempotencyKey: "advance-1", Kind: "workflow.stage_advanced"},
			command: "advance", accepted: true,
		},
		{
			name: "rejected with persisted reason", event: workflow.Event{InstanceID: "instance-1", IdempotencyKey: "advance-2", Kind: "workflow.advance_rejected", SourceRef: "control://advance?reason=" + url.QueryEscape(reason)},
			command: "advance", accepted: false, reason: reason,
		},
		{
			name: "legacy rejected fallback", event: workflow.Event{InstanceID: "instance-1", IdempotencyKey: "advance-legacy", Kind: "workflow.advance_rejected", SourceRef: "control://advance"},
			command: "advance", accepted: false, reason: "workflow control was previously rejected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			receipt := workflowReplayReceipt(tc.event)
			if receipt.Command != tc.command || receipt.Accepted != tc.accepted || receipt.Changed || receipt.Reason != tc.reason {
				t.Fatalf("replay receipt = %+v", receipt)
			}
			if receipt.InstanceID != tc.event.InstanceID || receipt.IdempotencyKey != tc.event.IdempotencyKey {
				t.Fatalf("replay receipt identity = %+v", receipt)
			}
		})
	}
}
