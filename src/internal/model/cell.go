package model

import "time"

// Cell is a normalized, source-system-agnostic task unit.
type Cell struct {
	ID          string
	SourceID    string
	Number      string // human-facing reference, e.g. "ERP-42" or "#42"
	Title       string
	Description string
	Labels      []string
	Type        string
	Priority    string
	State       string
	URL         string
	Metadata    map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Comments is populated only by a TaskPoller (per-task fetch used for
	// approval steps), not by Poll. It holds comments relevant to evaluating an
	// approval condition.
	Comments []Comment
}

// Comment is a single comment on a source task, used to evaluate approval-step
// resume/abort conditions (comment_contains).
type Comment struct {
	ID        string
	Body      string
	CreatedAt time.Time
}

type AckAction string

const (
	AckActionInProgress AckAction = "in_progress"
	AckActionSkip       AckAction = "skip"
)
