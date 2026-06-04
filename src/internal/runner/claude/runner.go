package claude

import (
	"context"
	"fmt"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/runner"
)

func init() {
	runner.Register("claude", func() runner.Runner { return &Runner{} })
}

type Runner struct {
	inner runner.Runner
}

func (r *Runner) ID() string { return "claude" }

func (r *Runner) Configure(config map[string]any) error {
	var ok bool
	r.inner, ok = runner.New("cli")
	if !ok {
		return fmt.Errorf("claude: cli runner not available")
	}

	cliCfg := map[string]any{
		"command":     "claude",
		"model_flag":  "--model",
		"prompt_flag": "-p",
	}
	if raw, has := config["args"]; has {
		cliCfg["args"] = raw
	}
	return r.inner.Configure(cliCfg)
}

func (r *Runner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	return r.inner.Run(ctx, req)
}
