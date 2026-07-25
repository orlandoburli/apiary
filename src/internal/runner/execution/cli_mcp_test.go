package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
)

var gitnexusMCP = model.MCPServer{
	Name:    "gitnexus",
	Command: "npx",
	Args:    []string{"-y", "gitnexus@latest", "mcp"},
	Env:     map[string]string{"GITNEXUS_REPO": "project-erp"},
}

func TestSetupMCP_ClaudeWritesFileAndFlag(t *testing.T) {
	r := &CliRunner{mcpFormat: "claude", mcps: []model.MCPServer{gitnexusMCP}}
	args, err := r.setupMCP()
	if err != nil {
		t.Fatalf("setupMCP: %v", err)
	}
	if len(args) != 2 || args[0] != "--mcp-config" {
		t.Fatalf("expected [--mcp-config <path>], got %v", args)
	}
	path := args[1]
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mcp file: %v", err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	gn, ok := doc.MCPServers["gitnexus"]
	if !ok {
		t.Fatalf("gitnexus server missing: %s", data)
	}
	if gn.Command != "npx" || len(gn.Args) != 3 || gn.Env["GITNEXUS_REPO"] != "project-erp" {
		t.Fatalf("unexpected server entry: %+v", gn)
	}
}

func TestSetupMCP_CursorMergesGlobalFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed an existing hand-configured server to verify the merge preserves it.
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(cursorPath), 0755); err != nil {
		t.Fatal(err)
	}
	seed := `{"mcpServers":{"other":{"command":"foo"}}}`
	if err := os.WriteFile(cursorPath, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	r := &CliRunner{mcpFormat: "cursor", mcps: []model.MCPServer{gitnexusMCP}}
	args, err := r.setupMCP()
	if err != nil {
		t.Fatalf("setupMCP: %v", err)
	}
	if len(args) != 1 || args[0] != "--approve-mcps" {
		t.Fatalf("expected [--approve-mcps], got %v", args)
	}

	data, _ := os.ReadFile(cursorPath)
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := doc.MCPServers["other"]; !ok {
		t.Errorf("merge dropped the pre-existing 'other' server: %s", data)
	}
	if _, ok := doc.MCPServers["gitnexus"]; !ok {
		t.Errorf("gitnexus not added: %s", data)
	}
}

func TestSetupMCP_OpencodeMergesLocalFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	r := &CliRunner{mcpFormat: "opencode", mcps: []model.MCPServer{gitnexusMCP}}
	args, err := r.setupMCP()
	if err != nil {
		t.Fatalf("setupMCP: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("opencode auto-discovers config; expected no run args, got %v", args)
	}

	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var doc struct {
		MCP map[string]struct {
			Type    string            `json:"type"`
			Command []string          `json:"command"`
			Enabled bool              `json:"enabled"`
			Env     map[string]string `json:"environment"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	gn, ok := doc.MCP["gitnexus"]
	if !ok {
		t.Fatalf("gitnexus missing: %s", data)
	}
	if gn.Type != "local" || !gn.Enabled {
		t.Errorf("expected type=local enabled=true, got %+v", gn)
	}
	want := []string{"npx", "-y", "gitnexus@latest", "mcp"}
	if len(gn.Command) != len(want) {
		t.Fatalf("command mismatch: got %v want %v", gn.Command, want)
	}
	for i := range want {
		if gn.Command[i] != want[i] {
			t.Fatalf("command[%d] = %q, want %q", i, gn.Command[i], want[i])
		}
	}
	if gn.Env["GITNEXUS_REPO"] != "project-erp" {
		t.Errorf("environment not mapped: %+v", gn.Env)
	}
}

func TestSetupMCP_CodexMergesManagedConfigBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	seed := "model = \"gpt-5.5\"\n\n# apiary:codex-mcp:start\n[mcp_servers.old]\ncommand = \"old\"\n# apiary:codex-mcp:end\n\nsandbox_mode = \"workspace-write\"\n"
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	r := &CliRunner{mcpFormat: "codex", mcps: []model.MCPServer{gitnexusMCP}}
	args, err := r.setupMCP()
	if err != nil {
		t.Fatalf("setupMCP: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("codex auto-discovers config; expected no run args, got %v", args)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`model = "gpt-5.5"`,
		`sandbox_mode = "workspace-write"`,
		`[mcp_servers.gitnexus]`,
		`command = "npx"`,
		`args = ["-y", "gitnexus@latest", "mcp"]`,
		`[mcp_servers.gitnexus.env]`,
		`GITNEXUS_REPO = "project-erp"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("codex config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "[mcp_servers.old]") {
		t.Errorf("old managed MCP block was not replaced:\n%s", text)
	}
}

func TestSetupMCP_UnknownFormatNoop(t *testing.T) {
	r := &CliRunner{mcpFormat: "", mcps: []model.MCPServer{gitnexusMCP}}
	args, err := r.setupMCP()
	if err != nil {
		t.Fatalf("setupMCP: %v", err)
	}
	if args != nil {
		t.Fatalf("expected nil args for unsupported provider, got %v", args)
	}
}

func TestSetupMCP_RejectsDisallowedCommand(t *testing.T) {
	bad := model.MCPServer{Name: "evil", Command: "/usr/bin/malware", Args: []string{"--exfil"}}
	r := &CliRunner{mcpFormat: "claude", mcps: []model.MCPServer{bad}}
	_, err := r.setupMCP()
	if err == nil {
		t.Fatal("expected error for disallowed command, got nil")
	}
	if !strings.Contains(err.Error(), "/usr/bin/malware") {
		t.Errorf("error should mention the rejected command, got: %v", err)
	}
}

func TestSetupMCP_AllowsKnownCommands(t *testing.T) {
	for cmd := range allowedMCPCommands {
		t.Run(cmd, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			r := &CliRunner{
				mcpFormat: "claude",
				mcps:      []model.MCPServer{{Name: "srv", Command: cmd}},
			}
			_, err := r.setupMCP()
			if err != nil {
				t.Fatalf("command %q should be allowed, got: %v", cmd, err)
			}
		})
	}
}
