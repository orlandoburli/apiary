package source

import (
	"context"
	"net/http"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// Adapter connects Apiary to a task management system.
type Adapter interface {
	// ID returns the adapter type key (e.g. "plane", "jira").
	ID() string

	// Connect initialises the connection using the raw config map from apiary.yaml.
	Connect(ctx context.Context, config map[string]any) error

	// Poll returns tasks matching the source filters since the given time.
	Poll(ctx context.Context, since time.Time) ([]model.Cell, error)

	// Acknowledge is called after a Cell has been dispatched.
	Acknowledge(ctx context.Context, cell model.Cell, action model.AckAction) error

	// WriteResult posts the run output back to the source task.
	WriteResult(ctx context.Context, cell model.Cell, result model.RunResult) error

	// WebhookHandler returns an http.Handler for push-mode sources.
	// Returns nil for poll-only adapters.
	WebhookHandler() http.Handler
}

var registry = map[string]Adapter{}

// Register adds a source adapter to the global registry.
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
