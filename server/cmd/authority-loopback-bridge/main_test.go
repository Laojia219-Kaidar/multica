package main

import "testing"

func TestBridgeConstantsAreLocked(t *testing.T) {
	if bridgePort != "3151" || targetAddress != "127.0.0.1:3150" { t.Fatal("authority bridge endpoint drifted") }
}
