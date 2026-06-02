package model

import "time"

// Cell is a normalised, source-system-agnostic task unit.
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
}

type AckAction string

const (
	AckActionInProgress AckAction = "in_progress"
	AckActionSkip       AckAction = "skip"
)
