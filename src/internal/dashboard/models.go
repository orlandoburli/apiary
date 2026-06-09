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
	ID         string
	Running    int
	MaxWorkers int
}

type OverviewTab struct {
	Status            string
	Uptime            string
	Concurrency       int
	ActiveAgents      int
	ActiveRuns        int
	QueuedTasks       int
	CompletedToday    int
	FailedToday       int
	ThroughputRatio   string
	AvgDuration       string
	SuccessRate       string
	AgentBreakdown    []AgentCount
	TodayCostUSD      float64
	TodayTokens       int
	TodayInputTokens  int
	TodayOutputTokens int
}

// TaskView is which sub-screen the Tasks tab is showing.
type TaskView int

const (
	TaskViewList TaskView = iota
	TaskViewDetail
	TaskViewLogs
	TaskViewWorkflow // live workflow instance monitor
)

// TasksTab shows task history with detail and log sub-views.
type TasksTab struct {
	History     []TaskItem // running + past tasks, newest first
	SelectedIdx int

	View            TaskView
	Detail          *TaskItem                // populated when View == TaskViewDetail
	DetailInstance  *WorkflowInstanceItem    // workflow instance for Detail, if any
	Logs            []LogEntry               // legacy flat stream (TaskViewLogs, legacy rows)
	InstanceHistory []TaskHistorySegmentItem // per-instance history (TaskViewLogs, InternalTask rows)
	LogScroll       int

	// Workflow monitor sub-view (View == TaskViewWorkflow).
	WorkflowInstances   []*WorkflowInstanceItem // all instances for the task, newest-first
	WorkflowInstanceIdx int                     // index into WorkflowInstances of the one being shown
	WorkflowInstance    *WorkflowInstanceItem   // instance being monitored (== WorkflowInstances[WorkflowInstanceIdx])
	WorkflowStepIdx     int                     // selected step index in monitor
	WorkflowLogs        []LogEntry              // logs for the selected step
	WorkflowLogScroll   int
	WorkflowShowLogs    bool // true when the log panel is expanded

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
	ID               string
	Workflow         string
	State            string // pending, running, approval_waiting, interrupted, done, failed
	Message          string // approval message when State == approval_waiting
	CellID           string // the task/cell this instance is bound to
	ParentInstanceID string // set for sub-workflow instances
	ResumedFrom      string // set when this instance resumed a prior one
	CreatedAt        time.Time
	Steps            []WorkflowStepItem
	CIPolls          []CIPollItem // recorded wait_for CI poll history (oldest first)

	// Span and token totals aggregated across this instance's steps (StartedAt =
	// earliest step start, FinishedAt = latest step finish). Used for the per-
	// workflow timing/usage line and to roll up the whole-task header, so the
	// detail no longer reflects only the last execution row.
	StartedAt    *time.Time
	FinishedAt   *time.Time
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
}

// CIPollItem is one recorded poll of a wait_for step's CI status, shown in the
// Task Detail / monitor so a parked CI wait reports how many times it polled,
// when, and what each poll returned.
type CIPollItem struct {
	StepID    string
	Status    string // passed|failed|pending|timeout|error|unknown
	PRURL     string
	Detail    string // JSON of per-check states, or an error message
	CheckedAt time.Time
}

// TaskHistorySegmentItem is one workflow instance's slice of a task's history in
// the Tasks tab: the instance (with its steps) plus the log lines scoped to that
// instance's time window. Rendered as a labeled section in the repurposed logs
// view so a multi-workflow task (e.g. investigator → implementation) reads
// top-to-bottom as a chronological story.
type TaskHistorySegmentItem struct {
	Instance WorkflowInstanceItem
	Logs     []LogEntry
}

// SourceBindingItem is a task's link to one source item (e.g. a GitHub issue or
// a Plane work item), shown in the Task Detail. A task may have several bindings;
// spawned tasks have none.
type SourceBindingItem struct {
	SourceID   string // "github", "plane"
	ItemNumber string // human ref, e.g. "#42", "ERP-42"
	ItemURL    string // deep link to the item in its source UI
	ItemID     string // source-native id
}

