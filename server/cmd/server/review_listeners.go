package main

import (
	"context"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerReviewListeners hooks into IssueUpdated to drive the ReviewPipelineV2
// acceptance axis (HIV-326 contract C4 / C4-L / C9):
//
//   - issue enters in_review  → resolve candidate lineage; valid lineage
//     creates the idempotent review task and moves review_state NULL → queued;
//     missing/invalid lineage fails closed to owner_decision WITHOUT creating a
//     review task, and publishes review:escalated for Owner/coordinator;
//   - issue re-enters in_review with a pending/revise round → supersede the old
//     candidate's open review task and start a fresh round;
//   - issue leaves in_review → cancel open review tasks and reset review_state.
//
// Every transition is idempotent (guarded state transitions + the partial
// unique index on open review tasks), so bus at-least-once redelivery and
// concurrent consumers collapse into a single outcome.
func registerReviewListeners(bus *events.Bus, svc *service.ReviewPipelineService) {
	ctx := context.Background()

	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		statusChanged, _ := payload["status_changed"].(bool)
		if !statusChanged {
			return
		}
		issue, ok := payload["issue"].(handler.IssueResponse)
		if !ok {
			return
		}
		issueID := parseUUID(issue.ID)
		switch issue.Status {
		case "in_review":
			if err := svc.OnIssueEnteredReview(ctx, issueID); err != nil {
				slog.Error("review listener: issue entered in_review",
					"issue_id", issue.ID, "error", err)
			}
		default:
			prevStatus, _ := payload["prev_status"].(string)
			if prevStatus == "in_review" {
				if err := svc.OnIssueLeftReview(ctx, issueID); err != nil {
					slog.Error("review listener: issue left in_review",
						"issue_id", issue.ID, "error", err)
				}
			}
		}
	})
}
