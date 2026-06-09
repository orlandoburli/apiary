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
	// CacheCreationTokens and CacheReadTokens break down the cache portion of the
	// input. InputTokens already includes them (it is the full billed input), so
	// pure uncached input = InputTokens - CacheCreationTokens - CacheReadTokens.
	// Reported by the Claude and Cursor CLIs; zero for runners that don't surface
	// cache usage.
	CacheCreationTokens int
	CacheReadTokens     int
	NumTurns            int
	NumToolCalls        int
	CostUSD             float64
}

type RunResult struct {
	WorkerID string
	Success  bool
	Output   string
	Logs     []LogEntry
	Duration time.Duration
	Error    error
	Usage    *Usage // populated by runners that support usage reporting

	// InputPrompt is the full composed prompt the runner sent to the agent (system
	// prepend + cell details + system append, exactly as buildPrompt assembled it).
	// Persisted per execution for cost auditing and replay. Empty for runners that
	// do not report it.
	InputPrompt string

	// StructuredOutput is the parsed JSON object from the APIARY_OUTPUT: sentinel
	// line, when present. Nil for runs that emit no structured output. The
	// sentinel line is stripped from Output.
	StructuredOutput map[string]any
	// Summary is the text extracted from the APIARY_SUMMARY_START/END markers,
	// when present. Empty otherwise. The markers are stripped from Output.
	Summary string
	// PublishPayload is the text extracted from the APIARY_PUBLISH_BEGIN/END
	// block, when present. Empty otherwise. The markers are stripped from
	// Output. The workflow engine writes this back to the task's source
	// bindings as a comment (the agent-driven replacement for result_comment).
	PublishPayload string
	// SpawnRequest is the parsed APIARY_SPAWN_BEGIN/END block when it carries a
	// single JSON object. Nil otherwise. The markers are stripped from Output. The
	// workflow engine creates a child InternalTask and dispatches the named
	// workflow against it.
	SpawnRequest *SpawnRequest
	// SpawnRequests is the parsed APIARY_SPAWN block when it carries a JSON array
	// of requests — one agent step fanning out into several children (e.g. a spec
	// decomposed into sub-issues). Empty for a single-object or absent block. The
	// engine treats SpawnRequest and SpawnRequests uniformly: each request becomes
	// one deduped child.
	SpawnRequests []SpawnRequest
	// SpawnError is set when an APIARY_SPAWN block was present but its body was
	// not valid JSON. The workflow executor turns this into a failed step.
	SpawnError error

	// RateLimited is true when the provider rejected the run because of a
	// usage/rate limit (e.g. Claude's 5-hour session limit). Such a run does no
	// real work — it may even exit 0 with a "you've hit your session limit"
	// message, so Success alone cannot be trusted. The dispatcher uses this to
	// back off until RateLimitResetsAt instead of counting the run as a genuine
	// success or failure.
	RateLimited bool
	// RateLimitResetsAt is when the provider's limit resets, when the provider
	// reported it (zero otherwise). Only meaningful when RateLimited is true.
	RateLimitResetsAt time.Time
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
