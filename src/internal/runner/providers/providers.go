// Package providers register concrete runner types by pre-configuring generic
// execution engines (cli, api) with provider-specific defaults.
package providers

import (
	"github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/runner/execution"
)

func init() {
	// ── CLI providers ──────────────────────────────────────────────────────────
	cliFactory := func(cmd string, extra map[string]any) func() runner.Runner {
		return func() runner.Runner {
			cfg := map[string]any{"command": cmd, "model_flag": "--model", "prompt_flag": "-p"}
			for k, v := range extra {
				cfg[k] = v
			}
			r := &execution.CliRunner{}
			_ = r.Configure(cfg)
			return r
		}
	}

	// mcp_format selects how the CLI runner serialises MCP servers into the
	// provider's native config (see execution.CliRunner.setupMCP).
	// stream-json output is required for usage accounting, final-result
	// extraction, and rate-limit detection (see execution/cli.go); user `args`
	// are appended after these, and claude tolerates repeated flags (last wins).
	// turns_flag is only emitted when the agent sets max_turns > 0 (see
	// execution.CliRunner.Run), so runs stay uncapped by default.
	runner.Register("claude-cli", cliFactory("claude", map[string]any{
		"args":       []any{"--output-format", "stream-json", "--verbose"},
		"mcp_format": "claude",
		"turns_flag": "--max-turns",
	}))

	runner.Register("opencode-cli", cliFactory("opencode", map[string]any{
		"args":              []any{"run"},
		"prompt_flag":       "",
		"prompt_positional": true,
		"mcp_format":        "opencode",
	}))

	// Cursor agent CLI — requires the `agent` binary from https://cursor.com/install.
	// Runs headlessly with stream-json output; --force auto-approves file changes.
	runner.Register("cursor-cli", cliFactory("agent", map[string]any{
		"args":              []any{"-p", "--output-format", "stream-json", "--force"},
		"prompt_flag":       "",
		"prompt_positional": true,
		"mcp_format":        "cursor",
	}))

	// ── API providers ──────────────────────────────────────────────────────────
	// ApiRunner uses default BuildBody and ParseResponse (OpenAI-compatible format).
	// Providers with a different schema should pass custom functions.
	runner.Register("opencode-api", func() runner.Runner {
		return &execution.ApiRunner{
			Endpoint:   "https://api.opencode.ai/v1/chat/completions",
			AuthHeader: "Bearer ${OPENCODE_API_KEY}",
		}
	})
}
