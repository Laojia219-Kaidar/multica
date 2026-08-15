package workentry

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// DefaultProjection1421Path is the read-only 1421 control registry location
// (noah-ark-4). The projection never writes to it.
const DefaultProjection1421Path = "/Users/jiawei/hivecosm/noah-ark-4/config/registry/project-lifecycle.registry.json"

// ProjectionRef is one read-only 1421 project projection. Status is empty when
// the control registry seed carries no runtime status (status is a runtime-
// derived field in noah-ark-4; it is never fabricated here).
type ProjectionRef struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	ProjectType string `json:"project_type"`
	Owner       string `json:"owner"`
	Status      string `json:"status,omitempty"`
	Source      string `json:"source"`
}

// Projection1421 is the read-only projection result for the 1421 control
// registry. Pure projection: no DB write, no Task/Run/acceptance state.
type Projection1421 struct {
	SourcePath string          `json:"source_path"`
	Version    string          `json:"version,omitempty"`
	UpdatedAt  string          `json:"updated_at,omitempty"`
	Projects   []ProjectionRef `json:"projects"`
	Warnings   []string        `json:"warnings,omitempty"`
}

// registry1421ProjectSeed is the tolerant per-seed shape parsed from the 1421
// project_seeds array. Extra fields are ignored; malformed seeds are skipped.
type registry1421ProjectSeed struct {
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	ProjectType string `json:"project_type"`
	OwnerAgent  string `json:"owner_agent"`
	HumanOwner  string `json:"human_owner"`
	Status      string `json:"status"`
}

// registry1421Envelope is the tolerant top-level shape of the control registry.
type registry1421Envelope struct {
	Version      string            `json:"version"`
	UpdatedAt    string            `json:"updated_at"`
	ProjectSeeds []json.RawMessage `json:"project_seeds"`
}

// LoadProjection1421 reads the 1421 project-lifecycle.registry.json and returns
// a pure read-only projection. A malformed top-level document returns an error;
// malformed individual seeds are skipped with a warning (tolerant parsing).
func LoadProjection1421(path string) (Projection1421, error) {
	out := Projection1421{SourcePath: path, Projects: []ProjectionRef{}, Warnings: []string{}}
	path = strings.TrimSpace(path)
	if path == "" {
		return out, fmt.Errorf("1421 registry path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("read 1421 registry: %w", err)
	}
	var env registry1421Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return out, fmt.Errorf("parse 1421 registry: %w", err)
	}
	out.Version = env.Version
	out.UpdatedAt = env.UpdatedAt
	for i, seedRaw := range env.ProjectSeeds {
		ref, err := projectRef1421(seedRaw)
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("project_seeds[%d]: %v", i, err))
			continue
		}
		out.Projects = append(out.Projects, ref)
	}
	return out, nil
}

// projectRef1421 parses one project seed into a ProjectionRef. A missing
// project_id or a non-object seed is an error and is skipped by the caller.
// Status is never fabricated: an absent status stays empty.
func projectRef1421(raw json.RawMessage) (ProjectionRef, error) {
	var seed registry1421ProjectSeed
	if err := json.Unmarshal(raw, &seed); err != nil {
		return ProjectionRef{}, err
	}
	if strings.TrimSpace(seed.ProjectID) == "" {
		return ProjectionRef{}, fmt.Errorf("project_id is required")
	}
	owner := strings.TrimSpace(seed.OwnerAgent)
	if owner == "" {
		owner = strings.TrimSpace(seed.HumanOwner)
	}
	return ProjectionRef{
		ProjectID:   strings.TrimSpace(seed.ProjectID),
		Name:        strings.TrimSpace(seed.Name),
		ProjectType: strings.TrimSpace(seed.ProjectType),
		Owner:       owner,
		Status:      strings.TrimSpace(seed.Status),
		Source:      "1421",
	}, nil
}
