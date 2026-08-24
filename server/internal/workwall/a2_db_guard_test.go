package workwall

// B5-2: the dedicated-test-DB guard lives here (untagged) so its exact-name
// allowlist is unit-tested on every run, not only under the integration tag.
// The DSN itself is never logged anywhere in this suite.

import (
	"os"
	"strings"
	"testing"

	neturl "net/url"
)

// a2DedicatedTestDBNames is the closed allowlist of dedicated throwaway
// database names this suite may write into. Anything else — including names
// that merely CONTAIN "test" (contest, latest, testing_prod...) — is refused.
var a2DedicatedTestDBNames = map[string]bool{
	"a2b1_itest":   true,
	"a2b1_test":    true,
	"hivetest":     true,
	"multica_test": true,
}

// a2DedicatedTestDB reports whether the DSN names exactly one of the
// dedicated throwaway test databases. Substring matches are deliberately
// rejected (B5-2): "contest", "latest", and similar must never pass.
func a2DedicatedTestDB(ds string) bool {
	u, err := neturl.Parse(ds)
	if err != nil {
		return false
	}
	name := strings.TrimPrefix(u.Path, "/")
	return a2DedicatedTestDBNames[strings.ToLower(name)]
}

// a2RequireDedicatedTestDB is the single canonical DB-eligibility decision
// for every DB-backed test in this package: it returns the DSN only when the
// environment names one of the dedicated throwaway databases, and otherwise
// records an honest explicit SKIP. The DSN string is never logged or echoed.
func a2RequireDedicatedTestDB(t *testing.T) string {
	t.Helper()
	ds := os.Getenv("DATABASE_URL")
	if ds == "" {
		t.Skip("DATABASE_URL not set: no dedicated test DB, integration explicitly skipped")
	}
	if !a2DedicatedTestDB(ds) {
		t.Skip("DATABASE_URL does not name a dedicated A2 test database (exact-name allowlist): integration explicitly skipped")
	}
	return ds
}

// TestA2DedicatedTestDBExactNameOnly proves the guard is an exact-name
// allowlist: substring lookalikes (contest, latest, testing_prod) and every
// non-dedicated name are refused.
func TestA2DedicatedTestDBExactNameOnly(t *testing.T) {
	for _, ok := range []string{
		"postgres://u:p@localhost:5432/a2b1_itest?sslmode=disable",
		"postgres://u:p@localhost:5432/A2B1_TEST?sslmode=disable",
		"postgres://u:p@localhost:5432/multica_test",
	} {
		if !a2DedicatedTestDB(ok) {
			t.Errorf("dedicated test DB must be allowed")
		}
	}
	for _, bad := range []string{
		"postgres://u:p@localhost:5432/contest?sslmode=disable",
		"postgres://u:p@localhost:5432/latest?sslmode=disable",
		"postgres://u:p@localhost:5432/testing_prod?sslmode=disable",
		"postgres://u:p@localhost:5432/multica?sslmode=disable",
		"postgres://u:p@localhost:5432/test_multica_prod?sslmode=disable",
		"://bad url",
	} {
		if a2DedicatedTestDB(bad) {
			t.Errorf("non-dedicated DB must be refused")
		}
	}
}
