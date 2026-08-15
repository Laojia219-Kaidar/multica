package workentry

import (
	"context"
	"sort"
	"strings"
)

// inventorySimilarityThreshold is the minimum symmetric token-overlap
// similarity for two same-repo titles to be reported as duplicate candidates.
const inventorySimilarityThreshold = 0.6

// RepoRef associates a repo identity (local_directory.local_path or
// github_repo.url) with a project or issue for duplicate detection.
type RepoRef struct {
	WorkspaceID string `json:"workspace_id"`
	OwnerKind   string `json:"owner_kind"` // project | issue
	OwnerID     string `json:"owner_id"`
	Repo        string `json:"repo"`
}

// InventorySnapshot is the raw read-only Store capability input for the
// duplicate/orphan diagnostic.
type InventorySnapshot struct {
	Projects []ProjectRef            `json:"projects"`
	Issues   []IssueRef              `json:"issues"`
	Links    []ExternalWorkOrderLink `json:"links"`
	Repos    []RepoRef               `json:"repos"`
}

// DuplicateCandidate is a diagnostic pair of work entries that share a repo and
// have similar titles.
type DuplicateCandidate struct {
	WorkspaceID string  `json:"workspace_id"`
	Repo        string  `json:"repo"`
	Kind        string  `json:"kind"` // project | issue
	RefA        string  `json:"ref_a"`
	TitleA      string  `json:"title_a"`
	RefB        string  `json:"ref_b"`
	TitleB      string  `json:"title_b"`
	Similarity  float64 `json:"similarity"`
}

// OrphanCandidate is a work entry with no active Goal ownership link.
type OrphanCandidate struct {
	WorkspaceID string `json:"workspace_id"`
	Kind        string `json:"kind"` // project | issue
	RefID       string `json:"ref_id"`
	Title       string `json:"title"`
	Status      string `json:"status,omitempty"`
}

// InventoryResult is the read-only duplicate/orphan diagnostic.
type InventoryResult struct {
	WorkspaceID string               `json:"workspace_id"`
	Duplicates  []DuplicateCandidate `json:"duplicates"`
	Orphans     []OrphanCandidate    `json:"orphans"`
}

// inventorySource is the optional read-only Store capability backing
// Service.Inventory. Stores without it return ErrUnavailable.
type inventorySource interface {
	InventorySnapshot(ctx context.Context, workspaceID string) (*InventorySnapshot, error)
}

// Inventory computes duplicate (same repo + similar title) and orphan (no
// active Goal ownership) candidates. Read-only; never writes.
func (s *Service) Inventory(ctx context.Context, workspaceID string) (InventoryResult, error) {
	if s == nil || s.store == nil {
		return InventoryResult{}, ErrUnavailable
	}
	if strings.TrimSpace(workspaceID) == "" {
		return InventoryResult{}, ErrInvalidRequest
	}
	src, ok := s.store.(inventorySource)
	if !ok {
		return InventoryResult{}, ErrUnavailable
	}
	snap, err := src.InventorySnapshot(ctx, workspaceID)
	if err != nil {
		return InventoryResult{}, err
	}
	return ComputeInventory(workspaceID, snap), nil
}

// ComputeInventory is the pure duplicate/orphan computation over a snapshot
// (testable without a concrete Store).
func ComputeInventory(workspaceID string, snap *InventorySnapshot) InventoryResult {
	out := InventoryResult{
		WorkspaceID: workspaceID,
		Duplicates:  []DuplicateCandidate{},
		Orphans:     []OrphanCandidate{},
	}
	if snap == nil {
		return out
	}

	// Orphans: no active Goal ownership link. A link owns its Issue; a Project
	// is owned transitively when at least one of its Issues is linked.
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
		if !linkedProjects[p.ID] {
			out.Orphans = append(out.Orphans, OrphanCandidate{
				WorkspaceID: p.WorkspaceID, Kind: "project", RefID: p.ID,
				Title: p.Title, Status: p.Status,
			})
		}
	}
	for _, i := range snap.Issues {
		if !linkedIssues[i.ID] {
			out.Orphans = append(out.Orphans, OrphanCandidate{
				WorkspaceID: i.WorkspaceID, Kind: "issue", RefID: i.ID,
				Title: i.Title, Status: i.Status,
			})
		}
	}

	// Duplicates: same repo + similar title.
	byRepo := map[string][]repoEntry{}
	for _, r := range snap.Repos {
		if strings.TrimSpace(r.Repo) == "" {
			continue
		}
		byRepo[r.Repo] = append(byRepo[r.Repo], repoEntry{kind: r.OwnerKind, id: r.OwnerID, title: entryTitle(snap, r)})
	}
	for repo, entries := range byRepo {
		if len(entries) < 2 {
			continue
		}
		for a := 0; a < len(entries); a++ {
			for b := a + 1; b < len(entries); b++ {
				ea, eb := entries[a], entries[b]
				sim := titleSimilarity(ea.title, eb.title)
				if sim < inventorySimilarityThreshold {
					continue
				}
				out.Duplicates = append(out.Duplicates, DuplicateCandidate{
					WorkspaceID: workspaceID, Repo: repo, Kind: ea.kind,
					RefA: ea.id, TitleA: ea.title, RefB: eb.id, TitleB: eb.title,
					Similarity: sim,
				})
			}
		}
	}

	sort.SliceStable(out.Duplicates, func(i, j int) bool {
		if out.Duplicates[i].Repo != out.Duplicates[j].Repo {
			return out.Duplicates[i].Repo < out.Duplicates[j].Repo
		}
		if out.Duplicates[i].RefA != out.Duplicates[j].RefA {
			return out.Duplicates[i].RefA < out.Duplicates[j].RefA
		}
		return out.Duplicates[i].RefB < out.Duplicates[j].RefB
	})
	sort.SliceStable(out.Orphans, func(i, j int) bool {
		if out.Orphans[i].Kind != out.Orphans[j].Kind {
			return out.Orphans[i].Kind < out.Orphans[j].Kind
		}
		return out.Orphans[i].RefID < out.Orphans[j].RefID
	})
	return out
}

type repoEntry struct {
	kind  string
	id    string
	title string
}

func entryTitle(snap *InventorySnapshot, r RepoRef) string {
	switch r.OwnerKind {
	case "issue":
		for _, i := range snap.Issues {
			if i.ID == r.OwnerID {
				return i.Title
			}
		}
	case "project":
		for _, p := range snap.Projects {
			if p.ID == r.OwnerID {
				return p.Title
			}
		}
	}
	return ""
}

// titleSimilarity is a symmetric token-overlap similarity in [0,1].
func titleSimilarity(a, b string) float64 {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	ta := tokenSet(a)
	tb := tokenSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for t := range ta {
		if tb[t] {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.Fields(s) {
		out[f] = true
	}
	return out
}
