package workentry

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Project Steward read-only diagnostics (VC-10): no-owner / no-next-action /
// orphan / duplicate detection over the same projection snapshot used by the
// Portfolio Reconciler. Pure computation; never writes, never fabricates.

// ProjectLead is the owner/lead projection for one project (project.lead_type /
// lead_id in the existing schema).
type ProjectLead struct {
	ProjectID string `json:"project_id"`
	LeadType  string `json:"lead_type,omitempty"`
	LeadID    string `json:"lead_id,omitempty"`
}

// HeartbeatRef is one terminal/presence heartbeat observation. Stale is
// computed by the store (now - last_heartbeat_at > StaleHeartbeatAfter) so the
// pure ComputeSteward stays deterministic.
type HeartbeatRef struct {
	Host            string `json:"host"`
	SessionName     string `json:"session_name"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
	Stale           bool   `json:"stale"`
}

// CandidateRef is one artifact candidate with its lifecycle event types.
type CandidateRef struct {
	CandidateID string   `json:"candidate_id"`
	LineageID   string   `json:"lineage_id"`
	Events      []string `json:"events"`
}

// StaleHeartbeatAfter is the diagnostic staleness threshold for presence
// heartbeats: a session whose last heartbeat is older than this is reported as
// stale (VC-10 "心跳过期"). The store computes Stale so ComputeSteward stays
// deterministic over a snapshot.
const StaleHeartbeatAfter = 24 * time.Hour

// StewardSnapshot extends InventorySnapshot with project lead ownership,
// presence heartbeats, and artifact candidates.
type StewardSnapshot struct {
	InventorySnapshot
	ProjectLeads []ProjectLead  `json:"project_leads"`
	Heartbeats   []HeartbeatRef `json:"heartbeats"`
	Candidates   []CandidateRef `json:"candidates"`
}

// StewardDiagnosticKind is the closed set of portfolio/steward findings.
type StewardDiagnosticKind string

const (
	StewardNoOwner       StewardDiagnosticKind = "no_owner"
	StewardNoNextAction  StewardDiagnosticKind = "no_next_action"
	StewardOrphan        StewardDiagnosticKind = "orphan"
	StewardDuplicate     StewardDiagnosticKind = "duplicate"
	StewardStale         StewardDiagnosticKind = "stale"
	StewardOrphanCandidate StewardDiagnosticKind = "orphan_candidate"
	StewardMissingReview StewardDiagnosticKind = "missing_review"
)

// StewardDiagnostic is one bounded, evidence-backed finding.
type StewardDiagnostic struct {
	WorkspaceID string                 `json:"workspace_id"`
	Kind        StewardDiagnosticKind  `json:"kind"`
	RefKind     string                 `json:"ref_kind"` // project | issue
	RefID       string                 `json:"ref_id"`
	Title       string                 `json:"title"`
	Detail      string                 `json:"detail,omitempty"`
}

// closedIssueStatus marks an issue that no longer counts as an open next action.
func closedIssueStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "cancelled", "canceled", "closed", "archived":
		return true
	default:
		return false
	}
}

// stewardSource is the optional read-only Store capability backing
// Service.StewardDiagnostics. Stores without it return ErrUnavailable.
type stewardSource interface {
	StewardSnapshot(ctx context.Context, workspaceID string) (*StewardSnapshot, error)
}

// StewardDiagnostics computes the VC-10 portfolio diagnostics (read-only).
func (s *Service) StewardDiagnostics(ctx context.Context, workspaceID string) ([]StewardDiagnostic, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	if strings.TrimSpace(workspaceID) == "" {
		return nil, ErrInvalidRequest
	}
	src, ok := s.store.(stewardSource)
	if !ok {
		return nil, ErrUnavailable
	}
	snap, err := src.StewardSnapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return ComputeSteward(workspaceID, snap), nil
}

// ComputeSteward is the pure VC-10 diagnostic computation over a snapshot.
func ComputeSteward(workspaceID string, snap *StewardSnapshot) []StewardDiagnostic {
	out := []StewardDiagnostic{}
	if snap == nil {
		return out
	}

	leadByProject := map[string]ProjectLead{}
	for _, l := range snap.ProjectLeads {
		leadByProject[l.ProjectID] = l
	}
	openIssueByProject := map[string]bool{}
	for _, i := range snap.Issues {
		if i.ProjectID != "" && !closedIssueStatus(i.Status) {
			openIssueByProject[i.ProjectID] = true
		}
	}
	linkedIssues := map[string]bool{}
	linkedProjects := map[string]bool{}
	for _, l := range snap.Links {
		if l.IssueID != "" {
			linkedIssues[l.IssueID] = true
		}
	}
	for _, i := range snap.Issues {
		if linkedIssues[i.ID] && i.ProjectID != "" {
			linkedProjects[i.ProjectID] = true
		}
	}

	for _, p := range snap.Projects {
		lead, hasLead := leadByProject[p.ID]
		if !hasLead || strings.TrimSpace(lead.LeadID) == "" {
			out = append(out, StewardDiagnostic{
				WorkspaceID: workspaceID, Kind: StewardNoOwner, RefKind: "project",
				RefID: p.ID, Title: p.Title, Detail: "project has no accountable owner/lead",
			})
		}
		if !openIssueByProject[p.ID] {
			out = append(out, StewardDiagnostic{
				WorkspaceID: workspaceID, Kind: StewardNoNextAction, RefKind: "project",
				RefID: p.ID, Title: p.Title, Detail: "project has no open issue/task as next action",
			})
		}
		if !linkedProjects[p.ID] {
			out = append(out, StewardDiagnostic{
				WorkspaceID: workspaceID, Kind: StewardOrphan, RefKind: "project",
				RefID: p.ID, Title: p.Title, Detail: "project has no active Goal/work-order ownership link",
			})
		}
	}

	// Stale presence: a session whose last heartbeat is older than the
	// threshold (VC-10 "心跳过期").
	for _, h := range snap.Heartbeats {
		if h.Stale {
			out = append(out, StewardDiagnostic{
				WorkspaceID: workspaceID, Kind: StewardStale, RefKind: "session",
				RefID: h.Host + ":" + h.SessionName, Title: h.SessionName,
				Detail: "presence heartbeat stale (last " + h.LastHeartbeatAt + ")",
			})
		}
	}

	// Artifact candidate diagnostics: a candidate with no lifecycle event has
	// never entered review (orphan_candidate); one that was submitted but never
	// got a terminal verdict (approved/rejected/changes_requested) is stuck in
	// review without a decision (missing_review). VC-10.
	for _, c := range snap.Candidates {
		submitted := false
		terminal := false
		for _, e := range c.Events {
			switch e {
			case "submitted", "promotion_requested":
				submitted = true
			case "approved", "rejected", "changes_requested":
				terminal = true
			}
		}
		if !submitted && !terminal {
			out = append(out, StewardDiagnostic{
				WorkspaceID: workspaceID, Kind: StewardOrphanCandidate, RefKind: "candidate",
				RefID: c.CandidateID, Title: c.LineageID,
				Detail: "candidate has no lifecycle event (never entered review)",
			})
		} else if submitted && !terminal {
			out = append(out, StewardDiagnostic{
				WorkspaceID: workspaceID, Kind: StewardMissingReview, RefKind: "candidate",
				RefID: c.CandidateID, Title: c.LineageID,
				Detail: "candidate submitted but no review verdict (approved/rejected/changes_requested)",
			})
		}
	}

	// Duplicate findings reuse the inventory computation.
	inv := ComputeInventory(workspaceID, &snap.InventorySnapshot)
	for _, d := range inv.Duplicates {
		out = append(out, StewardDiagnostic{
			WorkspaceID: workspaceID, Kind: StewardDuplicate, RefKind: d.Kind,
			RefID: d.RefA, Title: d.TitleA,
			Detail: "duplicate of " + d.RefB + " (" + d.TitleB + "), similarity " + trimFloat(d.Similarity),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].RefID < out[j].RefID
	})
	return out
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}
