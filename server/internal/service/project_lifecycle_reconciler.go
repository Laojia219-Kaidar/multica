package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ReconcileFinding is one detected broken chain (VC-12 diagnostic). The
// reconciler only DIAGNOSES + suggests a traceable action; it never writes.
type ReconcileFinding struct {
	Kind             string   `json:"kind"`
	ProjectID        string   `json:"project_id"`
	Disposition      string   `json:"disposition"`
	IssueID          *string  `json:"issue_id,omitempty"`
	FrontierIssueIDs []string `json:"frontier_issue_ids,omitempty"`
	Summary          string   `json:"summary"`
	NextAction       string   `json:"next_action"`
}

const maxReconcileSourceIssues = 10000

// projectReconcileProvenance is the typed, durable source record attached to a
// reconciler-created Issue. Goal and WorkOrder are explicit nullable fields:
// the lifecycle DB read model cannot infer either authority from a Project.
type projectReconcileProvenance struct {
	SchemaVersion  string   `json:"schema_version"`
	ProjectID      string   `json:"project_id"`
	FindingKind    string   `json:"finding_kind"`
	Disposition    string   `json:"disposition"`
	SourceIssueIDs []string `json:"source_issue_ids"`
	GoalID         *string  `json:"goal_id"`
	WorkOrderRef   *string  `json:"work_order_ref"`
	SourceGap      []string `json:"source_gap"`
}

// Reconcile finding kinds (the VC-12 broken-chain detectors plus the
// terminal-projection consistency detector).
const (
	FindingStalledNoTask                  = "stalled_no_task"
	FindingReviewNoReviewer               = "review_no_reviewer"
	FindingRepairNoRepair                 = "repair_no_repair"
	FindingTerminalNoPackage              = "terminal_no_package"
	FindingTerminalProjectionInconsistent = "terminal_projection_inconsistent"
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
		if snap.TerminalProjectionInconsistent {
			findings = append(findings, ReconcileFinding{
				Kind:             FindingTerminalProjectionInconsistent,
				ProjectID:        snap.ProjectID,
				Disposition:      snap.Disposition,
				FrontierIssueIDs: snap.FrontierIssueIDs,
				Summary:          fmt.Sprintf("project status %q disagrees with live projection (%s): %d nonterminal issue(s), %d active task(s)", snap.Status, snap.TerminalProjectionFinding, snap.NonterminalIssueCount, snap.ActiveTaskCount),
				NextAction:       snap.TerminalProjectionNextAction,
			})
		}
		switch snap.Health {
		case string(HealthStalledNoOpenTask):
			findings = append(findings, ReconcileFinding{
				Kind:             FindingStalledNoTask,
				ProjectID:        snap.ProjectID,
				Disposition:      snap.Disposition,
				FrontierIssueIDs: snap.FrontierIssueIDs,
				Summary: fmt.Sprintf("%d nonterminal issue(s), 0 live task(s)",
					snap.NonterminalIssueCount),
				NextAction: "resume the ready frontier or pause explicitly",
			})
		case string(HealthReviewOrRepairBlocked):
			// Two sub-cases: review backlog without a live reviewer task, and
			// a failed repair gap without a live repair task.
			if snap.ReviewIssueCount > 0 && snap.ActiveTaskCount == 0 {
				findings = append(findings, ReconcileFinding{
					Kind:             FindingReviewNoReviewer,
					ProjectID:        snap.ProjectID,
					Disposition:      snap.Disposition,
					FrontierIssueIDs: snap.FrontierIssueIDs,
					Summary: fmt.Sprintf("%d in_review issue(s), no live review task",
						snap.ReviewIssueCount),
					NextAction: "create an independent review/disposition task",
				})
			}
			if n := repairByProject[snap.ProjectID]; n > 0 {
				findings = append(findings, ReconcileFinding{
					Kind:             FindingRepairNoRepair,
					ProjectID:        snap.ProjectID,
					Disposition:      snap.Disposition,
					FrontierIssueIDs: snap.FrontierIssueIDs,
					Summary:          fmt.Sprintf("%d failed task(s) on open issue(s), no live repair task", n),
					NextAction:       "create a repair/re-review task",
				})
			}
		case string(HealthSourceGap):
			findings = append(findings, ReconcileFinding{
				Kind:             FindingTerminalNoPackage,
				ProjectID:        snap.ProjectID,
				Disposition:      snap.Disposition,
				FrontierIssueIDs: snap.FrontierIssueIDs,
				Summary: fmt.Sprintf("all %d issue(s) terminal but no confirmed outcome / closure package",
					snap.TerminalIssueCount),
				NextAction: "map issues to outcomes and generate a closure package",
			})
		}
	}
	return findings, nil
}

