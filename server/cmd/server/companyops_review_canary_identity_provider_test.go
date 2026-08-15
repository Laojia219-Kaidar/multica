package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

func canaryEnv(t *testing.T) {
	t.Helper()
	setenvs := map[string]string{
		"HIVECREW_REVIEW_CANARY_ENABLED":            "true",
		"HIVECREW_REVIEW_CANARY_WORKSPACE_ID":       "1b2a1f07-3050-4d47-aca5-6e6fdbd393d9",
		"HIVECREW_REVIEW_CANARY_PROJECT_ID":         "3b0330e7-a2da-4f41-94ab-61c911af2820",
		"HIVECREW_REVIEW_CANARY_ISSUE_ID":           "b18686c3-6581-4ea0-b484-fd935ea99adf",
		"HIVECREW_REVIEW_CANARY_TENANT_ID":          "00000000-0000-0000-0000-000000000001",
		"HIVECREW_REVIEW_CANARY_WORK_ORDER_REF":     "hive://hivecosm/delivery/project/3b0330e7-a2da-4f41-94ab-61c911af2820/work-order/WO-T3-01",
		"HIVECREW_REVIEW_CANARY_EMPLOYEE_ID":        "DE-PROJECTION-KT-005",
		"HIVECREW_REVIEW_CANARY_IDENTITY_BINDING_ID": "identity-bindings.v1:KT-005:ae973f7e-7d2d-43a8-8958-9d3f3c0b6e18",
		"HIVECREW_REVIEW_CANARY_AGENT_ID":           "ae973f7e-7d2d-43a8-8958-9d3f3c0b6e18",
		"HIVECREW_REVIEW_CANARY_ASSIGNMENT_ID":      "ASSIGN-WO-T3-01-KT005-CANARY-20260815",
		"HIVECREW_REVIEW_CANARY_EXPIRES_AT":         "2999-01-01T00:00:00Z",
	}
	for key, value := range setenvs {
		t.Setenv(key, value)
	}
}

func pgUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	parsed, err := uuid.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

func TestReviewCanaryIdentityProviderFailsClosedOutsideExactScope(t *testing.T) {
	canaryEnv(t)
	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	provider := reviewCanaryIdentityProviderFromEnv(func() time.Time { return now })
	if provider == nil {
		t.Fatal("fully configured canary env must construct the provider")
	}

	identity, err := provider.ResolveReviewDispatchIdentity(
		context.Background(),
		pgUUID(t, "1b2a1f07-3050-4d47-aca5-6e6fdbd393d9"),
		pgUUID(t, "3b0330e7-a2da-4f41-94ab-61c911af2820"),
		pgUUID(t, "b18686c3-6581-4ea0-b484-fd935ea99adf"),
		service.AuthorityReviewDispatchCandidate{},
	)
	if err != nil {
		t.Fatalf("exact canary candidate must resolve: %v", err)
	}
	if identity.EmployeeID != "DE-PROJECTION-KT-005" || identity.AgentID != "ae973f7e-7d2d-43a8-8958-9d3f3c0b6e18" {
		t.Fatalf("unexpected identity %+v", identity)
	}

	wrongIssue := pgUUID(t, "00000000-0000-0000-0000-000000000999")
	if _, err := provider.ResolveReviewDispatchIdentity(context.Background(), pgUUID(t, "1b2a1f07-3050-4d47-aca5-6e6fdbd393d9"), pgUUID(t, "3b0330e7-a2da-4f41-94ab-61c911af2820"), wrongIssue, service.AuthorityReviewDispatchCandidate{}); err == nil {
		t.Fatal("issue outside the canary scope must fail closed")
	}

	wrongWorkspace := pgUUID(t, "00000000-0000-0000-0000-000000000888")
	if _, err := provider.ResolveReviewDispatchIdentity(context.Background(), wrongWorkspace, pgUUID(t, "3b0330e7-a2da-4f41-94ab-61c911af2820"), pgUUID(t, "b18686c3-6581-4ea0-b484-fd935ea99adf"), service.AuthorityReviewDispatchCandidate{}); err == nil {
		t.Fatal("workspace outside the canary scope must fail closed")
	}

	expired := reviewCanaryIdentityProviderFromEnv(func() time.Time { return time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC) })
	if expired == nil {
		t.Fatal("provider construction uses wall-clock expiry only for the env gate")
	}
	if _, err := expired.ResolveReviewDispatchIdentity(context.Background(), pgUUID(t, "1b2a1f07-3050-4d47-aca5-6e6fdbd393d9"), pgUUID(t, "3b0330e7-a2da-4f41-94ab-61c911af2820"), pgUUID(t, "b18686c3-6581-4ea0-b484-fd935ea99adf"), service.AuthorityReviewDispatchCandidate{}); err == nil {
		t.Fatal("expired canary TTL must fail closed")
	}
}

func TestReviewCanaryIdentityProviderStaysDisabledWithoutCompleteEnv(t *testing.T) {
	canaryEnv(t)
	t.Setenv("HIVECREW_REVIEW_CANARY_ENABLED", "false")
	if reviewCanaryIdentityProviderFromEnv(time.Now) != nil {
		t.Fatal("kill switch must disable the provider")
	}
	canaryEnv(t)
	t.Setenv("HIVECREW_REVIEW_CANARY_AGENT_ID", "")
	if reviewCanaryIdentityProviderFromEnv(time.Now) != nil {
		t.Fatal("incomplete identity must disable the provider")
	}
	canaryEnv(t)
	t.Setenv("HIVECREW_REVIEW_CANARY_EXPIRES_AT", "2020-01-01T00:00:00Z")
	if reviewCanaryIdentityProviderFromEnv(time.Now) != nil {
		t.Fatal("past expiry must disable the provider")
	}
	canaryEnv(t)
	if reviewCanaryIdentityProviderFromEnv(time.Now) == nil {
		t.Fatal("sanity: full env with future expiry must construct")
	}
}
