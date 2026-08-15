package main

import "testing"

func TestLocalOperatorSessionEnvironmentFromEnvIsExplicitAllowlist(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"local", true},
		{"development", true},
		{"test", true},
		{"", false},
		{"staging", false},
		{"production", false},
		{"prod", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.value)
			if got := localOperatorSessionEnvironmentFromEnv(); got != tc.want {
				t.Fatalf("localOperatorSessionEnvironmentFromEnv(%q): want %t, got %t", tc.value, tc.want, got)
			}
		})
	}
}
