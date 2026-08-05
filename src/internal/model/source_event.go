package model

import "time"

// PR event kinds emitted by a source adapter's PollPREvents. They mirror the
// trigger `on:` vocabulary in config (config.TriggerOn*).
const (
	EventPRComment              = "pr_comment"
	EventPRReviewApproved       = "pr_review_approved"
	EventPRReviewChangesRequest = "pr_review_changes_requested"
)

// SourceEvent is a normalized, source-agnostic pull-request event (a comment or
// a review submission) returned by a source adapter's PollPREvents. Unlike a
// SourceItem it is not a unit of work by itself: the dispatcher routes it
// through event triggers (`trigger.on:`) and binds the resulting workflow
// instance to the InternalTask of the originating issue when one exists.
type SourceEvent struct {
	// ID is a stable, source-native identifier for the event (e.g. a comment id
	// or review id, prefixed by kind). It is the dedup key: an event ID must
	// dispatch a given workflow at most once, across daemon restarts.
	ID       string
	SourceID string
	// Kind is one of the EventPR* constants.
	Kind     string
	PRNumber int
	PRURL    string
	// Author is the source-native login of the user who wrote the comment or
	// submitted the review.
	Author string
	// AuthorAssociation is the author's relationship to the repository as
	// reported by the source (e.g. GitHub's OWNER / MEMBER / COLLABORATOR /
	// CONTRIBUTOR / NONE). Used for default actor gating.
	AuthorAssociation string
	// Body is the comment body or the review's summary text.
	Body        string
	SubmittedAt time.Time
	// RelatedItemID is the source-native id of the originating work item (e.g.
	// the issue a PR closes), resolved best-effort by the adapter. Empty when the
	// PR has no discoverable parent item.
	RelatedItemID string
}
