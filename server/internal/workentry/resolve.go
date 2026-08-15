package workentry

import (
	"context"
	"strings"
)

// MatchKind identifies which dedupe step produced a match
// (API-AND-ADAPTER-CONTRACT §3).
type MatchKind string

const (
	MatchWorkOrder          MatchKind = "work_order"
	MatchExternalCampaign   MatchKind = "external_campaign"
	MatchRepoRevisionBranch MatchKind = "repo_revision_branch"
	MatchProject            MatchKind = "project"
	MatchIssue              MatchKind = "issue"
	MatchSimilar            MatchKind = "similar"
)

// ResolveRequest is the input to resolve-preview and register.
type ResolveRequest struct {
	Actor  WorkActorIdentityV1 `json:"actor_identity"`
	Intent WorkIntentV1        `json:"intent"`
	// Explicit project/issue lineage selectors (step 4). Optional.
	ProjectID string `json:"project_id,omitempty"`
	IssueID   string `json:"issue_id,omitempty"`
}

// Match is one dedupe hit carrying enough lineage to continue.
type Match struct {
	Kind       MatchKind `json:"kind"`
	Key        string    `json:"key"`
	WorkRef    string    `json:"work_ref,omitempty"`
	ProjectID  string    `json:"project_id,omitempty"`
	IssueID    string    `json:"issue_id,omitempty"`
	TaskID     string    `json:"task_id,omitempty"`
	Similarity float64   `json:"similarity,omitempty"`
}

// ResolveResult is the read-only disposition returned by resolve-preview.
type ResolveResult struct {
	ResolutionDecision ResolutionDecision `json:"resolution_decision"`
	Matches            []Match            `json:"matches"`
	Similar            []SimilarMatch     `json:"similar,omitempty"`
	Suggestion         string             `json:"suggestion,omitempty"`
	Blockers           []string           `json:"blockers,omitempty"`
	DedupeKey          string             `json:"dedupe_key"`
	DedupeDigest       string             `json:"dedupe_digest"`
}

// ResolvePreview runs the frozen seven-step dedupe chain (read-only) and
// returns the disposition without writing. Step 5 (historical similarity) is
// read-only and always leads to classification_required when no exact match
// exists.
func (s *Service) ResolvePreview(ctx context.Context, req ResolveRequest) (ResolveResult, error) {
	if s == nil || s.store == nil {
		return ResolveResult{}, ErrUnavailable
	}
	if err := ValidateActorIdentity(req.Actor); err != nil {
		return ResolveResult{}, invalid(err)
	}
	if err := ValidateIntent(req.Intent); err != nil {
		return ResolveResult{}, invalid(err)
	}
	digest, err := ReceiptDigest(req.Actor, req.Intent)
	if err != nil {
		return ResolveResult{}, err
	}
	key := DedupeKey(req.Actor.WorkspaceID, req.Actor.ActorID, req.Intent.GoalRef, req.Intent.Repo, req.Intent.BaselineRevision, req.Intent.BranchOrWorktree)
	return s.resolve(ctx, req, key, digest)
}

