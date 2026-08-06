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
	// permission_mode defaults to "bypass" (→ --dangerously-skip-permissions):
	// claude in headless `-p` mode cannot show a permission prompt, so without it
	// every Bash/Grep/MCP call is denied and the agent completes having silently
	// written nothing. This mirrors cursor-cli's --force. Narrow it per runner
	// with permission_mode: default|acceptEdits|plan and allowed_tools.
	runner.Register("claude-cli", cliFactory("claude", map[string]any{
		"args":                   []any{"--output-format", "stream-json", "--verbose"},
		"mcp_format":             "claude",
		"turns_flag":             "--max-turns",
		"permission_mode":        "bypass",
		"permission_flag":        "--permission-mode",
		"permission_bypass_args": []any{"--dangerously-skip-permissions"},
		"allowed_tools_flag":     "--allowedTools",
	}))

	runner.Register("opencode-cli", cliFactory("opencode", map[string]any{
		"args":              []any{"run"},
		"prompt_flag":       "",
		"prompt_positional": true,
		"mcp_format":        "opencode",
	}))

	// Codex CLI — requires the `codex` binary from OpenAI's Codex CLI.
	// Runs non-interactively with workspace-write sandboxing so implementation
	// agents can edit the checked-out task workspace.
	runner.Register("codex-cli", cliFactory("codex", map[string]any{
		"args":              []any{"exec", "--sandbox", "workspace-write"},
		"prompt_flag":       "",
		"prompt_positional": true,
		"mcp_format":        "codex",
	}))

	// Cursor agent CLI — requires the `agent` binary from https://cursor.com/install.
	// Runs headlessly with stream-json output; --force auto-approves file changes.
	// --force moved from args to permission_bypass_args so it can be turned off
	// with permission_mode: default, like every other CLI provider. The emitted
	// argv is unchanged by default.
	runner.Register("cursor-cli", cliFactory("agent", map[string]any{
		"args":                   []any{"-p", "--output-format", "stream-json"},
		"prompt_flag":            "",
		"prompt_positional":      true,
		"mcp_format":             "cursor",
		"permission_mode":        "bypass",
		"permission_bypass_args": []any{"--force"},
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
