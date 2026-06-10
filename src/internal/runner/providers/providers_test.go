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