// TaskLineageItem is one node in a task's lineage: an ancestor on the breadcrumb
// to root, or a direct child (spawned task). It carries enough to render a tree
// node (title, state badge, whether it has a source binding, instance count).
type TaskLineageItem struct {
	TaskID        string
	Title         string
	State         string // InternalTask state: registered|running|approval_waiting|done|failed
	HasBinding    bool
	InstanceCount int
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

// TaskItem is one row in the Tasks tab. Since Phase 9 the list is keyed on the
// canonical InternalTask (InternalTaskID), with the legacy execution fields
// (Agent/Model/tokens/duration) hydrated on drill-down via DrillKey, which keys
// the legacy task_executions/task_logs/workflow_instances-by-cell machinery.
// It is also reused by the Agents tab, where it is built from a TaskHistoryItem
// (taskItemFromHistory) and the InternalTask fields are left zero.
type TaskItem struct {
	TaskID       string
	Number       string // human reference, e.g. "ERP-42"
	URL          string // link to the task in its source UI
	Title        string
	Agent        string
	Model        string
	Runner       string
	Status       string // execution status (running/success/failed) or InternalTask state
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

	// Internal task model (Phase 9). Populated when the row/detail comes from an
	// InternalTask; zero-valued for legacy/Agents-tab rows.
	InternalTaskID       string                 // canonical internal_tasks.id (primary identity)
	DrillKey             string                 // legacy cell_id for executions/logs/monitor
	ParentTaskID         string                 // empty for root tasks
	ParentTitle          string                 // resolved parent title, for the breadcrumb
	OutstandingWorkflows int                    // workflows still running for this task
	Bindings             []SourceBindingItem    // source items bound to this task
	Lineage              []TaskLineageItem      // ancestors root-first incl. self (detail only)
	Children             []TaskLineageItem      // direct children / spawned tasks (detail only)
	Instances            []WorkflowInstanceItem // all workflow instances for this task (detail only)
}

// AgentView is which sub-screen the Agents tab is showing.
type AgentView int

const (
	AgentViewList AgentView = iota
	AgentViewDetail
	AgentViewActivity
	AgentViewTaskLogs
	AgentViewFiles       // list of files related to the agent (soul + skills)
	AgentViewFileContent // viewer for one selected related file
)

// AgentFileItem is one file related to an agent — its soul prompt or one of its
// skills — shown in the agent's Files sub-view. Path is resolved relative to the
// working directory; Missing marks a configured file that could not be found.
type AgentFileItem struct {
	Kind    string // "soul" or "skill"
	Name    string // display name (skill id, or the soul file's base name)
	Path    string // resolved filesystem path
	Missing bool   // true when the file does not exist / is unreadable
}

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
	LogsTask   *TaskItem // task detail for the logs header
	TaskLogs   []LogEntry
	TaskLogIdx int // vertical scroll within TaskLogs (visual lines)

	// Related files (soul + skills) for the agent in Detail.
	Files       []AgentFileItem // populated when View == AgentViewFiles
	FilesIdx    int             // cursor within Files
	FileName    string          // display name of the file open in AgentViewFileContent
	FilePath    string          // resolved path of the open file
	FileContent string          // raw contents of the open file ("" until loaded)
	FileErr     string          // read error message, if any
	FileScroll  int             // vertical scroll within the open file (visual lines)
	FileRaw     bool            // show raw text instead of rendered markdown (toggle)

	// Memoized display lines for the open file, so glamour rendering happens once
	// per (width, mode) rather than on every keystroke. Invalidated by comparing
	// fileLinesWidth/fileLinesRaw against the current width and FileRaw.
	fileLines      []string
	fileLinesWidth int
	fileLinesRaw   bool
	fileLinesValid bool
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
	MaxWorkers   int
	RunnerType   string
	Model        string
	SoulFile     string
	Skills       []string // skill ids declared on the agent config
	Description  string
	SourceName   string   // git author name from agent config
	SourceEmail  string   // git author email from agent config
	Runners      []string // all available runner IDs for cycling
	RunnerModels []string // models declared on the current runner config
	TotalCostUSD float64
	TotalTokens  int
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
	Workflows   []WorkflowConfigItem
	SelectedIdx int // selected workflow in the left panel
	StepIdx     int // selected step in the right panel
	StepScroll  int // scroll offset in the step list
	Focus       WorkflowsView
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
		usageTab: &UsageTab{},
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
