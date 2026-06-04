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

	runner.Register("claude-cli", cliFactory("claude", nil))

	runner.Register("opencode-cli", cliFactory("opencode", map[string]any{
		"prompt_flag": "--prompt",
		"turns_flag":  "--max-turns",
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
