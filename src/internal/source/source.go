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
	Poll(ctx context.Context, since time.Time) ([]model.SourceItem, error)

	// Acknowledge is called after a SourceItem has been dispatched.
	Acknowledge(ctx context.Context, cell model.SourceItem, action model.AckAction) error

	// WriteResult posts the run output back to the source task.
	WriteResult(ctx context.Context, cell model.SourceItem, result model.RunResult) error

	// WebhookHandler returns an http.Handler for push-mode sources.
	// Returns nil for poll-only adapters.
	WebhookHandler() http.Handler
}

// StateSetter is an optional interface that sources may implement to allow
// the dispatcher to transition a task to a named state (e.g. on_complete).
type StateSetter interface {
	SetState(ctx context.Context, cell model.SourceItem, stateName string) error
}

// LabelAdder is an optional interface that sources may implement to add labels
// to a task. The dispatcher uses it for on_complete.add_labels and for the
// classifier handoff (e.g. a classifier agent assigns "agent:<chosen>").
type LabelAdder interface {
	AddLabels(ctx context.Context, cell model.SourceItem, labels []string) error
}

// LabelRemover is an optional interface that sources may implement to remove
// labels from a task. The dispatcher uses it on force-restart to strip a cell's
// control labels — a stale lock (e.g. "in-progress") and the stage marker
// (e.g. "agent:engineer") — so the task re-enters routing from the start.
type LabelRemover interface {
	RemoveLabels(ctx context.Context, cell model.SourceItem, labels []string) error
}

// TaskPoller is an optional interface that sources may implement to fetch the
// current state of a single task by ID, including its comments. The workflow
// engine uses it to evaluate approval-step resume/abort conditions against the
// live task. Sources that do not implement it cannot host approval steps.
type TaskPoller interface {
	PollTask(ctx context.Context, cellID string) (model.SourceItem, error)
}

// CIStatus represents the result of a CI status check. Used by poll steps waiting
// for CI to complete.
type CIStatus struct {
	Status string // "passed", "failed", "pending"
	URL    string // Link to the CI run
	Checks []struct {
		Name   string // Check name (e.g., "test", "lint")
		Status string // "passed", "failed", "pending", "skipped"
	}
}

// CIStatusPoller is an optional interface that sources may implement to check the
// current CI status of a PR or branch. The workflow engine uses it for poll steps
// that wait for CI to complete. Sources that do not implement it cannot host poll
// steps with kind: "ci".
type CIStatusPoller interface {
	PollCIStatus(ctx context.Context, cellID string) (CIStatus, error)
}

// PullRequestRef is one pull request linked to a source item (e.g. a PR that
// cross-references a GitHub issue). State is best-effort and may be empty when
// the source does not fetch it.
type PullRequestRef struct {
	Number int    // PR number
	URL    string // browser deep-link (html_url)
	State  string // "open", "closed", "merged", or "" when unknown
}

// PullRequestLister is an optional interface a source may implement to enumerate
// every pull request linked to one of its items, oldest first. The dashboard
// uses it to offer an "open the latest PR" shortcut. Sources that do not
// implement it simply have no PRs to show.
type PullRequestLister interface {
	ListPullRequests(ctx context.Context, cellID string) ([]PullRequestRef, error)
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
