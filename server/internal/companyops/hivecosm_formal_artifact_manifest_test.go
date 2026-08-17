package companyops

import (
	"strings"
	"testing"
)

func TestFormalArtifactManifestIDCanonical(t *testing.T) {
	const promotionID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c010"
	got, err := FormalArtifactManifestID(promotionID)
	if err != nil || got != "FA-HCW-"+strings.ToUpper(promotionID) {
		t.Fatalf("manifest = %q, err=%v", got, err)
	}
	for _, invalid := range []string{"", "NOT-A-UUID", strings.ToUpper(promotionID)} {
		if _, err := FormalArtifactManifestID(invalid); err == nil {
			t.Fatalf("FormalArtifactManifestID(%q) accepted invalid identity", invalid)
		}
	}
}
