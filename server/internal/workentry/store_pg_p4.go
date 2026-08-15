package workentry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// Phase-4 PG store capabilities (zero migration): G-series campaign resolution
// via project_resource and the duplicate/orphan inventory snapshot. They are
// optional capabilities (campaignResolver / inventorySource); the Service falls
// back cleanly when a store does not implement them.

// LookupCampaign implements campaignResolver against project_resource rows with
// resource_type='external_campaign' and resource_ref.campaign_id (P0-02 §3).
// The campaign ref is normalized (case-insensitive) on lookup.
func (p *PGStore) LookupCampaign(ctx context.Context, workspaceID, campaignRef string) (*CampaignMatch, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	ref := NormalizeCampaignRef(campaignRef)
	var (
		projectID pgtype.UUID
		stored    string
	)
	err = p.exec.QueryRow(ctx,
		"SELECT project_id, resource_ref->>'campaign_id' FROM project_resource WHERE workspace_id = $1 AND resource_type = 'external_campaign' AND resource_ref->>'campaign_id' = $2 LIMIT 1",
		ws, ref).Scan(&projectID, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup external campaign: %w", err)
	}
	return &CampaignMatch{
		WorkspaceID: workspaceID,
		ProjectID:   util.UUIDToString(projectID),
		CampaignRef: stored,
		Source:      CampaignSourceProjectResource,
	}, nil
}

// PutCampaignLink persists an external_campaign → project link with zero
// migration via project_resource. Idempotent on (project_id, resource_type,
// resource_ref).
func (p *PGStore) PutCampaignLink(ctx context.Context, link CampaignMatch) error {
	ws, err := p.uuid(link.WorkspaceID)
	if err != nil {
		return ErrInvalidRequest
	}
	projectID, err := p.uuid(link.ProjectID)
	if err != nil {
		return ErrInvalidRequest
	}
	ref := NormalizeCampaignRef(link.CampaignRef)
	_, err = p.exec.Exec(ctx,
		"INSERT INTO project_resource (workspace_id, project_id, resource_type, resource_ref) VALUES ($1, $2, 'external_campaign', jsonb_build_object('campaign_id', $3::text)) ON CONFLICT (project_id, resource_type, resource_ref) DO NOTHING",
		ws, projectID, ref)
	if err != nil {
		return fmt.Errorf("insert external campaign link: %w", err)
	}
	return nil
}

// InventorySnapshot implements inventorySource (read-only) against the existing
// project / issue / external_work_order_link / project_resource tables.
func (p *PGStore) InventorySnapshot(ctx context.Context, workspaceID string) (*InventorySnapshot, error) {
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	snap := &InventorySnapshot{
		Projects: []ProjectRef{}, Issues: []IssueRef{},
		Links: []ExternalWorkOrderLink{}, Repos: []RepoRef{},
	}

	rows, err := p.exec.Query(ctx, "SELECT id, title, status FROM project WHERE workspace_id = $1 ORDER BY created_at", ws)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id pgtype.UUID
		var title, status string
		if err := rows.Scan(&id, &title, &status); err != nil {
			return nil, err
		}
		snap.Projects = append(snap.Projects, ProjectRef{
			ID: util.UUIDToString(id), WorkspaceID: workspaceID, Title: title, Status: status,
		})
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	rows2, err := p.exec.Query(ctx, "SELECT id, title, status, COALESCE(project_id::text, '') FROM issue WHERE workspace_id = $1 ORDER BY created_at", ws)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var id pgtype.UUID
		var title, status, projectID string
		if err := rows2.Scan(&id, &title, &status, &projectID); err != nil {
			return nil, err
		}
		snap.Issues = append(snap.Issues, IssueRef{
			ID: util.UUIDToString(id), WorkspaceID: workspaceID, Title: title, Status: status, ProjectID: projectID,
		})
	}
	if rows2.Err() != nil {
		return nil, rows2.Err()
	}

	rows3, err := p.exec.Query(ctx, "SELECT work_order_ref, linked_revision, linked_digest, issue_id::text FROM external_work_order_link WHERE workspace_id = $1", ws)
	if err != nil {
		return nil, fmt.Errorf("list work order links: %w", err)
	}
	defer rows3.Close()
	for rows3.Next() {
		var ref, revision, digest, issueID string
		if err := rows3.Scan(&ref, &revision, &digest, &issueID); err != nil {
			return nil, err
		}
		snap.Links = append(snap.Links, ExternalWorkOrderLink{
			WorkspaceID: workspaceID, WorkOrderRef: ref, LinkedRevision: revision,
			LinkedDigest: digest, IssueID: issueID,
		})
	}
	if rows3.Err() != nil {
		return nil, rows3.Err()
	}

	rows4, err := p.exec.Query(ctx, "SELECT project_id, resource_type, resource_ref FROM project_resource WHERE workspace_id = $1 AND resource_type IN ('github_repo','local_directory')", ws)
	if err != nil {
		return nil, fmt.Errorf("list repo refs: %w", err)
	}
	defer rows4.Close()
	for rows4.Next() {
		var projectID pgtype.UUID
		var rtype string
		var raw []byte
		if err := rows4.Scan(&projectID, &rtype, &raw); err != nil {
			return nil, err
		}
		repo := ""
		if rtype == "github_repo" {
			repo = jsonStringField(raw, "url")
		} else {
			repo = jsonStringField(raw, "local_path")
		}
		if repo == "" {
			continue
		}
		snap.Repos = append(snap.Repos, RepoRef{
			WorkspaceID: workspaceID, OwnerKind: "project",
			OwnerID: util.UUIDToString(projectID), Repo: repo,
		})
	}
	if rows4.Err() != nil {
		return nil, rows4.Err()
	}
	return snap, nil
}


