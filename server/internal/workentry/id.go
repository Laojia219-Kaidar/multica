package workentry

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// newID returns a 16-byte random hex id, used by the in-memory store and
// service fallbacks for identifiers that have no authoritative source yet.
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a
		// deterministic value only if it does.
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}

// normalizeEventID keeps event IDs consistent across the in-memory ledger and
// PostgreSQL's UUID text representation. Empty IDs are server-generated;
// caller-supplied IDs must be valid UUIDs and are normalized to 32 lowercase
// hexadecimal characters so first-write versus replay comparisons are stable.
func normalizeEventID(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = newID()
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("event_id must be a UUID: %w", err)
	}
	return strings.ReplaceAll(id.String(), "-", ""), nil
}
