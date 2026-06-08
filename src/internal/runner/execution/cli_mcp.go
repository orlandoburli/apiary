package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/orlandoburli/apiary/internal/model"
)

// setupMCP writes the runner's MCP servers into the provider's native config
// format/location and returns extra CLI args to inject into every run.
//
// Each provider differs in both the on-disk format and how the CLI is told to
// load it:
//
//   - claude   → a temp `{"mcpServers":{…}}` file, activated per run with the
//     trusted `--mcp-config <path>` flag (no approval prompt, no workdir mutation).
//   - cursor   → merged into the user's global `~/.cursor/mcp.json` (mcpServers
//     format), activated with `--approve-mcps`.
//   - opencode → merged into the global `~/.config/opencode/opencode.json` under
//     the `mcp` key (local format); opencode auto-discovers it, so no run args.
//
// Called once from Configure (load time, sequential across agents) so the
// global-config merges are race-free. Returns nil args when there are no servers
// or the provider has no MCP support.
func (r *CliRunner) setupMCP() ([]string, error) {
	if len(r.mcps) == 0 {
		return nil, nil
	}
	switch r.mcpFormat {
	case "claude":
		return r.setupClaudeMCP()
	case "cursor":
		return r.setupCursorMCP()
	case "opencode":
		return r.setupOpencodeMCP()
	default:
		return nil, nil
	}
}

// setupClaudeMCP writes a temp .mcp.json and returns the --mcp-config flag.
func (r *CliRunner) setupClaudeMCP() ([]string, error) {
	doc := map[string]any{"mcpServers": mcpServersObject(r.mcps)}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal claude mcp: %w", err)
	}
	f, err := os.CreateTemp("", "apiary-mcp-*.json")
	if err != nil {
		return nil, fmt.Errorf("create claude mcp file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		return nil, fmt.Errorf("write claude mcp file: %w", err)
	}
	return []string{"--mcp-config", f.Name()}, nil
}

// setupCursorMCP merges the servers into ~/.cursor/mcp.json and returns the
// --approve-mcps flag so the headless agent auto-trusts them.
func (r *CliRunner) setupCursorMCP() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	path := filepath.Join(home, ".cursor", "mcp.json")
	if err := mergeMCPServersFile(path, r.mcps); err != nil {
		return nil, fmt.Errorf("merge cursor mcp: %w", err)
	}
	return []string{"--approve-mcps"}, nil
}

// setupOpencodeMCP merges the servers into the global opencode.json `mcp` block.
// opencode auto-discovers the global config, so no per-run args are needed.
func (r *CliRunner) setupOpencodeMCP() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create opencode config dir: %w", err)
	}
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &doc)
	}
	mcp, _ := doc["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
	}
	for _, m := range r.mcps {
		entry := map[string]any{
			"type":    "local",
			"command": append([]string{m.Command}, m.Args...),
			"enabled": true,
		}
		if len(m.Env) > 0 {
			entry["environment"] = m.Env
		}
		mcp[m.Name] = entry
	}
	doc["mcp"] = mcp
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal opencode config: %w", err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		return nil, fmt.Errorf("write opencode config: %w", err)
	}
	return nil, nil
}

// mcpServersObject builds the `mcpServers` map shared by the claude and cursor
// formats: {name: {command, args?, env?}}.
func mcpServersObject(mcps []model.MCPServer) map[string]any {
	out := make(map[string]any, len(mcps))
	for _, m := range mcps {
		entry := map[string]any{"command": m.Command}
		if len(m.Args) > 0 {
			entry["args"] = m.Args
		}
		if len(m.Env) > 0 {
			entry["env"] = m.Env
		}
		out[m.Name] = entry
	}
	return out
}

// mergeMCPServersFile reads an existing {"mcpServers":{…}} JSON file (claude /
// cursor format), overlays the given servers by name, and writes it back,
// preserving any servers the user configured by hand.
func mergeMCPServersFile(path string, mcps []model.MCPServer) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &doc)
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	for name, entry := range mcpServersObject(mcps) {
		servers[name] = entry
	}
	doc["mcpServers"] = servers
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0644)
}
