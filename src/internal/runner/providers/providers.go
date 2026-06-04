// Package providers registers concrete runner types by pre-configuring generic
// execution engines (cli, api) with provider-specific defaults.
package providers

import (
	"github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/runner/execution"
)

func init() {
	runner.Register("claude", func() runner.Runner {
		r := &execution.CliRunner{}
		_ = r.Configure(map[string]any{
			"command":     "claude",
			"model_flag":  "--model",
			"prompt_flag": "-p",
		})
		return r
	})

	runner.Register("opencode", func() runner.Runner {
		r := &execution.CliRunner{}
		_ = r.Configure(map[string]any{
			"command":     "opencode",
			"model_flag":  "--model",
			"prompt_flag": "--prompt",
			"turns_flag":  "--max-turns",
		})
		return r
	})

	runner.Register("opencode-api", func() runner.Runner {
		// Defaults for opencode API — provider config in YAML overrides via Configure().
		return &execution.ApiRunner{
			Endpoint:   "https://api.opencode.ai/v1/chat/completions",
			AuthHeader: "Bearer ${OPENCODE_API_KEY}", // expanded at runtime
		}
	})
}
