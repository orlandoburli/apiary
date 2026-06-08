package model

import "testing"

func names(mcps []MCPServer) []string {
	out := make([]string, len(mcps))
	for i, m := range mcps {
		out[i] = m.Name
	}
	return out
}

func TestMergeMCPServers_AgentOverridesRunnerByName(t *testing.T) {
	base := []MCPServer{
		{Name: "gitnexus", Command: "npx", Args: []string{"runner"}},
		{Name: "shared", Command: "shared-cmd"},
	}
	overrides := []MCPServer{
		{Name: "gitnexus", Command: "npx", Args: []string{"agent"}}, // overrides
		{Name: "extra", Command: "extra-cmd"},                       // appended
	}
	got := MergeMCPServers(base, overrides)

	if want := []string{"gitnexus", "shared", "extra"}; len(got) != 3 {
		t.Fatalf("expected order %v, got %v", want, names(got))
	}
	if got[0].Args[0] != "agent" {
		t.Errorf("gitnexus should be overridden by agent scope, got args %v", got[0].Args)
	}
	if names(got)[2] != "extra" {
		t.Errorf("expected 'extra' appended last, got %v", names(got))
	}
}

func TestMergeMCPServers_Empties(t *testing.T) {
	base := []MCPServer{{Name: "a", Command: "x"}}
	if got := MergeMCPServers(base, nil); len(got) != 1 || got[0].Name != "a" {
		t.Errorf("nil overrides should return base, got %v", names(got))
	}
	if got := MergeMCPServers(nil, base); len(got) != 1 || got[0].Name != "a" {
		t.Errorf("nil base should return overrides, got %v", names(got))
	}
	if got := MergeMCPServers(nil, nil); len(got) != 0 {
		t.Errorf("both nil should return empty, got %v", names(got))
	}
}
