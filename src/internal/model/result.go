package model

import "time"

type RunRequest struct {
	Cell          SourceItem
	WorkerID      string
	Model         string
	MaxTurns      int
	SystemAppend  string
	WorkingDir    string
	Env           map[string]string
	Timeout       time.Duration
	AgentMetadata map[string]any

	// SystemPrepend is injected at the very start of the prompt, before the cell
	// details and SystemAppend. In workflow mode it carries the formatted
	// workflow memory document. Empty for plain routes.
	SystemPrepend string
	// SummaryPrompt, when set, instructs the agent to emit a short handoff
	// summary delimited by APIARY_SUMMARY_START/END markers, extracted into
	// RunResult.Summary. Empty for plain routes.
	SummaryPrompt string
	// StepID is the workflow step this run belongs to (empty for plain routes).
	StepID string
	// WorkflowInstanceID identifies the owning workflow instance, for
	// logging/tracing (empty for plain routes).
	WorkflowInstanceID string

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

type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	NumTurns     int
	NumToolCalls int
	CostUSD      float64
}

type RunResult struct {
	WorkerID string
	Success  bool
	Output   string
	Logs     []LogEntry
	Duration time.Duration
	Error    error
	Usage    *Usage // populated by runners that support usage reporting

	// StructuredOutput is the parsed JSON object from the APIARY_OUTPUT: sentinel
	// line, when present. Nil for runs that emit no structured output. The
	// sentinel line is stripped from Output.
	StructuredOutput map[string]any
	// Summary is the text extracted from the APIARY_SUMMARY_START/END markers,
	// when present. Empty otherwise. The markers are stripped from Output.
	Summary string
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
	Cell      SourceItem
	WorkerID  string
	Model     string
	Status    RunStatus
	StartedAt time.Time
}