// jsonStringField extracts a top-level string field from a JSON object payload.
func jsonStringField(raw []byte, key string) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}


// StewardSnapshot implements stewardSource (read-only) over the existing
// project / issue / external_work_order_link / project_resource tables (via
// InventorySnapshot) plus project.lead_* ownership, terminal_presence
// heartbeats, and artifact_candidate + artifact_event lifecycle. Zero
// migration; never writes.
func (p *PGStore) StewardSnapshot(ctx context.Context, workspaceID string) (*StewardSnapshot, error) {
	inv, err := p.InventorySnapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	ws, err := p.uuid(workspaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	snap := &StewardSnapshot{
		InventorySnapshot: *inv,
		ProjectLeads:     []ProjectLead{},
		Heartbeats:       []HeartbeatRef{},
		Candidates:       []CandidateRef{},
	}

	lrows, err := p.exec.Query(ctx,
		"SELECT id, COALESCE(lead_type, ''), COALESCE(lead_id::text, '') FROM project WHERE workspace_id = $1 AND lead_id IS NOT NULL",
		ws)
	if err != nil {
		return nil, fmt.Errorf("list project leads: %w", err)
	}
	defer lrows.Close()
	for lrows.Next() {
		var id pgtype.UUID
		var leadType, leadID string
		if err := lrows.Scan(&id, &leadType, &leadID); err != nil {
			return nil, err
		}
		snap.ProjectLeads = append(snap.ProjectLeads, ProjectLead{
			ProjectID: util.UUIDToString(id), LeadType: leadType, LeadID: leadID,
		})
	}
	if lrows.Err() != nil {
		return nil, lrows.Err()
	}

	hrows, err := p.exec.Query(ctx,
		"SELECT host, session_name, heartbeat_at FROM terminal_presence WHERE workspace_id = $1 ORDER BY heartbeat_at DESC",
		ws)
	if err != nil {
		return nil, fmt.Errorf("list presence heartbeats: %w", err)
	}
	defer hrows.Close()
	for hrows.Next() {
		var host, session string
		var at time.Time
		if err := hrows.Scan(&host, &session, &at); err != nil {
			return nil, err
		}
		snap.Heartbeats = append(snap.Heartbeats, HeartbeatRef{
			Host:            host,
			SessionName:     session,
			LastHeartbeatAt: at.UTC().Format(time.RFC3339),
			Stale:           time.Since(at) > StaleHeartbeatAfter,
		})
	}
	if hrows.Err() != nil {
		return nil, hrows.Err()
	}

	crows, err := p.exec.Query(ctx,
		"SELECT c.id, c.lineage_id, COALESCE(e.event_type, '') FROM artifact_candidate c LEFT JOIN artifact_event e ON e.candidate_id = c.id WHERE c.workspace_id = $1 ORDER BY c.created_at, e.sequence",
		ws)
	if err != nil {
		return nil, fmt.Errorf("list artifact candidates: %w", err)
	}
	defer crows.Close()
	candIdx := map[string]int{}
	for crows.Next() {
		var cid, lid pgtype.UUID
		var evt string
		if err := crows.Scan(&cid, &lid, &evt); err != nil {
			return nil, err
		}
		cidStr := util.UUIDToString(cid)
		idx, ok := candIdx[cidStr]
		if !ok {
			snap.Candidates = append(snap.Candidates, CandidateRef{
				CandidateID: cidStr, LineageID: util.UUIDToString(lid), Events: []string{},
			})
			idx = len(snap.Candidates) - 1
			candIdx[cidStr] = idx
		}
		if evt != "" {
			snap.Candidates[idx].Events = append(snap.Candidates[idx].Events, evt)
		}
	}
	if crows.Err() != nil {
		return nil, crows.Err()
	}
	return snap, nil
}
