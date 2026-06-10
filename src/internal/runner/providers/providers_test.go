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
	res, err := r.Run(context.Background(), model.RunRequest{
		Cell:     model.SourceItem{Title: "ping"},
		WorkerID: "test",
	})
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

func TestOpencodeCliPresetKeepsRunSubcommand(t *testing.T) {
	out := echoRun(t, "opencode-cli", nil)
	if !strings.HasPrefix(out, "run ") {
		t.Errorf("opencode-cli preset must start argv with `run`: %q", out)
	}
}
