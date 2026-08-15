package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
)

// Live consumer integration against a real HiveCosm Authority BFF. Skipped
// unless HIVECREW_AUTHORITY_LIVE_BASE_URL is exported. The bearer token is
// resolved only through the production companyOpsAuthorityBearerTokenFromEnv
// path (legacy env or complete macOS Keychain reference); this file never
// embeds or echoes a token value. The loopback base URL is normally an SSH
// tunnel to the governed DGX release, keeping the adapter's loopback rule
// intact.
func TestCompanyOpsAuthorityLiveDispatchAuthorization(t *testing.T) {
	baseURL := os.Getenv("HIVECREW_AUTHORITY_LIVE_BASE_URL")
	if baseURL == "" {
		t.Skip("HIVECREW_AUTHORITY_LIVE_BASE_URL not set; live authority bridge not under test")
	}
	tenantID := os.Getenv("HIVECOSM_TENANT_ID")
	if tenantID == "" {
		t.Fatal("HIVECOSM_TENANT_ID is required for the live authority test")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || !isSafeCompanyOpsAuthorityURL(parsed) {
		t.Fatalf("live base URL is not a safe companyops authority origin: %v", baseURL)
	}
	token, err := companyOpsAuthorityBearerTokenFromEnv(context.Background(), nil)
	if err != nil {
		t.Fatalf("authority bearer could not be resolved from its reference: %v", err)
	}
	if token == "" {
		t.Fatal("authority bearer resolved to an empty value")
	}
	transport := companyOpsBearerTransport{
		base:            http.DefaultTransport,
		token:           token,
		authorityScheme: parsed.Scheme,
		authorityHost:   parsed.Host,
	}
	client, err := companyopsapi.NewHiveCosmDispatchAuthorizationClient(
		baseURL,
		&http.Client{Transport: transport, Timeout: 10 * time.Second},
		tenantID,
	)
	if err != nil {
		t.Fatalf("dispatch authorization client rejected the live configuration: %v", err)
	}

	allowed := companyopsapi.DispatchAuthorizationLookup{
		TenantID: tenantID,
		ExecutionIdentity: companyopsapi.DispatchAuthorizationExecutionIdentity{
			WorkOrderSourceRef: liveEnv(t, "HIVECREW_AUTHORITY_LIVE_WORK_ORDER_REF"),
			EmployeeID:         liveEnv(t, "HIVECREW_AUTHORITY_LIVE_EMPLOYEE_ID"),
			IdentityBindingID:  liveEnv(t, "HIVECREW_AUTHORITY_LIVE_IDENTITY_BINDING_ID"),
			AgentID:            liveEnv(t, "HIVECREW_AUTHORITY_LIVE_AGENT_ID"),
			AssignmentID:       liveEnv(t, "HIVECREW_AUTHORITY_LIVE_ASSIGNMENT_ID"),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	response, err := client.Resolve(ctx, allowed)
	if err != nil {
		t.Fatalf("legal five-selector identity must resolve against the live authority: %v", err)
	}
	if !response.Authorization.EventReconcile.Eligible || !response.Authorization.RecoveryOnly.Eligible {
		t.Fatalf("live authority must admit the authorized identity, got event=%v recovery=%v",
			response.Authorization.EventReconcile.Eligible, response.Authorization.RecoveryOnly.Eligible)
	}

	drifted := allowed
	drifted.ExecutionIdentity.EmployeeID = os.Getenv("HIVECREW_AUTHORITY_LIVE_WRONG_EMPLOYEE_ID")
	if drifted.ExecutionIdentity.EmployeeID == "" {
		drifted.ExecutionIdentity.EmployeeID = "KT-000-does-not-exist"
	}
	if _, err := client.Resolve(ctx, drifted); err == nil {
		t.Fatal("unknown execution identity must fail closed instead of resolving")
	}
}

func liveEnv(t *testing.T, key string) string {
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required for the live authority test", key)
	}
	return value
}
