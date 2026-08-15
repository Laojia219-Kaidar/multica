package workentry

import (
	"crypto/rand"
	"encoding/hex"
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
