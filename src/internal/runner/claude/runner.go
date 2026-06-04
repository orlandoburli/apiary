package claude

import (
	"github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/runner/cli"
)

func init() {
	runner.Register("claude", func() runner.Runner {
		r := &cli.ProcessRunner{}
		_ = r.Configure(map[string]any{
			"command":     "claude",
			"model_flag":  "--model",
			"prompt_flag": "-p",
		})
		return r
	})
}
