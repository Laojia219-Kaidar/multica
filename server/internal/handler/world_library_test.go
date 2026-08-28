package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

var wlTestNow = time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)

// TestEvaluateWorldLibraryStatus_UnconfiguredFailsClosed pins the honest
// default: with no owner-configured bridge URL the verdict stays
// source_available_runtime_unavailable and reachable stays null — never a
// guessed connection.
func TestEvaluateWorldLibraryStatus_UnconfiguredFailsClosed(t *testing.T) {
	out := evaluateWorldLibraryStatus("", func(string) (bool, string) {
		t.Fatal("probe must not run when unconfigured")
		return true, ""
	}, wlTestNow)
	if out.State != WorldLibraryStateUnavailable {
		t.Fatalf("state = %q, want %q", out.State, WorldLibraryStateUnavailable)
	}
	if out.Bridge.Configured {
		t.Fatalf("configured must be false: %+v", out.Bridge)
	}
	if out.Bridge.Reachable != nil {
		t.Fatalf("reachable must stay null when unconfigured: %+v", out.Bridge)
	}
	if out.Authority != worldLibraryAuthorityName || out.SourceRef != worldLibrarySourceRef {
		t.Fatalf("authority declaration wrong: %+v", out)
	}
}

// TestEvaluateWorldLibraryStatus_ConfiguredUnreachableFailsClosed pins that a
// configured but unreachable endpoint still reports the unavailable state and
// keeps the transport detail for diagnosis.
func TestEvaluateWorldLibraryStatus_ConfiguredUnreachableFailsClosed(t *testing.T) {
	out := evaluateWorldLibraryStatus("http://127.0.0.1:1", func(string) (bool, string) {
		return false, "connection refused"
	}, wlTestNow)
	if out.State != WorldLibraryStateUnavailable {
		t.Fatalf("state = %q, want %q", out.State, WorldLibraryStateUnavailable)
	}
	if out.Bridge.Configured != true || out.Bridge.Reachable == nil || *out.Bridge.Reachable {
		t.Fatalf("bridge projection wrong: %+v", out.Bridge)
	}
	if out.Bridge.Detail == "" {
		t.Fatalf("unreachable detail must be kept: %+v", out.Bridge)
	}
	if out.Bridge.CheckedAt == "" {
		t.Fatalf("checked_at must be stamped: %+v", out.Bridge)
	}
}

// TestEvaluateWorldLibraryStatus_ConfiguredReachable pins the only path to
// runtime_available: configured AND probe success.
func TestEvaluateWorldLibraryStatus_ConfiguredReachable(t *testing.T) {
	out := evaluateWorldLibraryStatus("https://world-library.example", func(string) (bool, string) {
		return true, ""
	}, wlTestNow)
	if out.State != WorldLibraryStateAvailable {
		t.Fatalf("state = %q, want %q", out.State, WorldLibraryStateAvailable)
	}
	if out.Bridge.Reachable == nil || !*out.Bridge.Reachable {
		t.Fatalf("reachable must be true: %+v", out.Bridge)
	}
}

// TestProbeWorldLibraryURL exercises the real probe against httptest servers:
// 200 answers reachable; non-200 and connection refused fail closed.
func TestProbeWorldLibraryURL(t *testing.T) {
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okServer.Close()
	if reachable, _ := probeWorldLibraryURL(okServer.URL); !reachable {
		t.Fatalf("200 must answer reachable")
	}

	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badServer.Close()
	if reachable, detail := probeWorldLibraryURL(badServer.URL); reachable || detail == "" {
		t.Fatalf("500 must fail closed with detail, got reachable=%v detail=%q", reachable, detail)
	}

	if reachable, detail := probeWorldLibraryURL("http://127.0.0.1:1"); reachable || detail == "" {
		t.Fatalf("refused connection must fail closed, got reachable=%v detail=%q", reachable, detail)
	}

	parsed, err := url.Parse(okServer.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	parsed.Host = "invalid.invalid.example"
	if reachable, _ := probeWorldLibraryURL(parsed.String()); reachable {
		t.Fatalf("unresolvable host must fail closed")
	}
}

// TestGetWorldLibraryStatus_Handler pins the HTTP contract: 200 with the
// structured honest verdict, decodable without any database dependency.
func TestGetWorldLibraryStatus_Handler(t *testing.T) {
	t.Setenv("HIVECREW_WORLD_LIBRARY_URL", "")
	h := &Handler{}
	w := httptest.NewRecorder()
	h.GetWorldLibraryStatus(w, httptest.NewRequest(http.MethodGet, "/api/datasets/world-library", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var out WorldLibraryStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.State != WorldLibraryStateUnavailable {
		t.Fatalf("default state = %q, want %q", out.State, WorldLibraryStateUnavailable)
	}
	if out.LocalRole != worldLibraryLocalRole {
		t.Fatalf("local_role = %q, want %q", out.LocalRole, worldLibraryLocalRole)
	}
}
