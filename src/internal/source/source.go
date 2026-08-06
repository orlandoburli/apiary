package source

import (
	"context"
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
	Status string // "passed", "failed", "pending", "conflict"
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

// BlockerRef is one upstream blocker of a source item — another work item that
// must land before the blocked one may proceed (e.g. the other side of a Jira
// "is blocked by" link). State and Merged carry the blocker's resolution
// signals; the workflow engine decides satisfaction against the wait_for step's
// satisfied_when conditions.
type BlockerRef struct {
	ID     string // source-native id of the blocking item
	Number string // human-facing reference (e.g. "PSP-49", "#42")
	Title  string // blocker summary, for poll-history/debug detail
	State  string // normalized: "done" when resolved/closed, otherwise the source status
	Merged bool   // true when a pull request linked to the blocker is merged
}

// BlockerLister is an optional interface a source may implement to enumerate a
// task's upstream blockers. The workflow engine uses it for wait_for steps with
// kind "dependency", which park the instance until every blocker is satisfied.
// linkType selects the source-native blocking relation (e.g. Jira's "Blocks"
// link type); empty means the source's default. Sources that do not implement
// it cannot host dependency waits (rejected at config validation).
type BlockerLister interface {
	ListBlockers(ctx context.Context, cellID, linkType string) ([]BlockerRef, error)
}

// PREventPoller is an optional interface a source may implement to enumerate
// pull-request events (comments and review submissions) since a watermark. The
// dispatcher polls it alongside Poll and routes the events through workflow
// triggers with an `on:` event kind (pr_comment, pr_review_approved,
// pr_review_changes_requested). Sources that do not implement it cannot host
// event triggers (rejected at config validation).
//
// Adapters must exclude events authored by their own token identity (and
// bot-typed users), so an agent commenting through the daemon's account can
// never re-trigger a workflow — the first line of loop prevention.
type PREventPoller interface {
	PollPREvents(ctx context.Context, since time.Time) ([]model.SourceEvent, error)
}

// SubIssueCreator is an optional interface a source may implement to create a
// child work item linked to a parent (a sub-issue). The workflow engine uses it
// to materialize a spawned InternalTask as a source sub-issue under the spawning
// task's item (see step.Materialize / APIARY_SPAWN). The child carries the spawn
// request's title, body, and labels so the normal poll→route loop dispatches the
// matching workflow once it sees the new item.
//
// CreateSubIssue returns the created item populated with its source-native ID,
// human number, and URL — enough for the engine to persist the child's
// SourceBinding. The parent-child link itself is best-effort: an adapter that
// creates the item but cannot link it should still return the created item (and
// log the link failure) rather than erroring, so the caller persists the binding
// and never re-creates a duplicate on the next run.
type SubIssueCreator interface {
	CreateSubIssue(ctx context.Context, parent, child model.SourceItem) (model.SourceItem, error)
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
