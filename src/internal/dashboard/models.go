package dashboard

import (
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// Model holds the state for the dashboard TUI.
type Model struct {
	activeTab      int
	tabs           []string
	overviewTab    *OverviewTab
	tasksTab       *TasksTab
	agentsTab      *AgentsTab
	usageTab       *UsageTab
	logsTab        *LogsTab
	workflowsTab   *WorkflowsTab
	width          int
	height         int
	lastRefresh    time.Time
	lastTabRefresh map[int]time.Time // Track refresh per tab
	loading        bool              // Show loading state

	confirmAction string // "restart" or "clear" or "stop" when awaiting confirmation
	confirmTaskID string
}

// OverviewTab shows dispatcher status and summary metrics.
type AgentCount struct {
	ID          string
	Running     int
	MaxWorkers  int
}

type OverviewTab struct {
	Status           string
	Uptime           string
	Concurrency      int
	ActiveAgents     int
	ActiveRuns       int
	QueuedTasks      int
	CompletedToday   int
	FailedToday      int
	ThroughputRatio  string
	AvgDuration      string
	SuccessRate      string
	AgentBreakdown   []AgentCount
	TodayCostUSD     float64
	TodayTokens      int
	TodayInputTokens int
	TodayOutputTokens int
}

// TaskView is which sub-screen the Tasks tab is showing.
type TaskView int

const (
	TaskViewList     TaskView = iota
	TaskViewDetail
	TaskViewLogs
	TaskViewWorkflow // live workflow instance monitor
)

// TasksTab shows task history with detail and log sub-views.
type TasksTab struct {
	History     []TaskItem // running + past tasks, newest first
	SelectedIdx int

	View           TaskView
	Detail         *TaskItem             // populated when View == TaskViewDetail
	DetailInstance *WorkflowInstanceItem // workflow instance for Detail, if any
	Logs           []LogEntry            // populated when View == TaskViewLogs
	LogScroll      int

	// Workflow monitor sub-view (View == TaskViewWorkflow).
	WorkflowInstance *WorkflowInstanceItem // instance being monitored
	WorkflowStepIdx  int                   // selected step index in monitor
	WorkflowLogs     []LogEntry            // logs for the selected step
	WorkflowLogScroll int
	WorkflowShowLogs bool // true when the log panel is expanded

	// Scroll / filter / sort
	ScrollOffset int    // first visible row index
	FilterText   string // current filter query
	FilterActive bool   // true while typing a filter
	SortField    string // "time" | "status" | "agent"  (default "time")
	SortAsc      bool
}

// WorkflowInstanceItem is a workflow instance bound to a task, with its steps,
// shown in the Task Detail and workflow monitor views.
type WorkflowInstanceItem struct {
	ID       string
	Workflow string
	State    string // pending, running, approval_waiting, interrupted, done, failed
	Message  string // approval message when State == approval_waiting
	CellID   string // the task/cell this instance is bound to
	Steps    []WorkflowStepItem
}

// WorkflowStepItem is one step row within a WorkflowInstanceItem.
type WorkflowStepItem struct {
	StepID       string
	Agent        string
	State        string // pending, running, passed, failed, skipped, skipped_cached
	Duration     string
	Cached       bool
	Output       string
	Summary      string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
	NumTurns     int
	NumToolCalls int
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// TaskItem is one task row (its latest execution attempt).
type TaskItem struct {
	TaskID       string
	Number       string // human reference, e.g. "ERP-42"
	URL          string // link to the task in its source UI
	Title        string
	Agent        string
	Model        string
	Runner       string
	Status       string // running, success, failed
	Attempt      int
	Duration     time.Duration
	StartedAt    *time.Time
	CompletedAt  *time.Time
	Error        string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	NumTurns     int
	NumToolCalls int
	CostUSD      float64
}

// AgentView is which sub-screen the Agents tab is showing.
type AgentView int

const (
	AgentViewList AgentView = iota
	AgentViewDetail
	AgentViewActivity
	AgentViewTaskLogs
)

// AgentsTab shows agent status and performance with detail/activity sub-views.
type AgentsTab struct {
	Agents      []AgentStatus
	SelectedIdx int

	View        AgentView
	Detail      *AgentStatus // populated when View == AgentViewDetail
	Activity    []TaskItem   // populated when View == AgentViewActivity
	ActivityIdx int          // cursor within Activity

	// Drill-down: logs of the task selected in the activity list.
	LogsTaskID string
	LogsTask   *TaskItem   // task detail for the logs header
	TaskLogs   []LogEntry
	TaskLogIdx int // vertical scroll within TaskLogs (visual lines)
}

type AgentStatus struct {
	ID              string
	Status          string // active, stale, zombie, idle
	RunningCount    int
	CurrentTask     string
	QueuedCount     int
	CompletedCount  int
	AvgDurationMs   int64
	SuccessRate     float64
	LastTaskEndedAt *time.Time
	PID             int
	HeartbeatAt     *time.Time
	HeartbeatCount  int

	// Config fields (enriched from apiary.yaml)
	MaxWorkers     int
	RunnerType     string
	Model          string
	SoulFile       string
	Description    string
	SourceName     string // git author name from agent config
	SourceEmail    string // git author email from agent config
	Runners        []string // all available runner IDs for cycling
	RunnerModels   []string // models declared on the current runner config
	TotalCostUSD   float64
	TotalTokens    int
}

// UsageTab shows token/cost charts over time and per agent.
type UsageTab struct {
	Daily  []db.DailyUsage
	Agents []db.AgentUsage
}

// LogsTab shows service logs with filtering.
type LogsTab struct {
	Logs        []LogEntry
	FilterLevel string // All, INFO, WARN, ERROR
	SearchText  string
	SelectedIdx int
	Scrolled    int  // vertical scroll offset (in display lines)
	Wrap        bool // break long messages onto multiple lines
	HScroll     int  // horizontal scroll offset (columns) when not wrapping
}

type LogEntry struct {
	Timestamp time.Time
	Level     string
	Component string
	Message   string
}

// WorkflowStepDef is one step definition shown in the Workflows config tab.
type WorkflowStepDef struct {
	ID        string
	Type      string
	Agent     string
	DependsOn []string
	Condition string
	Prompt    string
}

// WorkflowConfigItem is one workflow shown in the Workflows config tab.
type WorkflowConfigItem struct {
	ID          string
	Description string
	Steps       []WorkflowStepDef
}

// WorkflowsView is which panel the Workflows tab has focus on.
type WorkflowsView int

const (
	WorkflowsViewList WorkflowsView = iota
	WorkflowsViewSteps
)

// WorkflowsTab shows the static workflow config definitions (read-only).
type WorkflowsTab struct {
	Workflows    []WorkflowConfigItem
	SelectedIdx  int // selected workflow in the left panel
	StepIdx      int // selected step in the right panel
	StepScroll   int // scroll offset in the step list
	Focus        WorkflowsView
}

// NewModel creates a new dashboard model.
func NewModel() *Model {
	return &Model{
		activeTab: 0,
		tabs:      []string{"Overview", "Tasks", "Agents", "Usage", "Logs", "Workflows"},
		overviewTab: &OverviewTab{
			Status:      "Loading...",
			Uptime:      "0s",
			Concurrency: 4,
		},
		tasksTab: &TasksTab{
			History: []TaskItem{},
			View:    TaskViewList,
		},
		agentsTab: &AgentsTab{
			Agents: []AgentStatus{},
		},
		usageTab:     &UsageTab{},
		logsTab: &LogsTab{
			Logs:        []LogEntry{},
			FilterLevel: "All",
			Wrap:        true,
		},
		workflowsTab:   &WorkflowsTab{},
		lastTabRefresh: make(map[int]time.Time),
		loading:        true,
	}
}

// ActiveTab returns the currently selected tab.
func (m *Model) ActiveTab() string {
	if m.activeTab >= 0 && m.activeTab < len(m.tabs) {
		return m.tabs[m.activeTab]
	}
	return "Overview"
}

// NextTab moves to the next tab.
func (m *Model) NextTab() {
	m.activeTab = (m.activeTab + 1) % len(m.tabs)
}

// PrevTab moves to the previous tab.
func (m *Model) PrevTab() {
	m.activeTab--
	if m.activeTab < 0 {
		m.activeTab = len(m.tabs) - 1
	}
}
