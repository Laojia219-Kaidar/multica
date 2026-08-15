package workentry

import (
	"context"
	"strings"
)

// CampaignMatch is a read-only external_campaign_ref → Project resolution hit.
// Per P0-02 §3 the campaign ref is carried with zero migration via
// project_resource (resource_type='external_campaign',
// resource_ref={"campaign_id":"G61"}) or issue.metadata
// ({"external_campaign_ref":"G61"}). No second campaign table exists.
type CampaignMatch struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	IssueID     string `json:"issue_id,omitempty"`
	CampaignRef string `json:"campaign_ref"`
	Source      string `json:"source"` // project_resource | issue_metadata
	Title       string `json:"title,omitempty"`
}

// Campaign source labels for CampaignMatch.Source.
const (
	CampaignSourceProjectResource = "project_resource"
	CampaignSourceIssueMetadata   = "issue_metadata"
)

// campaignResolver is the optional read-only Store capability used by resolve
// step 2. Stores without it report no campaign match, which preserves the
// pre-P4 behavior for an empty external_campaign_ref (no regression).
type campaignResolver interface {
	LookupCampaign(ctx context.Context, workspaceID, campaignRef string) (*CampaignMatch, error)
}

// NormalizeCampaignRef canonicalizes an external_campaign_ref for exact,
// case-insensitive matching. G ids (G61, g61) collapse to one form; empty
// input stays empty.
func NormalizeCampaignRef(ref string) string {
	return strings.ToUpper(strings.TrimSpace(ref))
}

// lookupCampaign resolves an external_campaign_ref through a Store that
// implements campaignResolver. It returns (nil, nil) for an empty ref or a
// Store without campaign support.
func (s *Service) lookupCampaign(ctx context.Context, workspaceID, campaignRef string) (*CampaignMatch, error) {
	if strings.TrimSpace(campaignRef) == "" {
		return nil, nil
	}
	if s == nil || s.store == nil {
		return nil, nil
	}
	resolver, ok := s.store.(campaignResolver)
	if !ok {
		return nil, nil
	}
	return resolver.LookupCampaign(ctx, workspaceID, NormalizeCampaignRef(campaignRef))
}

// campaignMatchToMatch converts a CampaignMatch into the frozen resolve Match
// shape (MatchExternalCampaign).
func campaignMatchToMatch(cm *CampaignMatch) Match {
	if cm == nil {
		return Match{}
	}
	return Match{
		Kind:      MatchExternalCampaign,
		Key:       cm.CampaignRef,
		WorkRef:   FormatWorkRef(cm.WorkspaceID, cm.ProjectID, cm.IssueID, ""),
		ProjectID: cm.ProjectID,
		IssueID:   cm.IssueID,
	}
}
