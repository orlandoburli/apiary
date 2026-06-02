package runner

import (
	"context"

	"github.com/orlandoburli/apiary/internal/model"
)

// Adapter executes an agent runner for a given Cell.
type Adapter interface {
	// ID returns the adapter type key (e.g. "opencode", "script").
	ID() string

	// Configure sets runner-level options from the worker config block.
	Configure(config map[string]any) error

	// Run executes the agent and streams progress. Blocks until completion.
	Run(ctx context.Context, req model.RunRequest) (model.RunResult, error)
}

var registry = map[string]Adapter{}

// Register adds a runner adapter to the global registry.
func Register(a Adapter) {
	registry[a.ID()] = a
}

// Get returns a registered adapter by type key.
func Get(id string) (Adapter, bool) {
	a, ok := registry[id]
	return a, ok
}

// All returns all registered adapter type keys.
func All() []string {
	keys := make([]string, 0, len(registry))
	for k := range registry {
		keys = append(keys, k)
	}
	return keys
}
