package runner

import (
	"context"

	"github.com/orlandoburli/apiary/internal/model"
)

// Runner executes an agent runner for a given SourceItem.
type Runner interface {
	// ID returns the runner type key (e.g. "claude", "opencode").
	ID() string

	// Configure sets runner-level options from the worker config block.
	Configure(config map[string]any) error

	// Run executes the agent and streams progress. Blocks until completion.
	Run(ctx context.Context, req model.RunRequest) (model.RunResult, error)
}

// Factory creates a new, unconfigured Runner instance.
type Factory func() Runner

var factories = map[string]Factory{}

// Register stores a factory for the given runner type key.
func Register(id string, f Factory) {
	factories[id] = f
}

// New returns a fresh, unconfigured Runner instance for the given type key.
func New(id string) (Runner, bool) {
	f, ok := factories[id]
	if !ok {
		return nil, false
	}
	return f(), true
}

