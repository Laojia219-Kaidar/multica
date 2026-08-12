package service

import (
	"testing"
)

func TestComputeDigest(t *testing.T) {
	d1 := ComputeDigest([]byte(`{"idempotency_key":"abc"}`))
	d2 := ComputeDigest([]byte(`{"idempotency_key":"abc"}`))
	d3 := ComputeDigest([]byte(`{"idempotency_key":"xyz"}`))

	if d1 != d2 {
		t.Fatalf("same input should produce same digest: %s vs %s", d1, d2)
	}
	if d1 == d3 {
		t.Fatalf("different input should produce different digest: %s", d1)
	}
	if len(d1) != 64 {
		t.Fatalf("expected 64-char hex digest, got %d chars", len(d1))
	}
}

func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unique", errString("duplicate key value violates unique constraint"), true},
		{"pg code", errString("ERROR: 23505"), true},
		{"other", errString("connection refused"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUniqueViolation(tc.err); got != tc.want {
				t.Fatalf("isUniqueViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }
