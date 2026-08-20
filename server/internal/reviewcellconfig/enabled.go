// Package reviewcellconfig contains the fail-closed feature-switch parser for
// Review Cell wiring. Keeping it DB-free lets staging packaging verify the
// default and exact-enable boundary without starting the server.
package reviewcellconfig

// Enabled returns true only for the exact accepted compose value.
func Enabled(raw string) bool {
	return raw == "true"
}
