package workentry

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp1421(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "project-lifecycle.registry.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp registry: %v", err)
	}
	return path
}

func TestLoadProjection1421ParsesSeeds(t *testing.T) {
	path := writeTemp1421(t, `{
		"version": "ProjectLifecycleRegistryControlV1",
		"updated_at": "2026-07-01T18:45:00+08:00",
		"project_seeds": [
			{"project_id":"PRJ-G61-HIVECOSM-PUBLIC-SITE","name":"HiveCosm Public Operating Site","project_type":"G-SERIES","owner_agent":"Coco","human_owner":"William"},
			{"project_id":"PRJ-MOTHER-2026-001","name":"Noah Ark 4","project_type":"MOTHER","owner_agent":"Coco"}
		]
	}`)
	res, err := LoadProjection1421(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(res.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d (%+v)", len(res.Projects), res.Projects)
	}
	first := res.Projects[0]
	if first.ProjectID != "PRJ-G61-HIVECOSM-PUBLIC-SITE" || first.ProjectType != "G-SERIES" || first.Owner != "Coco" || first.Source != "1421" {
		t.Fatalf("unexpected first projection: %+v", first)
	}
	if first.Status != "" {
		t.Fatalf("status must not be fabricated, got %q", first.Status)
	}
}

func TestLoadProjection1421ToleratesMalformedSeeds(t *testing.T) {
	path := writeTemp1421(t, `{
		"project_seeds": [
			{"project_id":"PRJ-OK-1","name":"Good"},
			"not-an-object",
			{"name":"missing project_id"},
			{"project_id":"PRJ-OK-2","name":"Also good","project_type":"ASSET"}
		]
	}`)
	res, err := LoadProjection1421(path)
	if err != nil {
		t.Fatalf("malformed seeds must be tolerated, got error: %v", err)
	}
	if len(res.Projects) != 2 {
		t.Fatalf("expected 2 good seeds, got %d (%+v)", len(res.Projects), res.Projects)
	}
	if len(res.Warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d (%+v)", len(res.Warnings), res.Warnings)
	}
}

func TestLoadProjection1421MalformedDocumentFails(t *testing.T) {
	path := writeTemp1421(t, `{ not valid json`)
	if _, err := LoadProjection1421(path); err == nil {
		t.Fatalf("malformed document must return an error")
	}
}

func TestLoadProjection1421MissingSeedsEmpty(t *testing.T) {
	path := writeTemp1421(t, `{"version":"x"}`)
	res, err := LoadProjection1421(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(res.Projects) != 0 {
		t.Fatalf("expected no projects, got %+v", res.Projects)
	}
}

func TestLoadProjection1421RealRegistry(t *testing.T) {
	if _, err := os.Stat(DefaultProjection1421Path); err != nil {
		t.Skipf("real 1421 registry not available: %v", err)
	}
	res, err := LoadProjection1421(DefaultProjection1421Path)
	if err != nil {
		t.Fatalf("load real registry: %v", err)
	}
	if len(res.Projects) < 3 {
		t.Fatalf("expected >=3 seeds, got %d", len(res.Projects))
	}
	found := false
	for _, p := range res.Projects {
		if p.ProjectID == "PRJ-G61-HIVECOSM-PUBLIC-SITE" {
			found = true
			if p.ProjectType != "G-SERIES" {
				t.Fatalf("G61 project_type = %q, want G-SERIES", p.ProjectType)
			}
			if p.Source != "1421" || p.Owner == "" {
				t.Fatalf("G61 projection missing owner/source: %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("PRJ-G61-HIVECOSM-PUBLIC-SITE not found in projection")
	}
}
