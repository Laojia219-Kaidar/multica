package workentry

import "testing"

// TestDedupeKeyActorScoped locks the actor-scoped dedupe key (f86bbd472):
// two different actors working the same Goal get independent receipt anchors
// (VC-08 multi-actor), while the same actor re-registering the same Goal gets
// the exact same key (VC-03 same-actor exact replay).
func TestDedupeKeyActorScoped(t *testing.T) {
	ws := "11111111-1111-4111-8111-111111111111"
	goal := "HIVECREW-UNIVERSAL-DEVELOPMENT-ENTRY-PROJECT-OS-V1"

	keyA := DedupeKey(ws, "actor-A", goal, "", "", "")
	keyB := DedupeKey(ws, "actor-B", goal, "", "", "")
	if keyA == keyB {
		t.Fatalf("different actors must get different dedupe keys, got %q", keyA)
	}

	keyA2 := DedupeKey(ws, "actor-A", goal, "", "", "")
	if keyA != keyA2 {
		t.Fatalf("same actor must get the same dedupe key (exact replay), got %q vs %q", keyA, keyA2)
	}
}

// TestEscapeLike locks the F10 LIKE wildcard escaping: caller-supplied
// backslash/percent/underscore must match literally, never as wildcards.
func TestEscapeLike(t *testing.T) {
	cases := []struct{ in, want string }{
		{"100%_", "100\\%\\_"},
		{"a\\b", "a\\\\b"},
	}
	for _, c := range cases {
		if got := escapeLike(c.in); got != c.want {
			t.Fatalf("escapeLike(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
