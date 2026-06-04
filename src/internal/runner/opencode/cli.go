package opencode

import (
	"github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/runner/cli"
)

func init() {
	// CLI mode: pre-configured ProcessRunner (same pattern as claude)
	runner.Register("opencode", func() runner.Runner {
		r := &cli.ProcessRunner{}
		_ = r.Configure(map[string]any{
			"command":     "opencode",
			"model_flag":  "--model",
			"prompt_flag": "--prompt",
			"turns_flag":  "--max-turns",
		})
		return r
	})
}
