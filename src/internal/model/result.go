package model

import "time"

type RunRequest struct {
	Cell          Cell
	WorkerID      string
	Model         string
	MaxTurns      int
	SystemAppend  string
	WorkingDir    string
	Env           map[string]string
	Timeout       time.Duration
	AgentMetadata map[string]any // optional: for future use with agent-specific features

	// LogSink, if set, receives log entries in real time as the runner
	// produces them (the prompt sent to the agent, each line of the agent's
	// output, stderr, etc.). It must be safe to call from multiple goroutines.
	// The dispatcher wires this to the per-task DEBUG log so users can watch
	// the live conversation in the dashboard.
	LogSink func(LogEntry) `json:"-"`
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
	RunStatusPending RunStatus = "pending"
	RunStatusRunning RunStatus = "running"
	RunStatusDone    RunStatus = "done"
	RunStatusFailed  RunStatus = "failed"
	RunStatusSkipped RunStatus = "skipped"
)

type ActiveRun struct {
	ID        string
	Cell      Cell
	WorkerID  string
	Model     string
	Status    RunStatus
	StartedAt time.Time
}
