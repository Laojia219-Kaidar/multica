package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchMiniMaxRemains(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"remains":[{"window_type":"5h","total":1000,"used":200,"remaining":800,"reset_at":0},{"window_type":"weekly","total":5000,"used":1000,"remaining":4000,"reset_at":0}]}`)
	}))
	defer server.Close()

	cfg := VendorQuotaConfig{
		MiniMaxAPIKey: "test-key",
		HTTPClient:    server.Client(),
	}
	obs, err := fetchMiniMaxRemainsAt(context.Background(), cfg, server.URL+"/v1/token_plan/remains")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("obs = %d", len(obs))
	}
	if obs[0].WindowKind != "5h" || obs[0].Remaining == nil || *obs[0].Remaining != 800 {
		t.Fatalf("first window = %+v", obs[0])
	}
}

func TestMinimaxAndVolcWindowKinds(t *testing.T) {
	if minimaxWindowKind("weekly") != "7d" {
		t.Fatal("weekly mapping")
	}
	if volcengineWindowKind("1d") != "daily" {
		t.Fatal("daily mapping")
	}
}

func TestClassifyProviderPlan_MiniMaxRuntime(t *testing.T) {
	c := ClassifyProviderPlan("minimax-m2.7", "secure minimax", "qwen", "cloud")
	if c.Provider != "MiniMax" {
		t.Fatalf("provider = %q", c.Provider)
	}
}

func TestMaskSecret_Redaction(t *testing.T) {
	if MaskSecret("abcd1234wxyz9876") != "abcd…9876" {
		t.Fatalf("unexpected mask")
	}
}
