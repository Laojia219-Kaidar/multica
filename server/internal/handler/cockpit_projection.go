package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// Cockpit federation (read-only projection of the HiveCosm 1421 owner cockpit
// running on the DGX foundation base). The backend aggregates three public
// read-only cockpit endpoints and serves them as one snapshot so the bases
// page can render the foundation base card with live cockpit telemetry.
//
// Transport: a host-side SSH tunnel (launchd com.hivecosm.cockpit-tunnel)
// forwards 127.0.0.1:9421 on the Mac mini to 127.0.0.1:1421 on the DGX.
// The backend container reaches it through host.docker.internal. Nothing
// here mutates cockpit state — every call is a GET against documented
// read-only snapshot endpoints.

const cockpitFederationDefaultBaseURL = "http://host.docker.internal:9421"

var cockpitClient = &http.Client{Timeout: 6 * time.Second}

type cockpitSnapshotCache struct {
	mu        sync.Mutex
	fetchedAt time.Time
	body      []byte
	ok        bool
}

var cockpitCache cockpitSnapshotCache

func cockpitFederationBaseURL() string {
	if v := os.Getenv("HIVECREW_COCKPIT_URL"); v != "" {
		return v
	}
	return cockpitFederationDefaultBaseURL
}

type cockpitRaw struct {
	OK          bool   `json:"ok"`
	GeneratedAt string `json:"generated_at"`
	Version     string `json:"version"`
	ProjectID   string `json:"project_id"`
	Summary     json.RawMessage `json:"summary"`
}

// fetchCockpitJSON GETs one cockpit endpoint and decodes the common envelope.
// The request carries the cockpit's public hostname: vite preview enforces an
// allowedHosts check and answers 403 for plain tunnel-localhost requests.
func fetchCockpitJSON(path string) (*cockpitRaw, error) {
	req, err := http.NewRequest(http.MethodGet, cockpitFederationBaseURL()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Host = "dgx-hive-01.tailb1b6f3.ts.net"
	resp, err := cockpitClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cockpit %s: status %d", path, resp.StatusCode)
	}
	// agent-universe-index is a full registry dump (~1.2MB); allow headroom.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var raw cockpitRaw
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("cockpit %s: %w", path, err)
	}
	return &raw, nil
}

// getCockpitProjection serves GET /api/bases/cockpit-projection.
// The response aggregates:
//   - /api/health/surface     — surface health (frontend, bff, hermes-api …)
//   - /api/runtime/topology   — service topology summary
//   - /api/workforce/agent-universe-index — workforce readiness counts
//   - /api/world-entry/snapshot — world-entry registry summary
// Responses are cached for 30s so a page refresh storm cannot hammer the
// cockpit; errors degrade per-section (sections become null) and a total
// transport failure returns 503 with reason, never a partial-lie 200.
func (h *Handler) GetCockpitProjection(w http.ResponseWriter, r *http.Request) {
	cockpitCache.mu.Lock()
	defer cockpitCache.mu.Unlock()

	if cockpitCache.ok && time.Since(cockpitCache.fetchedAt) < 30*time.Second {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cockpit-Cache", "hit")
		w.Write(cockpitCache.body)
		return
	}

	type section struct {
		OK          bool            `json:"ok"`
		GeneratedAt string          `json:"generated_at,omitempty"`
		Version     string          `json:"version,omitempty"`
		ProjectID   string          `json:"project_id,omitempty"`
		Summary     json.RawMessage `json:"summary,omitempty"`
		Error       string          `json:"error,omitempty"`
	}
	type projection struct {
		FetchedAt  string   `json:"fetched_at"`
		CockpitURL string   `json:"cockpit_url"`
		OK         bool     `json:"ok"`
		Sections   struct {
			HealthSurface     *section `json:"health_surface,omitempty"`
			RuntimeTopology   *section `json:"runtime_topology,omitempty"`
			AgentUniverse     *section `json:"agent_universe,omitempty"`
			WorldEntrySnapshot *section `json:"world_entry_snapshot,omitempty"`
		} `json:"sections"`
	}

	out := projection{
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
		CockpitURL: "dgx-hive-01:1421 (ssh tunnel :9421)",
	}
	anyOK := false

	fetch := func(path string) *section {
		raw, err := fetchCockpitJSON(path)
		if err != nil {
			return &section{OK: false, Error: err.Error()}
		}
		anyOK = true
		return &section{OK: true, GeneratedAt: raw.GeneratedAt, Version: raw.Version, ProjectID: raw.ProjectID, Summary: raw.Summary}
	}

	out.Sections.HealthSurface = fetch("/api/health/surface")
	out.Sections.RuntimeTopology = fetch("/api/runtime/topology")
	out.Sections.AgentUniverse = fetch("/api/workforce/agent-universe-index")
	out.Sections.WorldEntrySnapshot = fetch("/api/world-entry/snapshot")
	out.OK = anyOK

	body, err := json.Marshal(out)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode cockpit projection")
		return
	}
	if !anyOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(body)
		return
	}
	cockpitCache.body = body
	cockpitCache.fetchedAt = time.Now()
	cockpitCache.ok = true

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cockpit-Cache", "miss")
	w.Write(body)
}
