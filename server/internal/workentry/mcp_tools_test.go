package workentry

import "testing"

func TestWorkMCPToolsContract(t *testing.T) {
	tools := WorkMCPTools()
	want := map[string]bool{
		"work.resolve": true, "work.register": true, "work.start": true,
		"work.status": true, "work.heartbeat": true, "work.event": true,
		"work.handoff": true, "work.finish": true, "work.sync": true,
		"work.doctor": true,
	}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if !want[tool.Name] {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate tool %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Description == "" {
			t.Fatalf("tool %q has empty description", tool.Name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("missing tool %q", name)
		}
	}
}
