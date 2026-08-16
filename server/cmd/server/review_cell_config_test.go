package main

import "testing"

func TestReviewCellConfigFromEnvIsAlwaysAuthorityOnly(t *testing.T) {
	cfg := reviewCellConfigFromEnv()
	if !cfg.AuthorityDispatchOnly {
		t.Fatal("review-cell production config must be Authority-only")
	}
}
