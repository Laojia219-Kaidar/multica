package db

import (
	"strings"
	"testing"
)

func TestLockWriterLeasesForCompletionUsesNonNullableDatabaseExpiryPredicate(t *testing.T) {
	const predicate = "CASE WHEN expires_at > clock_timestamp() THEN true ELSE false END AS not_expired"
	if !strings.Contains(lockWriterLeasesForCompletion, predicate) {
		t.Fatalf("completion lease query does not normalize NULL expiry: %s", lockWriterLeasesForCompletion)
	}
}
