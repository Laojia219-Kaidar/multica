package reviewcellconfig

import "testing"

func TestEnabledExactValueOnly(t *testing.T) {
	if !Enabled("true") {
		t.Fatal("exact true must enable Review Cell")
	}
	for _, raw := range []string{"", "TRUE", "1", " true", "true ", "false"} {
		t.Run(raw, func(t *testing.T) {
			if Enabled(raw) {
				t.Fatalf("%q must fail closed", raw)
			}
		})
	}
}
