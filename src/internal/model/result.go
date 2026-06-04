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
	AgentMetadata map[string]any

	// LogSink receives log entries in real time as the runner produces them.
	// Must be safe to call from multiple goroutines.
	LogSink func(LogEntry) `json:"-"`

	// SetPID is called after the child process starts with its OS PID.
	SetPID func(pid int) `json:"-"`

	// Heartbeat is called periodically (~every 15s) while the child runs.
	// The dispatcher uses it to update last_heartbeat_at in the DB so the
	// dashboard can detect stale/zombie processes.
	Heartbeat func() `json:"-"`
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
