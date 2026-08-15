package workentry

import (
	"context"
	"testing"
)

func TestResolveContinuedViaExternalCampaign(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	store.SeedCampaign(CampaignMatch{
		WorkspaceID: "ws-1", ProjectID: "proj-g61", CampaignRef: "G61",
		Source: CampaignSourceProjectResource,
	})

	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent()
	intent.ExternalCampaignRef = "G61"

	res, err := svc.ResolvePreview(ctx, ResolveRequest{Actor: actor, Intent: intent})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.ResolutionDecision != DecisionContinued {
		t.Fatalf("expected continued, got %s", res.ResolutionDecision)
	}
	if len(res.Matches) != 1 || res.Matches[0].Kind != MatchExternalCampaign {
		t.Fatalf("unexpected matches: %+v", res.Matches)
	}
	if res.Matches[0].ProjectID != "proj-g61" {
		t.Fatalf("expected project proj-g61, got %+v", res.Matches[0])
	}
	if res.Matches[0].Key != "G61" {
		t.Fatalf("expected key G61, got %q", res.Matches[0].Key)
	}
}

func TestResolveCampaignCaseInsensitive(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	store.SeedCampaign(CampaignMatch{
		WorkspaceID: "ws-1", ProjectID: "proj-g61", CampaignRef: "G61",
		Source: CampaignSourceIssueMetadata, IssueID: "issue-1",
	})

	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent()
	intent.ExternalCampaignRef = "g61"

	res, err := svc.ResolvePreview(ctx, ResolveRequest{Actor: actor, Intent: intent})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.ResolutionDecision != DecisionContinued {
		t.Fatalf("expected continued for case-insensitive campaign ref, got %s", res.ResolutionDecision)
	}
	if res.Matches[0].IssueID != "issue-1" {
		t.Fatalf("expected issue-1, got %+v", res.Matches[0])
	}
}

func TestResolveCampaignUnknownFallsThrough(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent()
	intent.ExternalCampaignRef = "G99"

	res, err := svc.ResolvePreview(ctx, ResolveRequest{Actor: actor, Intent: intent})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.ResolutionDecision != DecisionClassificationRequired {
		t.Fatalf("unknown campaign ref must fall through to classification_required, got %s", res.ResolutionDecision)
	}
	if len(res.Matches) != 0 {
		t.Fatalf("expected no matches for unknown campaign, got %+v", res.Matches)
	}
}

func TestResolveCampaignEmptyBehavesAsBefore(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	store.SeedCampaign(CampaignMatch{WorkspaceID: "ws-1", ProjectID: "proj-g61", CampaignRef: "G61"})

	actor := fixtureActor(ActorExternalAgent)
	intent := fixtureIntent() // ExternalCampaignRef empty

	res, err := svc.ResolvePreview(ctx, ResolveRequest{Actor: actor, Intent: intent})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.ResolutionDecision != DecisionClassificationRequired {
		t.Fatalf("empty external_campaign_ref must not change behavior: got %s", res.ResolutionDecision)
	}
	if len(res.Matches) != 0 {
		t.Fatalf("expected no matches, got %+v", res.Matches)
	}
}

func TestNormalizeCampaignRef(t *testing.T) {
	cases := map[string]string{
		"G61": "G61", "g61": "G61", " G61 ": "G61", "": "", "  ": "",
	}
	for in, want := range cases {
		if got := NormalizeCampaignRef(in); got != want {
			t.Fatalf("NormalizeCampaignRef(%q) = %q, want %q", in, got, want)
		}
	}
}
