package service

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ReconcileFinding is one detected broken chain (VC-12 diagnostic). The
// reconciler only DIAGNOSES + suggests a traceable action; it never writes.
type ReconcileFinding struct {
	Kind       string  `json:"kind"`
	ProjectID  string  `json:"project_id"`
	IssueID    *string `json:"issue_id,omitempty"`
	Summary    string  `json:"summary"`
	NextAction string  `json:"next_action"`
}

// Reconcile finding kinds (the four VC-12 broken-chain detectors).
const (
	FindingStalledNoTask     = "stalled_no_task"
	FindingReviewNoReviewer  = "review_no_reviewer"
	FindingRepairNoRepair    = "repair_no_repair"
	FindingTerminalNoPackage = "terminal_no_package"
)

// ProjectLifecycleReconciler detects the four self-operation broken chains and
// proposes traceable actions. It is READ-ONLY: the periodic job will later turn
// findings into Issues/Tasks via the existing issue/task services.
type ProjectLifecycleReconciler struct {
	Queries *db.Queries
}

func NewProjectLifecycleReconciler(q *db.Queries) *ProjectLifecycleReconciler {
	return &ProjectLifecycleReconciler{Queries: q}
}

// Diagnose scans the workspace and returns one finding per broken chain.
func (r *ProjectLifecycleReconciler) Diagnose(ctx context.Context, workspaceID pgtype.UUID) ([]ReconcileFinding, error) {
	projector := NewProjectLifecycleProjector(r.Queries)
	snaps, err := projector.ListPortfolio(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	repairGaps, err := r.Queries.ListProjectRepairGaps(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	repairByProject := map[string]int{}
	for _, rg := range repairGaps {
		if pid := util.UUIDToString(rg.ProjectID); pid != "" {
			repairByProject[pid] = int(rg.FailedCount)
		}
	}

	var findings []ReconcileFinding
	for _, snap := range snaps {
		switch snap.Health {
		case string(HealthStalledNoOpenTask):
			findings = append(findings, ReconcileFinding{
				Kind:      FindingStalledNoTask,
				ProjectID: snap.ProjectID,
				Summary: fmt.Sprintf("%d nonterminal issue(s), 0 live task(s)",
					snap.NonterminalIssueCount),
				NextAction: "resume the ready frontier or pause explicitly",
			})
		case string(HealthReviewOrRepairBlocked):
			// Two sub-cases: review backlog without a live reviewer task, and
			// a failed repair gap without a live repair task.
			if snap.ReviewIssueCount > 0 && snap.ActiveTaskCount == 0 {
				findings = append(findings, ReconcileFinding{
					Kind:      FindingReviewNoReviewer,
					ProjectID: snap.ProjectID,
					Summary: fmt.Sprintf("%d in_review issue(s), no live review task",
						snap.ReviewIssueCount),
					NextAction: "create an independent review/disposition task",
				})
			}
			if n := repairByProject[snap.ProjectID]; n > 0 {
				findings = append(findings, ReconcileFinding{
					Kind:       FindingRepairNoRepair,
					ProjectID:  snap.ProjectID,
					Summary:    fmt.Sprintf("%d failed task(s) on open issue(s), no live repair task", n),
					NextAction: "create a repair/re-review task",
				})
			}
		case string(HealthSourceGap):
			findings = append(findings, ReconcileFinding{
				Kind:      FindingTerminalNoPackage,
				ProjectID: snap.ProjectID,
				Summary: fmt.Sprintf("all %d issue(s) terminal but no confirmed outcome / closure package",
					snap.TerminalIssueCount),
				NextAction: "map issues to outcomes and generate a closure package",
			})
		}
	}
	return findings, nil
}

// ReconcileWorkspace runs the diagnosis and creates one dedup'd traceable
// action per finding (the "handle" half of VC-12). A finding is skipped when an
// open "[自愈] <kind>" issue already exists for the project (idempotent).
func (r *ProjectLifecycleReconciler) ReconcileWorkspace(ctx context.Context, workspaceID pgtype.UUID, issueSvc *IssueService, creatorType string, creatorID pgtype.UUID) (int, error) {
	findings, err := r.Diagnose(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, f := range findings {
		prefix := "[自愈] " + f.Kind + " · %"
		exists, err := r.Queries.HasOpenReconcileIssue(ctx, db.HasOpenReconcileIssueParams{
			ProjectID: util.MustParseUUID(f.ProjectID),
			Title:     prefix,
		})
		if err != nil || exists {
			continue
		}
		title := "[自愈] " + f.Kind + " · " + f.Summary
		if len(title) > 200 {
			title = title[:200]
		}
		_, err = issueSvc.Create(ctx, IssueCreateParams{
			WorkspaceID:    workspaceID,
			Title:          title,
			Description:    pgtype.Text{String: f.NextAction, Valid: true},
			Status:         "backlog",
			Priority:       "medium",
			CreatorType:    creatorType,
			CreatorID:      creatorID,
			ProjectID:      util.MustParseUUID(f.ProjectID),
			AllowDuplicate: true,
		}, IssueCreateOpts{})
		if err != nil {
			return created, fmt.Errorf("create reconcile action for %s/%s: %w", f.ProjectID, f.Kind, err)
		}
		created++
	}
	return created, nil
}
