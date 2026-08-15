package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	companyopsapi "github.com/multica-ai/multica/server/internal/companyops"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestProbeShadowDirectory(t *testing.T) {
	if os.Getenv("HIVECREW_PROBE") != "1" {
		t.Skip("probe only")
	}
	baseURL := os.Getenv("HIVECOSM_AUTHORITY_BASE_URL")
	parsed, err := url.Parse(baseURL)
	if err != nil || !isSafeCompanyOpsAuthorityURL(parsed) {
		t.Fatalf("bad base url: %v", err)
	}
	token, err := companyOpsAuthorityBearerTokenFromEnv(context.Background(), nil)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	transport := companyOpsBearerTransport{base: http.DefaultTransport, token: token, authorityScheme: parsed.Scheme, authorityHost: parsed.Host}
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	queries := db.New(pool)
	directoryClient, err := companyopsapi.NewHiveCrewDirectoryClient(baseURL, &http.Client{Transport: transport, Timeout: 30 * time.Second}, os.Getenv("HIVECOSM_TENANT_ID"))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	directory := service.NewCompanyOpsDirectoryService(directoryClient, queries)
	workspace, err := uuid.Parse("1b2a1f07-3050-4d47-aca5-6e6fdbd393d9")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	result, err := directory.GetEmployees(context.Background(), pgtype.UUID{Bytes: workspace, Valid: true}, "", "", 500, 0)
	if err != nil {
		t.Fatalf("GetEmployees failed: %v", err)
	}
	fmt.Printf("employees: %d\n", result.Total)
}
