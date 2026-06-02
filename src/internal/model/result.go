package model

import "time"

type RunRequest struct {
	Cell         Cell
	WorkerID     string
	Model        string
	MaxTurns     int
	SystemAppend string
	WorkingDir   string
	Env          map[string]string
	Timeout      time.Duration
}

type RunResult struct {
	WorkerID string
	Success  bool
	Output   string
	Logs     []LogEntry
	Duration time.Duration
	Error    error
}

type LogEntry struct {
	Level     string
	Message   string
	Timestamp time.Time
	Fields    map[string]any
}

type RunStatus string

const (
	RunStatusPending  RunStatus = "pending"
	RunStatusRunning  RunStatus = "running"
	RunStatusDone     RunStatus = "done"
	RunStatusFailed   RunStatus = "failed"
	RunStatusSkipped  RunStatus = "skipped"
)

type ActiveRun struct {
	ID       string
	Cell     Cell
	WorkerID string
	Model    string
	Status   RunStatus
	StartedAt time.Time
}
