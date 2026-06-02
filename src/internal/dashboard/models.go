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
	loading        bool               // Show loading state
}

// OverviewTab shows dispatcher status and summary metrics.
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
}

// TaskItem is one task row (its latest execution attempt).
type TaskItem struct {
	TaskID      string
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

// AgentsTab shows agent status and performance.
type AgentsTab struct {
	Agents      []AgentStatus
	SelectedIdx int
}

type AgentStatus struct {
	ID              string
	Status          string // active, idle, error
	CurrentTask     string
	QueuedCount     int
	CompletedCount  int
	AvgDurationMs   int64
	SuccessRate     float64
	LastTaskEndedAt *time.Time
}

// LogsTab shows service logs with filtering.
type LogsTab struct {
	Logs        []LogEntry
	FilterLevel string // All, INFO, WARN, ERROR
	SearchText  string
	SelectedIdx int
	Scrolled    int
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
