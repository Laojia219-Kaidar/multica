package workwall

// B5-2: the dedicated-test-DB guard lives here (untagged) so its exact-name
// allowlist is unit-tested on every run, not only under the integration tag.
// The DSN itself is never logged anywhere in this suite.

import (
	"os"
	"strconv"
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

// a2AllowedDBHosts is the closed allowlist of local-only hosts a dedicated
// test database may live on. Any remote host is refused (B7): an unknown
// machine can never be proven to be a throwaway, so it fails closed.
var a2AllowedDBHosts = map[string]bool{
	"localhost": true,
	"127.0.0.1": true,
	"::1":       true,
	"[::1]":     true,
}

// a2ProductionDBPort is the default PostgreSQL port this product's
// production/shared containers listen on. A DSN targeting it is refused
// (B7/B8): dedicated test databases must live on a non-production port.
const a2ProductionDBPort = 5432

// a2DedicatedTestPort parses the DSN's port and applies the B8 constraints:
// it must be present, a plain decimal run of digits (no sign, whitespace,
// underscores, or separators), in the valid TCP range 1..65535, and must not
// normalize to the default production port (so "05432" and "5432" are both
// rejected). Any parse or range failure is fail-closed.
func a2DedicatedTestPort(raw string) bool {
	if raw == "" {
		return false // missing port: driver default is 5432
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return false // nonnumeric characters, signs, separators, spaces
		}
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return false // unparseable (overflow) port
	}
	if port < 1 || port > 65535 {
		return false // outside the valid TCP port range
	}
	if port == a2ProductionDBPort {
		return false // normalized production port (also rejects "05432")
	}
	return true
}

// a2DedicatedTestDB reports whether the DSN names exactly one of the
// dedicated throwaway test databases AND is reachable only fail-closed:
// the host must be a local loopback and the port must NOT be the default
// production port. Substring name matches are deliberately rejected (B5-2):
// "contest", "latest", and similar must never pass.
func a2DedicatedTestDB(ds string) bool {
	u, err := neturl.Parse(ds)
	if err != nil {
		return false
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !a2DedicatedTestDBNames[strings.ToLower(name)] {
		return false
	}
	// B7 fail-closed host: only an explicit local loopback is allowed; a
	// missing host or any remote name is refused.
	if !a2AllowedDBHosts[strings.ToLower(u.Hostname())] {
		return false
	}
	// B7/B8 fail-closed port: an explicit, numeric, in-range port that does
	// not normalize to the default production port. Leading-zero forms such
	// as "05432" parse to 5432 and are rejected by the same rule.
	if !a2DedicatedTestPort(u.Port()) {
		return false
	}
	return true
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
	// Allowed: dedicated name + local host + dedicated non-production port.
	for _, ok := range []string{
		"postgres://u:p@localhost:54329/a2b1_itest?sslmode=disable",
		"postgres://u:p@127.0.0.1:54329/A2B1_TEST?sslmode=disable",
		"postgres://u:p@localhost:54330/multica_test",
	} {
		if !a2DedicatedTestDB(ok) {
			t.Errorf("dedicated test DB must be allowed")
		}
	}
	// Refused: substring name lookalikes and every non-dedicated name.
	for _, bad := range []string{
		"postgres://u:p@localhost:54329/contest?sslmode=disable",
		"postgres://u:p@localhost:54329/latest?sslmode=disable",
		"postgres://u:p@localhost:54329/testing_prod?sslmode=disable",
		"postgres://u:p@localhost:54329/multica?sslmode=disable",
		"postgres://u:p@localhost:54329/test_multica_prod?sslmode=disable",
		"://bad url",
	} {
		if a2DedicatedTestDB(bad) {
			t.Errorf("non-dedicated DB must be refused")
		}
	}
}

// TestA2DedicatedTestDBHostAndPortFailClosed proves the B7 additions: a
// remote host is refused no matter how correct the database name is, and the
// default production port (or a missing port) is refused even on localhost.
func TestA2DedicatedTestDBHostAndPortFailClosed(t *testing.T) {
	// Remote hosts: never allowed, regardless of the dedicated name.
	for _, remote := range []string{
		"db.example.com",
		"postgres.internal.hivecosm.io",
		"10.0.0.5",
		"192.168.1.20",
		"dgx-spark.local",
	} {
		ds := "postgres://u:p@" + remote + ":54329/a2b1_itest?sslmode=disable"
		if a2DedicatedTestDB(ds) {
			t.Errorf("remote host must be refused (fail closed)")
		}
	}

	// Missing host entirely: also refused.
	if a2DedicatedTestDB("postgres://u:p@:54329/a2b1_itest?sslmode=disable") {
		t.Errorf("missing host must be refused (fail closed)")
	}

	// Default production port on localhost: refused.
	for _, prod := range []string{
		"postgres://u:p@localhost:5432/a2b1_itest?sslmode=disable",
		"postgres://u:p@127.0.0.1:5432/a2b1_itest?sslmode=disable",
	} {
		if a2DedicatedTestDB(prod) {
			t.Errorf("default production port must be refused (fail closed)")
		}
	}

	// Missing port (driver default = 5432): refused.
	if a2DedicatedTestDB("postgres://u:p@localhost/a2b1_itest?sslmode=disable") {
		t.Errorf("missing port must be refused (fail closed)")
	}

	// IPv6 loopback with a dedicated port remains allowed.
	if !a2DedicatedTestDB("postgres://u:p@[::1]:54329/a2b1_itest?sslmode=disable") {
		t.Errorf("ipv6 loopback with dedicated port must be allowed")
	}
}

// TestA2DedicatedTestDBPortNumericParsing proves the B8 constraints: the
// port must parse as a number in 1..65535 and must not normalize to the
// production port — so "05432" is rejected exactly like "5432" — while
// nonnumeric, zero, out-of-range, and missing ports are refused outright.
func TestA2DedicatedTestDBPortNumericParsing(t *testing.T) {
	// Direct constraint-level checks.
	for _, ok := range []string{"54329", "1", "65535"} {
		if !a2DedicatedTestPort(ok) {
			t.Errorf("port %q must be accepted (numeric, in range, non-production)", ok)
		}
	}
	for _, bad := range []string{"", "abc", "54a32", "0", "-1", "65536", "99999", "5432", "05432", "+54329", "5432 ", " 54329", "5,429", "0x...", "٣٥"} {
		if a2DedicatedTestPort(bad) {
			t.Errorf("port %q must be refused (fail closed)", bad)
		}
	}

	// Full-DSN form: leading-zero production port and invalid ports are
	// rejected even with an allowlisted name and loopback host.
	for _, ds := range []string{
		"postgres://u:p@localhost:05432/a2b1_itest?sslmode=disable",
		"postgres://u:p@localhost:0/a2b1_itest?sslmode=disable",
		"postgres://u:p@localhost:65536/a2b1_itest?sslmode=disable",
		"postgres://u:p@localhost:abc/a2b1_itest?sslmode=disable",
		"postgres://u:p@localhost/a2b1_itest?sslmode=disable",
	} {
		if a2DedicatedTestDB(ds) {
			t.Errorf("DSN with invalid/production port must be refused (fail closed)")
		}
	}

	// Sanity: the accepted dedicated-port DSN still passes the whole guard.
	if !a2DedicatedTestDB("postgres://u:p@localhost:54329/a2b1_itest?sslmode=disable") {
		t.Errorf("valid dedicated-port DSN must be allowed")
	}
}
