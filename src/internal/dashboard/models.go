package dashboard

import (
	"time"
)

// Model holds the state for the dashboard TUI.
type Model struct {
	activeTab      int
	tabs           []string
	overviewTab    *OverviewTab
	tasksTab       *TasksTab
	agentsTab      *AgentsTab
	logsTab        *LogsTab
	width          int
	height         int
	lastRefresh    time.Time
	lastTabRefresh map[int]time.Time // Track refresh per tab
	loading        bool              // Show loading state

	confirmAction string // "restart" or "clear" when awaiting confirmation
	confirmTaskID string
}

// OverviewTab shows dispatcher status and summary metrics.
type AgentCount struct {
	ID          string
	Running     int
	MaxWorkers  int
}

type OverviewTab struct {
	Status          string
	Uptime          string
	Concurrency     int
	ActiveAgents    int
	ActiveRuns      int
	QueuedTasks     int
	CompletedToday  int
	FailedToday     int
	ThroughputRatio string
	AvgDuration     string
	SuccessRate     string
	AgentBreakdown  []AgentCount
}

// TaskView is which sub-screen the Tasks tab is showing.
type TaskView int

const (
	TaskViewList TaskView = iota
	TaskViewDetail
	TaskViewLogs
)

// TasksTab shows task history with detail and log sub-views.
type TasksTab struct {
	History     []TaskItem // running + past tasks, newest first
	SelectedIdx int

	View      TaskView
	Detail    *TaskItem  // populated when View == TaskViewDetail
	Logs      []LogEntry // populated when View == TaskViewLogs
	LogScroll int

	// Scroll / filter / sort
	ScrollOffset int    // first visible row index
	FilterText   string // current filter query
	FilterActive bool   // true while typing a filter
	SortField    string // "time" | "status" | "agent"  (default "time")
	SortAsc      bool
}

// TaskItem is one task row (its latest execution attempt).
type TaskItem struct {
	TaskID      string
	Number      string // human reference, e.g. "ERP-42"
	URL         string // link to the task in its source UI
	Title       string
	Agent       string
	Model       string
	Runner      string
	Status      string // running, success, failed
	Attempt     int
	Duration    time.Duration
	StartedAt   *time.Time
	CompletedAt *time.Time
	Error       string
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

// NewModel creates a new dashboard model.
func NewModel() *Model {
	return &Model{
		activeTab: 0,
		tabs:      []string{"Overview", "Tasks", "Agents", "Logs"},
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
		logsTab: &LogsTab{
			Logs:        []LogEntry{},
			FilterLevel: "All",
			Wrap:        true,
		},
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