// ReconcileWorkspace runs the diagnosis and creates one dedup'd traceable
// action per finding (the "handle" half of VC-12). Deduplication is handled
// atomically by the IssueService duplicate guard (advisory lock + normalized
// title match inside the create transaction), not by a separate check-then-create.
//
// The title is stable (kind + project ID only) so the duplicate guard key
// does not shift when project counts change after the first repair Issue.
// Structured provenance and the Issue row are committed in one transaction;
// missing Goal/WorkOrder authority remains an explicit source_gap.
func (r *ProjectLifecycleReconciler) ReconcileWorkspace(ctx context.Context, workspaceID pgtype.UUID, issueSvc *IssueService, creatorType string, creatorID pgtype.UUID) (int, error) {
	findings, err := r.Diagnose(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, f := range findings {
		wasCreated, err := r.createFindingIssue(ctx, workspaceID, issueSvc, creatorType, creatorID, f)
		if err != nil {
			return created, fmt.Errorf("create reconcile action for %s/%s: %w", f.ProjectID, f.Kind, err)
		}
		if wasCreated {
			created++
		}
	}
	return created, nil
}

func (r *ProjectLifecycleReconciler) createFindingIssue(
	ctx context.Context,
	workspaceID pgtype.UUID,
	issueSvc *IssueService,
	creatorType string,
	creatorID pgtype.UUID,
	f ReconcileFinding,
) (bool, error) {
	if issueSvc == nil || issueSvc.Queries == nil || issueSvc.TxStarter == nil {
		return false, errors.New("issue service transaction writer is unavailable")
	}
	tx, err := issueSvc.TxStarter.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin reconcile Issue transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := issueSvc.Queries.WithTx(tx)
	projectID := util.MustParseUUID(f.ProjectID)

	// Capture the real pre-existing Project Issue set before creating the
	// repair Issue. This is a typed source chain, not a title/description hint.
	sourceRows, err := qtx.ListIssues(ctx, db.ListIssuesParams{
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
		Limit:       maxReconcileSourceIssues,
	})
	if err != nil {
		return false, fmt.Errorf("list reconcile source Issues: %w", err)
	}
	if len(sourceRows) == maxReconcileSourceIssues {
		return false, fmt.Errorf("reconcile source Issue set reached safety limit %d", maxReconcileSourceIssues)
	}
	sourceIssueIDs := make([]string, 0, len(sourceRows))
	for _, row := range sourceRows {
		if id := util.UUIDToString(row.ID); id != "" {
			sourceIssueIDs = append(sourceIssueIDs, id)
		}
	}
	sourceGap := []string{"goal", "work_order"}
	if len(sourceIssueIDs) == 0 {
		sourceGap = append(sourceGap, "source_issue")
	}
	provenance, err := json.Marshal(projectReconcileProvenance{
		SchemaVersion:  "hivecrew.project-reconcile-provenance/v1",
		ProjectID:      f.ProjectID,
		FindingKind:    f.Kind,
		Disposition:    f.Disposition,
		SourceIssueIDs: sourceIssueIDs,
		SourceGap:      sourceGap,
	})
	if err != nil {
		return false, fmt.Errorf("encode reconcile provenance: %w", err)
	}

	params := IssueCreateParams{
		WorkspaceID: workspaceID,
		Title:       "[自愈] " + f.Kind + " · " + f.ProjectID,
		Description: pgtype.Text{String: f.NextAction, Valid: f.NextAction != ""},
		Status:      "backlog",
		Priority:    "medium",
		CreatorType: creatorType,
		CreatorID:   creatorID,
		ProjectID:   projectID,
	}
	result, err := issueSvc.createInTransaction(ctx, tx, qtx, params)
	if err != nil {
		if errors.Is(err, ErrActiveDuplicate) {
			return false, nil
		}
		return false, err
	}
	if _, err := qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		Key:         "project_reconcile_provenance",
		Value:       provenance,
		ID:          result.Issue.ID,
		WorkspaceID: workspaceID,
	}); err != nil {
		return false, fmt.Errorf("persist reconcile provenance: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit reconcile Issue transaction: %w", err)
	}
	issueSvc.finishCreate(ctx, params, IssueCreateOpts{}, result)
	return true, nil
}
