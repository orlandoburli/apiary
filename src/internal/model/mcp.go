package model

// MCPServer is a provider-agnostic Model Context Protocol server definition.
// It is declared once in apiary.yaml (at runner and/or agent scope) and each
// CLI runner serialises it into that provider's native MCP config format and
// location (claude `.mcp.json`, cursor `.cursor/mcp.json`, opencode `mcp` block).
//
// Only stdio (local subprocess) servers are modelled — the common case for
// code-intelligence tools like GitNexus. Remote/HTTP MCP servers are not yet
// supported.
type MCPServer struct {
	// Name is the server key the agent sees (e.g. "gitnexus"). Required.
	Name string `yaml:"name" json:"name"`
	// Command is the executable launched over stdio (e.g. "npx"). Required.
	Command string `yaml:"command" json:"command"`
	// Args are the command arguments (e.g. ["-y", "gitnexus@latest", "mcp"]).
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`
	// Env is the environment overlay for the server subprocess. ${VAR} references
	// are already expanded by the config loader before they reach the runner.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// MergeMCPServers overlays `overrides` on top of `base`, keyed by Name. An entry
// in overrides with the same Name replaces the base entry; new names are
// appended. Order is stable: base entries first (in their original order, with
// any overridden in place), then any override-only entries. Used to layer
// agent-scope MCP servers over runner-scope defaults.
func MergeMCPServers(base, overrides []MCPServer) []MCPServer {
	if len(overrides) == 0 {
		return base
	}
	if len(base) == 0 {
		return overrides
	}
	idx := make(map[string]int, len(base))
	out := make([]MCPServer, len(base))
	copy(out, base)
	for i, s := range out {
		idx[s.Name] = i
	}
	for _, o := range overrides {
		if i, ok := idx[o.Name]; ok {
			out[i] = o
			continue
		}
		idx[o.Name] = len(out)
		out = append(out, o)
	}
	return out
}
