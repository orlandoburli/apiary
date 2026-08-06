package providers

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/runner"
)

// echoRun instantiates a registered provider, swaps its command for `echo`,
// and returns the argv line the provider would have executed.
func echoRun(t *testing.T, providerID string, userConfig map[string]any) string {
	t.Helper()
	return echoRunReq(t, providerID, userConfig, model.RunRequest{
		Cell:     model.SourceItem{Title: "ping"},
		WorkerID: "test",
	})
}

// echoRunReq is echoRun with a caller-supplied RunRequest, for asserting how
// request fields (e.g. MaxTurns) surface in the provider's argv.
func echoRunReq(t *testing.T, providerID string, userConfig map[string]any, req model.RunRequest) string {
	t.Helper()
	r, ok := runner.New(providerID)
	if !ok {
		t.Fatalf("provider %q not registered", providerID)
	}
	cfg := map[string]any{"command": "echo"}
	for k, v := range userConfig {
		cfg[k] = v
	}
	if err := r.Configure(cfg); err != nil {
		t.Fatalf("configure: %v", err)
	}
	res, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res.Output
}

func TestClaudeCliPresetIncludesStreamJSONArgs(t *testing.T) {
	out := echoRun(t, "claude-cli", nil)
	if !strings.Contains(out, "--output-format stream-json --verbose") {
		t.Errorf("claude-cli preset args missing from argv: %q", out)
	}
	if !strings.Contains(out, "-p ") {
		t.Errorf("claude-cli prompt flag missing from argv: %q", out)
	}
}

func TestClaudeCliUserArgsAppendAfterPreset(t *testing.T) {
	out := echoRun(t, "claude-cli", map[string]any{"args": []any{"--add-dir", "/tmp"}})
	want := "--output-format stream-json --verbose --add-dir /tmp"
	if !strings.Contains(out, want) {
		t.Errorf("user args not appended after preset args: %q", out)
	}
}

func TestClaudeCliMaxTurnsEmitsFlag(t *testing.T) {
	out := echoRunReq(t, "claude-cli", nil, model.RunRequest{
		Cell:     model.SourceItem{Title: "ping"},
		WorkerID: "test",
		MaxTurns: 30,
	})
	if !strings.Contains(out, "--max-turns 30") {
		t.Errorf("max_turns > 0 must emit --max-turns in argv: %q", out)
	}
}

func TestClaudeCliZeroMaxTurnsOmitsFlag(t *testing.T) {
	out := echoRun(t, "claude-cli", nil)
	if strings.Contains(out, "--max-turns") {
		t.Errorf("max_turns 0 must omit --max-turns from argv (uncapped): %q", out)
	}
}

func TestOpencodeCliPresetKeepsRunSubcommand(t *testing.T) {
	out := echoRun(t, "opencode-cli", nil)
	if !strings.HasPrefix(out, "run ") {
		t.Errorf("opencode-cli preset must start argv with `run`: %q", out)
	}
}

func TestCodexCliPresetUsesExecWithWorkspaceWrite(t *testing.T) {
	out := echoRun(t, "codex-cli", nil)
	if !strings.HasPrefix(out, "exec --sandbox workspace-write ") {
		t.Errorf("codex-cli preset must start argv with `exec --sandbox workspace-write`: %q", out)
	}
}

// Regression for #370: headless claude denies every tool call unless a
// permission flag is passed, and the run still reports success — so the agent
// finishes having written nothing. The default must bypass the prompt.
func TestClaudeCliBypassesPermissionsByDefault(t *testing.T) {
	out := echoRun(t, "claude-cli", nil)
	if !strings.Contains(out, "--dangerously-skip-permissions") {
		t.Errorf("claude-cli must bypass permissions by default: %q", out)
	}
}

func TestClaudeCliPermissionModeNarrowsToNamedMode(t *testing.T) {
	out := echoRun(t, "claude-cli", map[string]any{"permission_mode": "acceptEdits"})
	if !strings.Contains(out, "--permission-mode acceptEdits") {
		t.Errorf("named permission mode missing from argv: %q", out)
	}
	if strings.Contains(out, "--dangerously-skip-permissions") {
		t.Errorf("named permission mode must replace the bypass flag: %q", out)
	}
}

func TestClaudeCliPermissionModeDefaultEmitsNoFlags(t *testing.T) {
	out := echoRun(t, "claude-cli", map[string]any{"permission_mode": "default"})
	if strings.Contains(out, "--dangerously-skip-permissions") || strings.Contains(out, "--permission-mode") {
		t.Errorf("permission_mode default must emit no permission flags: %q", out)
	}
}

func TestClaudeCliAllowedToolsEmitsCommaList(t *testing.T) {
	out := echoRun(t, "claude-cli", map[string]any{
		"permission_mode": "default",
		"allowed_tools":   []any{"Bash", "mcp__atlassian__jira_add_comment"},
	})
	if !strings.Contains(out, "--allowedTools Bash,mcp__atlassian__jira_add_comment") {
		t.Errorf("allowed_tools missing from argv: %q", out)
	}
}

// cursor's --force moved from args to permission_bypass_args; the emitted argv
// must not change, and the flag must now be switchable off like claude's.
func TestCursorCliStillForcesByDefault(t *testing.T) {
	out := echoRun(t, "cursor-cli", nil)
	if !strings.Contains(out, "--force") {
		t.Errorf("cursor-cli must keep --force by default: %q", out)
	}
}

func TestCursorCliPermissionModeDefaultDropsForce(t *testing.T) {
	out := echoRun(t, "cursor-cli", map[string]any{"permission_mode": "default"})
	if strings.Contains(out, "--force") {
		t.Errorf("permission_mode default must drop --force: %q", out)
	}
}

// A provider with no permission flags must reject a mode it cannot honour,
// rather than silently ignoring it and leaving the agent tool-less.
func TestUnsupportedPermissionModeFailsAtConfigure(t *testing.T) {
	r, ok := runner.New("opencode-cli")
	if !ok {
		t.Fatal("opencode-cli not registered")
	}
	err := r.Configure(map[string]any{"command": "echo", "permission_mode": "bypass"})
	if err == nil {
		t.Fatal("expected configure to reject an unsupported permission_mode")
	}
	if !strings.Contains(err.Error(), "not supported by this provider") {
		t.Errorf("unexpected error: %v", err)
	}
}
