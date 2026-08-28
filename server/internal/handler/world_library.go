package handler

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// World Library bridge status (read-only seam). The World Library is the
// canonical knowledge authority for the data & knowledge column; HiveCrew
// keeps only the local execution projection (the `dataset` table). Connecting
// the runtime is an external-authority decision that requires Owner /
// authority-side authorization, so the bridge URL is owner-configured via
// HIVECREW_WORLD_LIBRARY_URL and stays unset — the honest
// `source_available_runtime_unavailable` state — until then.
//
// This handler never mutates anything and never guesses: an unset, malformed
// or unreachable endpoint all fail closed to the same unavailable verdict.

const (
	worldLibraryAuthorityName = "World Library"
	worldLibrarySourceRef     = "noah-ark-4"
	worldLibraryLocalRole     = "execution_projection"

	// WorldLibraryStateUnavailable is the honest fail-closed verdict: the
	// authority is declared and available as a source of truth, but no
	// reachable runtime connection exists.
	WorldLibraryStateUnavailable = "source_available_runtime_unavailable"
	// WorldLibraryStateAvailable is reported only when the owner-configured
	// endpoint answers the probe successfully.
	WorldLibraryStateAvailable = "runtime_available"
)

const worldLibraryProbeTimeout = 4 * time.Second

var worldLibraryHTTPClient = &http.Client{Timeout: worldLibraryProbeTimeout}

// worldLibraryProbe reports (reachable, detail) for one configured endpoint.
type worldLibraryProbe func(url string) (bool, string)

func worldLibraryBaseURL() string {
	return strings.TrimSpace(os.Getenv("HIVECREW_WORLD_LIBRARY_URL"))
}

// probeWorldLibraryURL GETs the configured endpoint with a bounded timeout.
// Any transport error, non-200 status or oversized body fails closed.
func probeWorldLibraryURL(url string) (bool, string) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Sprintf("invalid url: %v", err)
	}
	resp, err := worldLibraryHTTPClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)); err != nil {
		return false, err.Error()
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("probe status %d", resp.StatusCode)
	}
	return true, ""
}

// WorldLibraryBridgeStatus is the structured bridge projection rendered by
// the data & knowledge page.
type WorldLibraryBridgeStatus struct {
	Configured bool   `json:"configured"`
	Reachable  *bool  `json:"reachable"`
	Detail     string `json:"detail,omitempty"`
	CheckedAt  string `json:"checked_at,omitempty"`
}

// WorldLibraryStatusResponse is the honest authority verdict for the column.
type WorldLibraryStatusResponse struct {
	Authority string                     `json:"authority"`
	SourceRef string                     `json:"source_ref"`
	LocalRole string                     `json:"local_role"`
	State     string                     `json:"state"`
	Bridge    WorldLibraryBridgeStatus   `json:"bridge"`
}

// evaluateWorldLibraryStatus maps the owner-configured bridge URL plus one
// probe result onto the fail-closed verdict. It is pure so the contract stays
// unit-testable without network or environment.
func evaluateWorldLibraryStatus(configuredURL string, probe worldLibraryProbe, now time.Time) WorldLibraryStatusResponse {
	checkedAt := now.UTC().Format(time.RFC3339)
	if configuredURL == "" {
		return WorldLibraryStatusResponse{
			Authority: worldLibraryAuthorityName,
			SourceRef: worldLibrarySourceRef,
			LocalRole: worldLibraryLocalRole,
			State:     WorldLibraryStateUnavailable,
			Bridge:    WorldLibraryBridgeStatus{Configured: false},
		}
	}
	reachable, detail := probe(configuredURL)
	state := WorldLibraryStateUnavailable
	if reachable {
		state = WorldLibraryStateAvailable
	}
	return WorldLibraryStatusResponse{
		Authority: worldLibraryAuthorityName,
		SourceRef: worldLibrarySourceRef,
		LocalRole: worldLibraryLocalRole,
		State:     state,
		Bridge: WorldLibraryBridgeStatus{
			Configured: true,
			Reachable:  &reachable,
			Detail:     detail,
			CheckedAt:  checkedAt,
		},
	}
}

// GetWorldLibraryStatus reports the World Library bridge verdict. Read-only;
// no workspace scoping needed because nothing workspace-owned is revealed.
func (h *Handler) GetWorldLibraryStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, evaluateWorldLibraryStatus(worldLibraryBaseURL(), probeWorldLibraryURL, time.Now()))
}
