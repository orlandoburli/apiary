package claude

import (
	"context"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/runner/cli"
)

func init() {
	runner.Register("claude", func() runner.Runner { return &Runner{} })
}

type Runner struct {
	proc *cli.ProcessRunner
}

func (r *Runner) ID() string { return "claude" }

func (r *Runner) Configure(config map[string]any) error {
	r.proc = &cli.ProcessRunner{}
	return r.proc.Configure(defaults(config))
}

func (r *Runner) Run(ctx context.Context, req model.RunRequest) (model.RunResult, error) {
	return r.proc.Run(ctx, req)
}

func defaults(config map[string]any) map[string]any {
	m := map[string]any{
		"command":     "claude",
		"model_flag":  "--model",
		"prompt_flag": "-p",
	}
	for k, v := range config {
		m[k] = v
	}
	return m
}