// resolve runs the dedupe chain. Callers must have validated the request and
// computed key/digest.
func (s *Service) resolve(ctx context.Context, req ResolveRequest, key, digest string) (ResolveResult, error) {
	result := ResolveResult{
		ResolutionDecision: DecisionClassificationRequired,
		Matches:            []Match{},
		Similar:            []SimilarMatch{},
		DedupeKey:          key,
		DedupeDigest:       digest,
	}
	ws := req.Actor.WorkspaceID

	// Step 1: Goal/WorkOrder exact → external_work_order_link.
	if strings.TrimSpace(req.Intent.GoalRef) != "" {
		link, err := s.store.LookupWorkOrder(ctx, ws, req.Intent.GoalRef)
		if err != nil {
			return ResolveResult{}, err
		}
		if link != nil {
			projectID := ""
			if link.IssueID != "" {
				if issue, err := s.store.LookupIssue(ctx, ws, link.IssueID); err == nil && issue != nil {
					projectID = issue.ProjectID
				}
			}
			result.ResolutionDecision = DecisionContinued
			result.Matches = append(result.Matches, Match{
				Kind:      MatchWorkOrder, Key: link.WorkOrderRef,
				WorkRef:   FormatWorkRef(link.WorkspaceID, projectID, link.IssueID, ""),
				ProjectID: projectID,
				IssueID:   link.IssueID,
			})
			return result, nil
		}
	}

	// Step 2: external G / campaign exact — project_resource
	// (resource_type='external_campaign') or issue.metadata, zero migration
	// (P0-02 §3). Empty external_campaign_ref behaves exactly as before.
	if strings.TrimSpace(req.Intent.ExternalCampaignRef) != "" {
		cm, err := s.lookupCampaign(ctx, ws, req.Intent.ExternalCampaignRef)
		if err != nil {
			return ResolveResult{}, err
		}
		if cm != nil {
			result.ResolutionDecision = DecisionContinued
			result.Matches = append(result.Matches, campaignMatchToMatch(cm))
			return result, nil
		}
	}

	// Step 3: repo+revision+branch/worktree exact.
	if strings.TrimSpace(req.Intent.Repo) != "" && strings.TrimSpace(req.Intent.BaselineRevision) != "" {
		repo, err := s.store.LookupRepoRevisionBranch(ctx, ws, req.Intent.Repo, req.Intent.BaselineRevision, req.Intent.BranchOrWorktree)
		if err != nil {
			return ResolveResult{}, err
		}
		if repo != nil {
			result.ResolutionDecision = DecisionContinued
			result.Matches = append(result.Matches, Match{
				Kind:      MatchRepoRevisionBranch,
				Key:       req.Intent.Repo + ":" + req.Intent.BaselineRevision + ":" + req.Intent.BranchOrWorktree,
				WorkRef:   FormatWorkRef(repo.WorkspaceID, repo.ProjectID, repo.IssueID, ""),
				ProjectID: repo.ProjectID, IssueID: repo.IssueID,
			})
			return result, nil
		}
	}

	// Step 4: explicit project/issue lineage.
	if strings.TrimSpace(req.ProjectID) != "" {
		p, err := s.store.LookupProject(ctx, ws, req.ProjectID)
		if err != nil {
			return ResolveResult{}, err
		}
		if p != nil {
			result.ResolutionDecision = DecisionContinued
			result.Matches = append(result.Matches, Match{
				Kind: MatchProject, Key: p.ID,
				WorkRef: FormatWorkRef(p.WorkspaceID, p.ID, "", ""), ProjectID: p.ID,
			})
			return result, nil
		}
	}
	if strings.TrimSpace(req.IssueID) != "" {
		i, err := s.store.LookupIssue(ctx, ws, req.IssueID)
		if err != nil {
			return ResolveResult{}, err
		}
		if i != nil {
			result.ResolutionDecision = DecisionContinued
			result.Matches = append(result.Matches, Match{
				Kind: MatchIssue, Key: i.ID,
				WorkRef: FormatWorkRef(i.WorkspaceID, i.ProjectID, i.ID, ""),
				ProjectID: i.ProjectID, IssueID: i.ID,
			})
			return result, nil
		}
	}

	// Step 5: historical similarity (read-only). A similarity hit never creates
	// and never continues; it only informs the classification suggestion.
	query := req.Intent.Objective
	if strings.TrimSpace(query) == "" {
		query = req.Intent.OwnerIntent
	}
	similar, err := s.store.SearchSimilar(ctx, ws, query, 5)
	if err != nil {
		return ResolveResult{}, err
	}
	result.Similar = similar

	// Step 6: classification_required. Never create here.
	if len(similar) > 0 {
		result.Suggestion = "similar existing work found; classify (attach) instead of creating a duplicate"
	} else {
		result.Suggestion = "no exact ownership matched; provide an explicit project_id/issue_id, or obtain an Owner/Steward classification before creation"
	}
	return result, nil
}
