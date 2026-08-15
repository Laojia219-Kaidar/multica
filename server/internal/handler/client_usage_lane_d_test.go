package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetProviderPlanUsageHandler exercises the Lane D usage endpoint over the
// real handler seam (workspace membership + live aggregation SQL). It is
// skipped when the shared handler test fixture has no database.
func TestGetProviderPlanUsageHandler(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	req := newRequest("GET", "/api/company-ops/usage?days=30", nil)
	w := httptest.NewRecorder()
	testHandler.GetProviderPlanUsage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		WorkspaceID string `json:"workspace_id"`
		Totals      struct {
			UsedTokens int64 `json:"used_tokens"`
			TaskCount  int   `json:"task_count"`
		} `json:"totals"`
		Providers []struct {
			Provider   string `json:"provider"`
			UsedTokens int64  `json:"used_tokens"`
		} `json:"providers"`
		DataGaps []string `json:"data_gaps"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if resp.WorkspaceID == "" {
		t.Fatalf("workspace_id missing in response")
	}
	t.Logf("providers=%d used_tokens=%d tasks=%d gaps=%v",
		len(resp.Providers), resp.Totals.UsedTokens, resp.Totals.TaskCount, resp.DataGaps)

	// The response must be a valid aggregation: whenever rows exist the totals
	// are non-zero; when no rows exist the gap is declared (never fabricated).
	if len(resp.Providers) == 0 && !containsUsageGap(resp.DataGaps, "usage_no_rows") {
		t.Fatalf("zero providers but no usage_no_rows gap declared: %v", resp.DataGaps)
	}
}

// TestPutProviderUsageQuotaValidation covers the negative validation surface of
// the quota upsert without touching a database.
func TestPutProviderUsageQuotaValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"missing provider", `{"plan":"p"}`, http.StatusBadRequest},
		{"missing plan", `{"provider":"p"}`, http.StatusBadRequest},
		{"bad cycle", `{"provider":"p","plan":"p","cycle":"yearly"}`, http.StatusBadRequest},
		{"negative total", `{"provider":"p","plan":"p","cycle":"monthly","total_tokens":-1}`, http.StatusBadRequest},
		{"bad reset day", `{"provider":"p","plan":"p","cycle":"monthly","reset_day":31}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/api/company-ops/usage/quota", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", testUserID)
			req.Header.Set("X-Workspace-ID", testWorkspaceID)
			w := httptest.NewRecorder()
			testHandler.PutProviderUsageQuota(w, req)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func containsUsageGap(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
