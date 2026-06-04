package source

import (
	"context"
	"net/http"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
)

// SourceTokenCtxKey is the context key for per-agent source token overrides.
// When set, the adapter uses this token instead of the source config's api_key
// for write operations (Acknowledge, WriteResult, SetState, AddLabels).
type sourceTokenCtxKey struct{}

var SourceTokenCtxKey sourceTokenCtxKey

// Adapter connects Apiary to a task management system.
type Adapter interface {
	// ID returns the adapter type key (e.g. "plane", "jira").
	ID() string

	// Connect initializes the connection using the raw config map from apiary.yaml.
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

// StateSetter is an optional interface that sources may implement to allow
// the dispatcher to transition a task to a named state (e.g. on_complete).
type StateSetter interface {
	SetState(ctx context.Context, cell model.Cell, stateName string) error
}

// LabelAdder is an optional interface that sources may implement to add labels
// to a task. The dispatcher uses it for on_complete.add_labels and for the
// classifier handoff (e.g. a classifier agent assigns "agent:<chosen>").
type LabelAdder interface {
	AddLabels(ctx context.Context, cell model.Cell, labels []string) error
}

// PRReviewEvent is the type of review to submit on a pull request.
type PRReviewEvent string

const (
	ReviewApprove        PRReviewEvent = "approve"
	ReviewRequestChanges PRReviewEvent = "request_changes"
	ReviewComment        PRReviewEvent = "comment"
)

// PRReviewInput describes a pull request review to submit.
type PRReviewInput struct {
	PRNumber string
	Event    PRReviewEvent
	Body     string
}

// PRReviewer is an optional interface that source adapters may implement to
// submit pull request reviews (approve, request changes, comment).
type PRReviewer interface {
	SubmitReview(ctx context.Context, cell model.Cell, review PRReviewInput) error
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
