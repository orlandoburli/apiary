package model

import "time"

// Cell is a normalised, source-system-agnostic task unit.
type Cell struct {
	ID          string
	SourceID    string
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
