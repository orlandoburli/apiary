package runner

import (
	"context"

	"github.com/orlandoburli/apiary/internal/model"
)

// Adapter executes an agent runner for a given Cell.
type Adapter interface {
	// ID returns the adapter type key (e.g. "cli", "script").
	ID() string

	// Configure sets runner-level options from the worker config block.
	Configure(config map[string]any) error

	// Run executes the agent and streams progress. Blocks until completion.
	Run(ctx context.Context, req model.RunRequest) (model.RunResult, error)
}

// Factory creates a new, unconfigured Adapter instance.
type Factory func() Adapter

var factories = map[string]Factory{}

// Register stores a factory for the given adapter type key.
func Register(id string, f Factory) {
	factories[id] = f
}

// New returns a fresh, unconfigured Adapter instance for the given type key.
func New(id string) (Adapter, bool) {
	f, ok := factories[id]
	if !ok {
		return nil, false
	}
	return f(), true
}

// Types returns all registered adapter type keys.
func Types() []string {
	keys := make([]string, 0, len(factories))
	for k := range factories {
		keys = append(keys, k)
	}
	return keys
}
