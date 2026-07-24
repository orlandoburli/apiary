package model

import (
	"strings"
	"time"
)

// SourceItem is a normalized, source-system-agnostic view of a single item
// returned by a source adapter's Poll. It lives only within the binding layer:
// the SourceBinder translates a SourceItem into an InternalTask, after which the
// task — not the SourceItem — travels forward into routing and execution.
//
// This is the canonical name; it replaces the former Cell type.
type SourceItem struct {
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
	// AuthorLogin is the source-native username of whoever opened or triggered
	// this item. Populated by adapters that carry author identity (e.g. GitHub
	// issues carry the opener's login). Empty when the source does not expose it.
	AuthorLogin string
	// Comments is populated only by a TaskPoller (per-task fetch used for
	// approval steps), not by Poll. It holds comments relevant to evaluating an
	// approval condition.
	Comments []Comment
}

// LogLabel returns the identifier used to tag this item's log lines: the
// source-native id plus the human-facing reference when that adds information
// the id alone doesn't carry (e.g. Jira's "CDT-123" next to its numeric id).
// For sources where the reference is just the id itself (GitHub's "#42"), the
// id alone is returned.
func (s SourceItem) LogLabel() string {
	if s.Number == "" || strings.TrimPrefix(s.Number, "#") == s.ID {
		return s.ID
	}
	return s.ID + " " + s.Number
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
