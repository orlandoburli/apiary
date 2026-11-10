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
// instance to the InternalTask of the ticket the PR is about. An event with no
// resolvable ticket is dropped rather than turned into a synthetic PR task.
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
	// PRAuthor is the source-native login of the user who opened the pull
	// request, as opposed to Author, who wrote this comment or review. Empty when
	// the adapter could not resolve the PR; a trigger's pr_authors filter treats
	// that as no match.
	PRAuthor string
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
	// PRTitle and PRBranch carry the pull request's metadata so the dispatcher
	// can resolve the ticket the PR is about (a Jira/Plane key in the title or
	// head branch is a stronger signal than a mention in a comment).
	PRTitle  string
	PRBranch string
	// PRBody is the pull request's description. The adapter already scans it for
	// a closing issue reference (RelatedItemID); it is surfaced here so the
	// dispatcher can resolve a ticket key found there too.
	PRBody string
	// TicketRefs are the human ticket references found in the PR title, head
	// branch, and body, in that order (see internal/ticket.Refs). A PR event
	// whose ticket resolves to no bound task is dropped rather than
	// materialising a synthetic "PR #n" task: a PR is an event receipt, not a
	// unit of work. Empty when no ticket key was found.
	TicketRefs []string
}
