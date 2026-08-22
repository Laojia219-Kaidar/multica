package workentry

import (
	"strings"
	"testing"
)

func TestSanitizeHeartbeatField(t *testing.T) {
	cases := []struct {
		name  string
		input string
		leak  string
		want  string
	}{
		{name: "OSC", input: "host\x1b]0;hidden\x07-safe", leak: "hidden", want: "host-safe"},
		{name: "CSI", input: "session\x1b[31m-red\x1b[0m", leak: "\x1b", want: "session-red"},
		{name: "C1 OSC", input: "host\u009d0;hidden\u009c-safe", leak: "hidden", want: "host-safe"},
		{name: "C1 OSC with ESC ST", input: "host\u009d0;hidden\x1b\\-safe", leak: "hidden", want: "host-safe"},
		{name: "C1 CSI", input: "session\u009b31m-red\u009b0m", leak: "\u009b", want: "session-red"},
		{name: "Unicode control", input: "host\u0085-safe", leak: "\u0085", want: "host-safe"},
		{name: "Multica token", input: "agent-mul_0123456789abcdef", leak: "mul_", want: "agent-[REDACTED MULTICA TOKEN]"},
		{name: "lower bearer", input: "bearer abcdefghijklmnopqrst", leak: "abcdefghijklmnopqrst", want: "Bearer [REDACTED]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeHeartbeatField(tc.input, 255)
			if strings.Contains(got, tc.leak) {
				t.Fatalf("sensitive terminal field content survived: %q", got)
			}
			if got != tc.want {
				t.Fatalf("sanitizeHeartbeatField() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSanitizeHeartbeatFieldTruncatesRunes(t *testing.T) {
	got := sanitizeHeartbeatField("员工现场abcdef", 4)
	if got != "员工现场" {
		t.Fatalf("rune-safe truncation = %q", got)
	}
}
