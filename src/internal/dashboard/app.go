package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/daemon"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// refreshInterval controls how often the active tab re-queries the database.
const refreshInterval = 2 * time.Second

// queryTimeout bounds each database query so a locked DB never blocks the UI.
const queryTimeout = 2 * time.Second

// taskLogTailLimit caps how many of a task's most recent log lines are loaded
// when the logs view opens. task_logs rows are large (~10KB of agent stream), so
// loading the full history cold is the main cause of a slow first open; a tail of
// this size keeps it fast, and older lines load on scroll-to-top (see
// fetchOlderTaskLogs).
const taskLogTailLimit = 1000

// maxGlamourBytes caps which log messages get glamour-styled markdown. Above
// this, plain wrapping is used unconditionally: giant agent dumps gain little
// from styling and are exactly the messages that cost the most to render (#175).
const maxGlamourBytes = 8 * 1024

// logPrefixWidth is the fixed "15:04:05 LEVEL " prefix column of log lines.
const logPrefixWidth = 15

// App is the main dashboard application.
//
// It follows the Elm architecture used by Bubble Tea: commands run in
// goroutines and return *messages*; only Update (which runs on the single
// event-loop goroutine) is allowed to mutate the model. This is what keeps
// the data-fetching goroutines from racing with View.
type App struct {
	model      *Model
	dbConn     *db.Client
	socketPath string
	dataDir    string
	cfg        *config.Config
	// logDir is the daemon's log directory, used to locate per-task markdown
	// transcripts (logDir/transcripts/<task>/...).
	logDir string

	// logMDCache memoizes the display lines of multi-line log messages, keyed by
	// the message text: glamour-rendered markdown delivered by the async warm-up
	// (warmMarkdownCmd), and plain wraps of messages glamour will never touch
	// (oversized or non-markdown). glamour is too slow for the render path (#175),
	// so a markdown message shows plain-wrapped until its warmed lines land in the
	// cache. logMDPending tracks messages already handed to an in-flight warm-up so
	// periodic refreshes don't re-render them. Both maps are dropped whenever the
	// render width changes (logMDWidth).
	logMDCache   map[string][]string
	logMDPending map[string]bool
	logMDWidth   int
}

func New(dbConn *db.Client, socketPath, dataDir string, cfg *config.Config, logDir string) *App {
	return &App{
		model:      NewModel(),
		dbConn:     dbConn,
		socketPath: socketPath,
		dataDir:    dataDir,
		cfg:        cfg,
		logDir:     logDir,
	}
}

// ── messages ────────────────────────────────────────────────────────────────

// tickMsg fires on the refresh timer.
type tickMsg time.Time

// spinnerTickMsg fires on the fast spinner timer to advance loading animations.
type spinnerTickMsg time.Time

// Each *DataMsg carries the result of a background query. They are produced by
// commands (goroutines) and consumed by Update (event loop), never sharing
// memory with the model directly.
type overviewDataMsg struct{ data OverviewTab }
type tasksDataMsg struct{ items []TaskItem }
type taskDetailMsg struct {
	taskID   string
	detail   *TaskItem
	instance *WorkflowInstanceItem
}

// taskPullsRefreshedMsg is emitted after the daemon has (re)discovered and
// persisted a task's pull requests, so the open detail can reload and pick them up.
type taskPullsRefreshedMsg struct {
	internalTaskID string
}
type taskLogsMsg struct {
	taskID   string
	logs     []LogEntry
	detail   *TaskItem
	oldestID int64 // row id of the oldest loaded line (older-page cursor)
	newestID int64 // row id of the newest loaded line (live-tail cursor)
	hasMore  bool  // an older page may exist
}

// olderTaskLogsMsg carries an older page of flat task logs lazily loaded when the
// logs view is scrolled to the top.
type olderTaskLogsMsg struct {
	taskID   string
	logs     []LogEntry
	oldestID int64
	hasMore  bool
}
type taskHistoryMsg struct {
	taskID   string
	drillKey string // legacy cell id, kept so the open view can re-fetch itself
	segments []TaskHistorySegmentItem
	detail   *TaskItem
}

// Live-refresh messages for the open Detail/Logs sub-views. Mirroring
// workflowMonitorRefreshMsg, their handlers update data in place and never touch
// View or the scroll cursor, so a refresh that lands after the user has navigated
// away is harmless.
type taskDetailRefreshMsg struct {
	detail   *TaskItem
	instance *WorkflowInstanceItem
}
type tailTaskLogsMsg struct {
	taskID   string // must match LogTaskID for the append to apply
	logs     []LogEntry
	newestID int64
}
type taskHistoryRefreshMsg struct {
	segments []TaskHistorySegmentItem
	detail   *TaskItem
}
type agentsDataMsg struct{ agents []AgentStatus }
type agentActivityMsg struct {
	agentID string
	items   []TaskItem
}
type agentTaskLogsMsg struct {
	taskID string
	logs   []LogEntry
	detail *TaskItem
}
type logsDataMsg struct{ logs []LogEntry }
type usageDataMsg struct{ data UsageTab }
type workflowsConfigMsg struct{ workflows []WorkflowConfigItem }
type workflowMonitorMsg struct {
	taskID    string
	instances []*WorkflowInstanceItem // all instances for the task, newest-first
}
type workflowStepLogsMsg struct {
	stepID string
	open   bool // user opened the panel (vs a live-tail refresh of an open panel)
	logs   []LogEntry
}

// mdWarmedMsg delivers glamour-rendered markdown log lines computed off the UI
// thread by warmMarkdownCmd. width is the wrap width the batch was rendered at;
// a batch that no longer matches the cache width (resize mid-flight) is dropped.
type mdWarmedMsg struct {
	width    int
	rendered map[string][]string
}

// ── lifecycle ───────────────────────────────────────────────────────────────

// Init initializes the app: enter alt-screen, fetch the first tab, start timer.
// Workflows config is fetched eagerly so it is ready before the user navigates there.
func (a *App) Init() tea.Cmd {
	return tea.Batch(
		tea.EnterAltScreen,
		a.fetchActiveTab(),
		a.fetchWorkflowsConfig(),
		tickCmd(),
		spinnerTickCmd(),
	)
}

// Update handles messages. This is the ONLY place the model is mutated.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if a == nil || a.model == nil {
		return a, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return a.handleKeyMsg(msg)

	case tea.WindowSizeMsg:
		a.model.width = msg.Width
		a.model.height = msg.Height
		// The resize drops the width-scoped markdown cache; re-warm whatever log
		// entries are currently loaded so styled output comes back. An open
		// transcript view re-renders at the new width the same way.
		return a, tea.Batch(a.warmOpenLogsCmd(), a.warmTranscriptCmd())

	case mdWarmedMsg:
		// Merge the off-thread glamour renders. A batch from before a resize no
		// longer matches the cache width and is dropped; pending is cleared either
		// way so the next refresh can re-dispatch.
		for m, lines := range msg.rendered {
			delete(a.logMDPending, m)
			if a.logMDCache != nil && a.logMDWidth == msg.width {
				a.logMDCache[m] = lines
			}
		}

	case tickMsg:
		// Re-query the active tab and schedule the next tick.
		a.model.tickCount++
		return a, tea.Batch(a.fetchActiveTab(), tickCmd())

	case spinnerTickMsg:
		// Advance the loading-spinner animation and keep the fast tick alive. The
		// frame only matters while a view is rendering a loading indicator, but the
		// loop runs continuously so the animation is ready the instant loading flips.
		a.model.spinnerFrame++
		return a, spinnerTickCmd()

	case overviewDataMsg:
		if a.model.overviewTab != nil {
			*a.model.overviewTab = msg.data
		}
		a.model.loading = false
		a.model.lastRefresh = time.Now()

	case tasksDataMsg:
		if a.model.tasksTab != nil {
			a.model.tasksTab.History = msg.items
			if a.model.tasksTab.SelectedIdx >= len(msg.items) {
				a.model.tasksTab.SelectedIdx = 0
			}
		}
		a.model.loading = false
		a.model.lastRefresh = time.Now()

	case taskDetailMsg:
		if a.model.tasksTab != nil {
			// A fresh open (d/enter/r) resets the scroll to the top; the in-place
			// live refresh (taskDetailRefreshMsg) preserves the reader's position.
			if a.model.tasksTab.Detail == nil || a.model.tasksTab.Detail.TaskID != msg.detail.TaskID {
				a.model.tasksTab.DetailScroll = 0
			}
			a.model.tasksTab.Detail = msg.detail
			a.model.tasksTab.DetailInstance = msg.instance
			a.model.tasksTab.View = TaskViewDetail
		}
		a.model.loading = false

	case taskPullsRefreshedMsg:
		// Daemon persisted fresh PRs; reload the open detail so they appear and the
		// (p) shortcut targets the latest one.
		if t := a.model.tasksTab; t != nil && t.View == TaskViewDetail && t.Detail != nil &&
			t.Detail.InternalTaskID == msg.internalTaskID && msg.internalTaskID != "" {
			return a, a.refreshTaskDetail(t.Detail.DrillKey, msg.internalTaskID)
		}

	case taskLogsMsg:
		if t := a.model.tasksTab; t != nil {
			t.Logs = msg.logs
			t.InstanceHistory = nil
			t.Detail = msg.detail
			t.LogScroll = 0
			t.LogFollow = true // open pinned to the tail; render anchors the viewport
			t.View = TaskViewLogs
			t.LogTaskID = msg.taskID
			t.LogOldestID = msg.oldestID
			t.LogNewestID = msg.newestID
			t.LogHasMore = msg.hasMore
			t.LogLoadingMore = false
			// Flat-log mode: refresh by drill key, not internal id.
			t.LogDrillKey = msg.taskID
			t.LogInternalTaskID = ""
		}
		a.model.loading = false
		return a, a.warmMarkdownCmd(msg.logs)

	case olderTaskLogsMsg:
		if t := a.model.tasksTab; t != nil && t.View == TaskViewLogs && msg.taskID == t.LogTaskID {
			t.LogLoadingMore = false
			if len(msg.logs) > 0 {
				// Prepend older lines and keep the viewport anchored: the prior top
				// line shifts down by the number of visual lines the new entries add.
				delta := len(a.logEntryLines(msg.logs))
				t.Logs = append(append([]LogEntry{}, msg.logs...), t.Logs...)
				t.LogScroll += delta
				t.LogOldestID = msg.oldestID
			}
			t.LogHasMore = msg.hasMore
			return a, a.warmMarkdownCmd(msg.logs)
		}

	case taskHistoryMsg:
		if t := a.model.tasksTab; t != nil {
			t.InstanceHistory = msg.segments
			t.Logs = nil
			t.Detail = msg.detail
			t.LogScroll = 0
			t.LogFollow = true // open pinned to the tail; render anchors the viewport
			t.View = TaskViewLogs
			// History (per-instance) path is segment-bounded — no flat-log cursor.
			t.LogTaskID = ""
			t.LogHasMore = false
			t.LogLoadingMore = false
			// History mode: refresh re-runs the full per-instance history by id.
			t.LogInternalTaskID = msg.taskID
			t.LogDrillKey = msg.drillKey
		}
		a.model.loading = false
		return a, a.warmMarkdownCmd(segmentLogs(msg.segments))

	case agentsDataMsg:
		if a.model.agentsTab != nil {
			a.model.agentsTab.Agents = msg.agents
			if a.model.agentsTab.SelectedIdx >= len(msg.agents) {
				a.model.agentsTab.SelectedIdx = 0
			}
			// Re-point detail pointer after refresh so changes persist.
			if a.model.agentsTab.Detail != nil {
				for i := range msg.agents {
					if msg.agents[i].ID == a.model.agentsTab.Detail.ID {
						a.model.agentsTab.Detail = &msg.agents[i]
						break
					}
				}
			}
		}
		a.model.loading = false
		a.model.lastRefresh = time.Now()

	case agentActivityMsg:
		if a.model.agentsTab != nil {
			a.model.agentsTab.Activity = msg.items
			a.model.agentsTab.ActivityIdx = 0
			a.model.agentsTab.View = AgentViewActivity
		}
		a.model.loading = false

	case agentTaskLogsMsg:
		if ag := a.model.agentsTab; ag != nil {
			// A live-tail refresh of the already-open view swaps the data in place
			// and leaves the scroll cursor alone (render re-pins when following).
			refresh := ag.View == AgentViewTaskLogs && ag.LogsTaskID == msg.taskID
			ag.LogsTaskID = msg.taskID
			ag.LogsTask = msg.detail
			ag.TaskLogs = msg.logs
			if !refresh {
				ag.TaskLogIdx = 0
				ag.TaskLogFollow = true // open pinned to the tail
				ag.View = AgentViewTaskLogs
			}
		}
		a.model.loading = false
		return a, a.warmMarkdownCmd(msg.logs)

	case logsDataMsg:
		if a.model.logsTab != nil {
			a.model.logsTab.Logs = msg.logs
		}
		a.model.loading = false
		a.model.lastRefresh = time.Now()
		return a, a.warmMarkdownCmd(msg.logs)

	case usageDataMsg:
		if a.model.usageTab != nil {
			*a.model.usageTab = msg.data
		}
		a.model.loading = false
		a.model.lastRefresh = time.Now()

	case workflowsConfigMsg:
		if a.model.workflowsTab != nil {
			a.model.workflowsTab.Workflows = msg.workflows
		}
		a.model.loading = false
		a.model.lastRefresh = time.Now()

	case workflowMonitorMsg:
		if a.model.tasksTab != nil && len(msg.instances) > 0 {
			a.model.tasksTab.WorkflowInstances = msg.instances
			a.model.tasksTab.WorkflowInstanceIdx = 0
			a.model.tasksTab.WorkflowInstance = msg.instances[0]
			a.model.tasksTab.WorkflowStepIdx = 0
			a.model.tasksTab.WorkflowLogs = nil
			a.model.tasksTab.WorkflowLogScroll = 0
			a.model.tasksTab.WorkflowLogStepID = ""
			a.model.tasksTab.WorkflowShowLogs = false
			a.model.tasksTab.View = TaskViewWorkflow
		}
		a.model.loading = false

	case workflowStepLogsMsg:
		if t := a.model.tasksTab; t != nil {
			if msg.open {
				t.WorkflowLogs = msg.logs
				t.WorkflowLogStepID = msg.stepID
				t.WorkflowLogScroll = 0
				t.WorkflowLogFollow = true // open pinned to the tail
				t.WorkflowShowLogs = true
			} else if t.WorkflowShowLogs && t.WorkflowLogStepID == msg.stepID {
				// Live-tail refresh: swap data only; a stale refresh for a closed
				// panel or another step is dropped.
				t.WorkflowLogs = msg.logs
			} else {
				a.model.loading = false
				return a, nil // dropped — don't warm what isn't shown
			}
		}
		a.model.loading = false
		return a, a.warmMarkdownCmd(msg.logs)

	case workflowMonitorRefreshMsg:
		if t := a.model.tasksTab; t != nil && msg.instance != nil {
			// Update step states without resetting the cursor position. Write the
			// refreshed instance back into the slice too, so switching away and
			// back keeps the live state.
			t.WorkflowInstance = msg.instance
			if t.WorkflowInstanceIdx >= 0 && t.WorkflowInstanceIdx < len(t.WorkflowInstances) {
				t.WorkflowInstances[t.WorkflowInstanceIdx] = msg.instance
			}
		}
		a.model.loading = false

	case taskDetailRefreshMsg:
		// Live-refresh the open detail panel in place. Skip a nil detail (a transient
		// query miss) so the panel keeps its last good content instead of blanking.
		if t := a.model.tasksTab; t != nil && t.View == TaskViewDetail && msg.detail != nil {
			t.Detail = msg.detail
			t.DetailInstance = msg.instance
		}

	case taskTranscriptMsg:
		if cmd := a.applyTranscriptMsg(msg); cmd != nil {
			return a, cmd
		}

	case transcriptWarmedMsg:
		a.applyTranscriptWarmed(msg)

	case tailTaskLogsMsg:
		// Append newly-arrived flat-log lines. While LogFollow is on, render keeps
		// the viewport pinned to the tail; otherwise the appended lines sit below
		// and the reader's position is left untouched.
		if t := a.model.tasksTab; t != nil && t.View == TaskViewLogs &&
			len(t.InstanceHistory) == 0 && msg.taskID == t.LogTaskID && len(msg.logs) > 0 {
			t.Logs = append(t.Logs, msg.logs...)
			t.LogNewestID = msg.newestID
			return a, a.warmMarkdownCmd(msg.logs)
		}

	case taskHistoryRefreshMsg:
		// Live-refresh the per-instance history view. Earlier (terminal) segments
		// keep a stable line count, so a scrolled-up reader's top-anchored position
		// holds; while LogFollow is on, render keeps the viewport on the tail.
		if t := a.model.tasksTab; t != nil && t.View == TaskViewLogs && len(t.InstanceHistory) > 0 {
			t.InstanceHistory = msg.segments
			if msg.detail != nil {
				t.Detail = msg.detail
			}
			return a, a.warmMarkdownCmd(segmentLogs(msg.segments))
		}
	}

	return a, nil
}

func (a *App) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Quit is always available.
	if key == "q" || key == "ctrl+c" {
		return a, tea.Quit
	}

	// Open the focused task in the browser, from any task-oriented view.
	if key == "o" {
		if u, ok := a.focusedTaskURL(); ok {
			return a, openURLCmd(u)
		}
		return a, nil
	}

	// Show the focused task's markdown transcript (assistant messages,
	// thinking, tool calls) rendered inside the dashboard. Only consumes the
	// key when a task is focused and a transcript exists — other views (e.g.
	// the agent file viewer's raw/rendered toggle) keep their own "t" bindings.
	if key == "t" && a.model.tasksTab != nil && a.model.tasksTab.View != TaskViewTranscript {
		if handled, cmd := a.openTranscriptView(); handled {
			return a, cmd
		}
	}

	// Open the focused task's most recent pull request in the browser.
	if key == "p" {
		if u, ok := a.focusedTaskPRURL(); ok {
			return a, openURLCmd(u)
		}
		return a, nil
	}

	// Handle confirmation prompts.
	if a.model.confirmAction != "" {
		switch key {
		case "y", "Y":
			action := a.model.confirmAction
			id := a.model.confirmTaskID
			a.model.confirmAction = ""
			a.model.confirmTaskID = ""
			switch action {
			case "restart":
				return a, a.restartTaskCmd(id)
			case "clear":
				return a, a.clearLogsCmd(id)
			case "stop":
				return a, a.stopInstanceCmd(id)
			}
		default:
			a.model.confirmAction = ""
			a.model.confirmTaskID = ""
		}
		return a, nil
	}

	// Force-restart the focused task: cancel and re-dispatch (Shift+R).
	if key == "R" {
		if id, ok := a.focusedTaskID(); ok {
			a.model.confirmAction = "restart"
			a.model.confirmTaskID = id
			return a, nil
		}
		return a, nil
	}

	// Clear logs for the focused task.
	if key == "c" || key == "C" {
		if id, ok := a.focusedTaskID(); ok {
			a.model.confirmAction = "clear"
			a.model.confirmTaskID = id
			return a, nil
		}
		return a, nil
	}

	// While a Tasks sub-view (detail/logs/workflow) is open, keys are scoped to it.
	if a.model.ActiveTab() == "Tasks" && a.model.tasksTab != nil && a.model.tasksTab.View != TaskViewList {
		return a.handleTaskSubViewKey(key)
	}

	// Workflow config tab: scope navigation keys when the step panel is focused.
	if a.model.ActiveTab() == "Workflows" && a.model.workflowsTab != nil {
		switch key {
		case "up", "down", "k", "j", "enter", "right", "l", "esc", "left", "h", "backspace":
			return a.handleWorkflowsKey(key)
		}
	}
	// While an Agents sub-view (detail/activity) is open, keys are scoped to it.
	if a.model.ActiveTab() == "Agents" && a.model.agentsTab != nil && a.model.agentsTab.View != AgentViewList {
		return a.handleAgentSubViewKey(key)
	}

	const hStep = 8

	switch key {
	case "tab":
		a.model.NextTab()
		a.model.loading = true
		return a, a.fetchActiveTab()
	case "shift+tab":
		a.model.PrevTab()
		a.model.loading = true
		return a, a.fetchActiveTab()
	case "right":
		// In the Logs tab (unwrapped), → scrolls the message horizontally.
		if a.model.ActiveTab() == "Logs" && a.model.logsTab != nil && !a.model.logsTab.Wrap {
			a.model.logsTab.HScroll += hStep
			return a, nil
		}
		a.model.NextTab()
		a.model.loading = true
		return a, a.fetchActiveTab()
	case "left":
		if a.model.ActiveTab() == "Logs" && a.model.logsTab != nil && !a.model.logsTab.Wrap {
			a.model.logsTab.HScroll -= hStep
			if a.model.logsTab.HScroll < 0 {
				a.model.logsTab.HScroll = 0
			}
			return a, nil
		}
		a.model.PrevTab()
		a.model.loading = true
		return a, a.fetchActiveTab()
	case "w":
		// Toggle line-wrap in the Logs tab.
		if a.model.ActiveTab() == "Logs" && a.model.logsTab != nil {
			a.model.logsTab.Wrap = !a.model.logsTab.Wrap
			a.model.logsTab.HScroll = 0
			a.model.logsTab.Scrolled = 0
		}
	case "r":
		a.model.loading = true
		return a, a.fetchActiveTab()
	case "/":
		if a.model.ActiveTab() == "Tasks" && a.model.tasksTab != nil && a.model.tasksTab.View == TaskViewList {
			a.model.tasksTab.FilterActive = true
			a.model.tasksTab.FilterText = ""
		}
	case "s":
		if a.model.ActiveTab() == "Tasks" && a.model.tasksTab != nil && a.model.tasksTab.View == TaskViewList {
			t := a.model.tasksTab
			if t.SortField == "" || t.SortField == "time" {
				t.SortField = "status"
				t.SortAsc = true
			} else {
				t.SortAsc = !t.SortAsc
			}
		}
	case "S":
		if a.model.ActiveTab() == "Tasks" && a.model.tasksTab != nil && a.model.tasksTab.View == TaskViewList {
			t := a.model.tasksTab
			switch t.SortField {
			case "", "time":
				t.SortField = "status"
				t.SortAsc = true
			case "status":
				t.SortField = "agent"
				t.SortAsc = true
			case "agent":
				t.SortField = "number"
				t.SortAsc = true
			case "number":
				t.SortField = "title"
				t.SortAsc = true
			case "title":
				t.SortField = "updated"
				t.SortAsc = false
			case "updated":
				t.SortField = "time"
				t.SortAsc = false
			}
		}
	case "esc":
		if a.model.ActiveTab() == "Tasks" && a.model.tasksTab != nil && a.model.tasksTab.FilterActive {
			a.model.tasksTab.FilterActive = false
			a.model.tasksTab.FilterText = ""
			a.model.tasksTab.SelectedIdx = 0
		}
	case "backspace":
		if a.model.ActiveTab() == "Tasks" && a.model.tasksTab != nil && a.model.tasksTab.FilterActive && len(a.model.tasksTab.FilterText) > 0 {
			a.model.tasksTab.FilterText = a.model.tasksTab.FilterText[:len(a.model.tasksTab.FilterText)-1]
			a.model.tasksTab.SelectedIdx = 0
		}
	case "up":
		switch a.model.ActiveTab() {
		case "Tasks":
			if a.model.tasksTab != nil && a.model.tasksTab.SelectedIdx > 0 {
				a.model.tasksTab.SelectedIdx--
			}
		case "Agents":
			if a.model.agentsTab != nil && a.model.agentsTab.SelectedIdx > 0 {
				a.model.agentsTab.SelectedIdx--
			}
		case "Logs":
			if a.model.logsTab != nil && a.model.logsTab.Scrolled > 0 {
				a.model.logsTab.Scrolled--
				a.model.logsTab.Follow = false
			}
		}
	case "down":
		switch a.model.ActiveTab() {
		case "Tasks":
			if a.model.tasksTab != nil {
				maxIdx := len(a.filteredTasks(a.model.tasksTab)) - 1
				if a.model.tasksTab.SelectedIdx < maxIdx {
					a.model.tasksTab.SelectedIdx++
				}
			}
		case "Agents":
			if a.model.agentsTab != nil && a.model.agentsTab.SelectedIdx < len(a.model.agentsTab.Agents)-1 {
				a.model.agentsTab.SelectedIdx++
			}
		case "Logs":
			if a.model.logsTab != nil && a.model.logsTab.Scrolled < len(a.logVisualLines())-1 {
				a.model.logsTab.Scrolled++
			}
		}
	case "home":
		switch a.model.ActiveTab() {
		case "Tasks":
			if a.model.tasksTab != nil {
				a.model.tasksTab.SelectedIdx = 0
			}
		case "Agents":
			if a.model.agentsTab != nil {
				a.model.agentsTab.SelectedIdx = 0
			}
		case "Logs":
			if a.model.logsTab != nil {
				a.model.logsTab.Scrolled = 0
				a.model.logsTab.Follow = false
			}
		}
	case "end":
		switch a.model.ActiveTab() {
		case "Tasks":
			if a.model.tasksTab != nil {
				a.model.tasksTab.SelectedIdx = len(a.filteredTasks(a.model.tasksTab)) - 1
			}
		case "Agents":
			if a.model.agentsTab != nil {
				a.model.agentsTab.SelectedIdx = len(a.model.agentsTab.Agents) - 1
			}
		case "Logs":
			if a.model.logsTab != nil {
				a.model.logsTab.Scrolled = lastIndex(len(a.logVisualLines()))
				a.model.logsTab.Follow = true
			}
		}
	case "pgup", "ctrl+u":
		switch a.model.ActiveTab() {
		case "Tasks":
			if a.model.tasksTab != nil {
				a.model.tasksTab.SelectedIdx -= a.pageSize()
				if a.model.tasksTab.SelectedIdx < 0 {
					a.model.tasksTab.SelectedIdx = 0
				}
			}
		case "Agents":
			if a.model.agentsTab != nil {
				a.model.agentsTab.SelectedIdx -= a.pageSize()
				if a.model.agentsTab.SelectedIdx < 0 {
					a.model.agentsTab.SelectedIdx = 0
				}
			}
		case "Logs":
			if a.model.logsTab != nil {
				a.model.logsTab.Scrolled = clampScroll(a.model.logsTab.Scrolled-a.pageSize(), len(a.logVisualLines()))
				a.model.logsTab.Follow = false
			}
		}
	case "pgdown", "ctrl+d", " ":
		switch a.model.ActiveTab() {
		case "Tasks":
			if a.model.tasksTab != nil {
				a.model.tasksTab.SelectedIdx += a.pageSize()
				maxIdx := len(a.filteredTasks(a.model.tasksTab)) - 1
				if a.model.tasksTab.SelectedIdx > maxIdx {
					a.model.tasksTab.SelectedIdx = maxIdx
				}
			}
		case "Agents":
			if a.model.agentsTab != nil {
				a.model.agentsTab.SelectedIdx += a.pageSize()
				maxIdx := len(a.model.agentsTab.Agents) - 1
				if a.model.agentsTab.SelectedIdx > maxIdx {
					a.model.agentsTab.SelectedIdx = maxIdx
				}
			}
		case "Logs":
			if a.model.logsTab != nil {
				a.model.logsTab.Scrolled = clampScroll(a.model.logsTab.Scrolled+a.pageSize(), len(a.logVisualLines()))
			}
		}
	case "enter", "l":
		switch a.model.ActiveTab() {
		case "Tasks":
			if id, ok := a.selectedTaskID(); ok {
				a.model.loading = true
				// Open the workflow monitor if this task has a workflow instance,
				// otherwise fall back to the task logs view.
				return a, a.openWorkflowMonitorOrLogs(id)
			}
		case "Agents":
			if id, ok := a.selectedAgentID(); ok {
				a.model.loading = true
				return a, a.fetchAgentActivity(id)
			}
		}
	case "d":
		switch a.model.ActiveTab() {
		case "Tasks":
			if row, ok := a.selectedTask(); ok {
				a.model.loading = true
				return a, tea.Batch(a.fetchTaskDetail(row.TaskID, row.InternalTaskID), a.refreshTaskPullsCmd(row.InternalTaskID))
			}
		case "Agents":
			// Detail uses the already-loaded stats — no DB round-trip needed.
			if ag, ok := a.selectedAgent(); ok {
				a.model.agentsTab.Detail = ag
				a.model.agentsTab.View = AgentViewDetail
			}
		}
	default:
		// If task filter is active, any printable char appends to filter text
		if a.model.ActiveTab() == "Tasks" && a.model.tasksTab != nil && a.model.tasksTab.FilterActive && len(key) == 1 && key >= " " && key <= "~" {
			a.model.tasksTab.FilterText += key
			a.model.tasksTab.SelectedIdx = 0
			return a, nil
		}
	}
	return a, nil
}

// selectedAgent returns the agent under the cursor in the Agents list.
func (a *App) selectedAgent() (*AgentStatus, bool) {
	ag := a.model.agentsTab
	if ag == nil || ag.SelectedIdx < 0 || ag.SelectedIdx >= len(ag.Agents) {
		return nil, false
	}
	sel := ag.Agents[ag.SelectedIdx]
	return &sel, true
}

func (a *App) selectedAgentID() (string, bool) {
	if ag, ok := a.selectedAgent(); ok {
		return ag.ID, true
	}
	return "", false
}

// handleAgentSubViewKey handles keys while an agent detail/activity/task-logs
// view is open. The activity view mirrors the Tasks tab: ↑/↓ move a cursor and
// enter/l drills into the selected task's logs.
// In detail view: m cycles model, w cycles max_workers, both persist via IPC.
func (a *App) handleAgentSubViewKey(key string) (tea.Model, tea.Cmd) {
	ag := a.model.agentsTab

	// Task-logs drill-down has its own key map.
	if ag.View == AgentViewTaskLogs {
		switch key {
		case "esc", "backspace", "h", "left":
			ag.View = AgentViewActivity
			ag.TaskLogs = nil
			ag.LogsTaskID = ""
			ag.TaskLogIdx = 0
		case "up":
			ag.TaskLogFollow = false
			if ag.TaskLogIdx > 0 {
				ag.TaskLogIdx--
			}
		case "down":
			if ag.TaskLogIdx < lastIndex(len(a.agentTaskLogLines())) {
				ag.TaskLogIdx++
			}
		case "g", "home":
			ag.TaskLogIdx = 0
			ag.TaskLogFollow = false
		case "G", "end":
			ag.TaskLogIdx = lastIndex(len(a.agentTaskLogLines()))
			ag.TaskLogFollow = true
		case "pgup", "ctrl+u":
			ag.TaskLogIdx = clampScroll(ag.TaskLogIdx-a.pageSize(), len(a.agentTaskLogLines()))
			ag.TaskLogFollow = false
		case "pgdown", "ctrl+d", " ":
			ag.TaskLogIdx = clampScroll(ag.TaskLogIdx+a.pageSize(), len(a.agentTaskLogLines()))
		case "r":
			if ag.LogsTaskID != "" {
				a.model.loading = true
				return a, a.fetchAgentTaskLogs(ag.LogsTaskID)
			}
		}
		return a, nil
	}

	// File content viewer has its own scroll key map.
	if ag.View == AgentViewFileContent {
		lines := a.agentFileLines()
		switch key {
		case "esc", "backspace", "h", "left":
			if ag.FileReturn != AgentViewList {
				ag.View = ag.FileReturn
			} else {
				ag.View = AgentViewFiles
			}
			ag.FileReturn = AgentViewList
			ag.FileContent = ""
			ag.FileErr = ""
			ag.FileScroll = 0
			ag.invalidateFileLines()
		case "t":
			// Toggle between rendered markdown and raw text. Reset scroll since
			// the line count changes between modes.
			ag.FileRaw = !ag.FileRaw
			ag.FileScroll = 0
			ag.invalidateFileLines()
		case "up":
			if ag.FileScroll > 0 {
				ag.FileScroll--
			}
		case "down":
			if ag.FileScroll < lastIndex(len(lines)) {
				ag.FileScroll++
			}
		case "g", "home":
			ag.FileScroll = 0
		case "G", "end":
			ag.FileScroll = lastIndex(len(lines))
		case "pgup", "ctrl+u":
			ag.FileScroll = clampScroll(ag.FileScroll-a.pageSize(), len(lines))
		case "pgdown", "ctrl+d", " ":
			ag.FileScroll = clampScroll(ag.FileScroll+a.pageSize(), len(lines))
		}
		return a, nil
	}

	// Related-files list has its own navigation key map.
	if ag.View == AgentViewFiles {
		switch key {
		case "esc", "backspace", "h", "left":
			ag.View = AgentViewDetail
			ag.Files = nil
			ag.FilesIdx = 0
		case "up":
			if ag.FilesIdx > 0 {
				ag.FilesIdx--
			}
		case "down":
			if ag.FilesIdx < lastIndex(len(ag.Files)) {
				ag.FilesIdx++
			}
		case "g", "home":
			ag.FilesIdx = 0
		case "G", "end":
			ag.FilesIdx = lastIndex(len(ag.Files))
		case "enter", "l", "right":
			a.openSelectedAgentFile()
		}
		return a, nil
	}

	switch key {
	case "esc", "backspace", "h", "left":
		ag.View = AgentViewList
		ag.Detail = nil
		ag.Activity = nil
		ag.ActivityIdx = 0
	case "d":
		if a2, ok := a.selectedAgent(); ok {
			ag.Detail = a2
			ag.View = AgentViewDetail
		}
	case "f":
		// From the detail view: open the agent's related files (soul + skills).
		if ag.View == AgentViewDetail && ag.Detail != nil {
			ag.Files = a.buildAgentFiles(ag.Detail)
			ag.FilesIdx = 0
			ag.View = AgentViewFiles
		}
	case "l", "enter":
		// From the list/detail: open the agent's activity. From the activity
		// list: drill into the selected task's logs.
		if ag.View == AgentViewActivity {
			if id, ok := a.selectedActivityTaskID(); ok {
				a.model.loading = true
				return a, a.fetchAgentTaskLogs(id)
			}
			return a, nil
		}
		if id, ok := a.selectedAgentID(); ok {
			a.model.loading = true
			return a, a.fetchAgentActivity(id)
		}
	case "m":
		// Cycle single model: rotate through runner.models one at a time.
		if ag.View == AgentViewDetail && ag.Detail != nil {
			models := ag.Detail.RunnerModels
			if len(models) < 2 {
				models = []string{ag.Detail.Model}
			}
			if len(models) > 1 {
				cur := ag.Detail.Model
				next := models[0]
				for i, m := range models {
					if m == cur && i+1 < len(models) {
						next = models[i+1]
						break
					}
				}
				ag.Detail.Model = next
				return a, a.updateAgentConfigCmd(ag.Detail.ID, next, "", 0)
			}
		}
	case "w":
		// Cycle max_workers in agent detail view: 1→2→3→4→5→1.
		if ag.View == AgentViewDetail && ag.Detail != nil {
			current := ag.Detail.MaxWorkers
			if current < 1 {
				current = 1
			}
			next := current%5 + 1
			ag.Detail.MaxWorkers = next
			return a, a.updateAgentConfigCmd(ag.Detail.ID, "", "", next)
		}
	case "r":
		// Detail view: cycle runner (and auto-select matching model).
		if ag.View == AgentViewDetail && ag.Detail != nil && len(ag.Detail.Runners) > 1 {
			runners := ag.Detail.Runners
			current := ag.Detail.RunnerType
			next := runners[0]
			for i, r := range runners {
				if r == current && i+1 < len(runners) {
					next = runners[i+1]
					break
				}
			}
			// Find a suitable model for the new runner:
			// 1. Use the runner's Models list if available
			// 2. Fall back to another agent that uses this runner
			newModel := ""
			if a.cfg != nil {
				for _, rc := range a.cfg.Runners {
					if rc.ID == next && len(rc.Models) > 0 {
						newModel = rc.Models[0]
						break
					}
				}
				if newModel == "" {
					for _, ac := range a.cfg.Agents {
						if ac.Runner == next && ac.Model != "" {
							newModel = ac.Model
							break
						}
					}
				}
			}
			if a.cfg != nil {
				for _, rc := range a.cfg.Runners {
					if rc.ID == next {
						ag.Detail.RunnerModels = rc.Models
						break
					}
				}
			}
			if newModel != "" {
				ag.Detail.Model = newModel
			}
			return a, a.updateAgentConfigCmd(ag.Detail.ID, newModel, next, 0)
		}
		if id, ok := a.selectedAgentID(); ok && ag.View == AgentViewActivity {
			a.model.loading = true
			return a, a.fetchAgentActivity(id)
		}
	case "up":
		if ag.View == AgentViewActivity && ag.ActivityIdx > 0 {
			ag.ActivityIdx--
		}
	case "down":
		if ag.View == AgentViewActivity && ag.ActivityIdx < lastIndex(len(ag.Activity)) {
			ag.ActivityIdx++
		}
	case "g", "home":
		if ag.View == AgentViewActivity {
			ag.ActivityIdx = 0
		}
	case "G", "end":
		if ag.View == AgentViewActivity {
			ag.ActivityIdx = lastIndex(len(ag.Activity))
		}
	case "pgup", "ctrl+u":
		if ag.View == AgentViewActivity {
			ag.ActivityIdx = clampScroll(ag.ActivityIdx-a.pageSize(), len(ag.Activity))
		}
	case "pgdown", "ctrl+d", " ":
		if ag.View == AgentViewActivity {
			ag.ActivityIdx = clampScroll(ag.ActivityIdx+a.pageSize(), len(ag.Activity))
		}
	}
	return a, nil
}

// selectedActivityTaskID returns the task id under the cursor in an agent's
// activity list.
func (a *App) selectedActivityTaskID() (string, bool) {
	ag := a.model.agentsTab
	if ag == nil || ag.ActivityIdx < 0 || ag.ActivityIdx >= len(ag.Activity) {
		return "", false
	}
	return ag.Activity[ag.ActivityIdx].TaskID, true
}

// handleTaskSubViewKey handles keys while a task detail/logs/workflow sub-view is open.
func (a *App) handleTaskSubViewKey(key string) (tea.Model, tea.Cmd) {
	t := a.model.tasksTab

	// Workflow monitor has its own key map.
	if t.View == TaskViewWorkflow {
		return a.handleWorkflowMonitorKey(key)
	}

	// Transcript viewer has its own key map.
	if t.View == TaskViewTranscript {
		return a.handleTranscriptKey(key)
	}

	switch key {
	case "esc", "backspace", "h", "left":
		// Back to the list.
		t.View = TaskViewList
		t.Detail = nil
		t.Logs = nil
		t.InstanceHistory = nil
		t.LogScroll = 0
		t.DetailScroll = 0
		t.LogTaskID = ""
		t.LogHasMore = false
		t.LogLoadingMore = false
	case "d":
		if row, ok := a.selectedTask(); ok {
			a.model.loading = true
			return a, tea.Batch(a.fetchTaskDetail(row.TaskID, row.InternalTaskID), a.refreshTaskPullsCmd(row.InternalTaskID))
		}
	case "y", "n":
		if t.View == TaskViewDetail && t.DetailInstance != nil && t.DetailInstance.Approval != nil {
			decision := "approve"
			if key == "n" {
				decision = "reject"
			}
			return a, a.approvalResponseCmd(t.DetailInstance.Approval.ID, decision)
		}
	case "l", "enter":
		if row, ok := a.selectedTask(); ok {
			a.model.loading = true
			return a, a.fetchTaskHistory(row.InternalTaskID, row.TaskID)
		}
	case "r":
		if row, ok := a.selectedTask(); ok {
			a.model.loading = true
			if t.View == TaskViewLogs {
				return a, a.fetchTaskHistory(row.InternalTaskID, row.TaskID)
			}
			return a, tea.Batch(a.fetchTaskDetail(row.TaskID, row.InternalTaskID), a.refreshTaskPullsCmd(row.InternalTaskID))
		}
	case "up":
		if t.View == TaskViewLogs {
			t.LogFollow = false
			if t.LogScroll > 0 {
				t.LogScroll--
			} else if cmd := a.loadOlderLogsCmd(t); cmd != nil {
				return a, cmd // at top — pull in the next older page
			}
		} else if t.View == TaskViewDetail && t.DetailScroll > 0 {
			t.DetailScroll--
		}
	case "down":
		if t.View == TaskViewLogs && t.LogScroll < len(a.taskLogLines())-1 {
			t.LogScroll++
		} else if t.View == TaskViewDetail {
			t.DetailScroll++ // render clamps to the last full page
		}
	case "g", "home":
		if t.View == TaskViewLogs {
			t.LogScroll = 0
			t.LogFollow = false
		} else if t.View == TaskViewDetail {
			t.DetailScroll = 0
		}
	case "G", "end":
		if t.View == TaskViewLogs {
			t.LogScroll = lastIndex(len(a.taskLogLines()))
			t.LogFollow = true
		} else if t.View == TaskViewDetail {
			t.DetailScroll = len(a.taskDetailLines(t)) // render clamps to the last full page
		}
	case "pgup", "ctrl+u":
		if t.View == TaskViewLogs {
			t.LogFollow = false
			if t.LogScroll == 0 {
				if cmd := a.loadOlderLogsCmd(t); cmd != nil {
					return a, cmd
				}
			}
			t.LogScroll = clampScroll(t.LogScroll-a.pageSize(), len(a.taskLogLines()))
		} else if t.View == TaskViewDetail {
			t.DetailScroll -= a.pageSize()
			if t.DetailScroll < 0 {
				t.DetailScroll = 0
			}
		}
	case "pgdown", "ctrl+d", " ":
		if t.View == TaskViewLogs {
			t.LogScroll = clampScroll(t.LogScroll+a.pageSize(), len(a.taskLogLines()))
		} else if t.View == TaskViewDetail {
			t.DetailScroll += a.pageSize() // render clamps to the last full page
		}
	}
	return a, nil
}

// handleWorkflowMonitorKey handles keys inside the live workflow monitor.
func (a *App) handleWorkflowMonitorKey(key string) (tea.Model, tea.Cmd) {
	t := a.model.tasksTab
	inst := t.WorkflowInstance
	if inst == nil {
		t.View = TaskViewList
		return a, nil
	}

	switch key {
	case "esc", "backspace", "h", "left":
		if t.WorkflowShowLogs {
			t.WorkflowShowLogs = false
			t.WorkflowLogs = nil
			t.WorkflowLogStepID = ""
		} else {
			t.View = TaskViewList
			t.WorkflowInstance = nil
			t.WorkflowStepIdx = 0
			t.WorkflowLogs = nil
		}

	case "up", "k":
		if t.WorkflowShowLogs {
			t.WorkflowLogFollow = false
			if t.WorkflowLogScroll > 0 {
				t.WorkflowLogScroll--
			}
		} else if t.WorkflowStepIdx > 0 {
			t.WorkflowStepIdx--
			t.WorkflowLogs = nil
			t.WorkflowLogStepID = ""
			t.WorkflowShowLogs = false
		}

	case "down", "j":
		if t.WorkflowShowLogs {
			lines := a.wfStepLogLines()
			if t.WorkflowLogScroll < len(lines)-1 {
				t.WorkflowLogScroll++
			}
		} else if t.WorkflowStepIdx < len(inst.Steps)-1 {
			t.WorkflowStepIdx++
			t.WorkflowLogs = nil
			t.WorkflowLogStepID = ""
			t.WorkflowShowLogs = false
		}

	case "g", "home":
		if t.WorkflowShowLogs {
			t.WorkflowLogScroll = 0
			t.WorkflowLogFollow = false
		} else {
			t.WorkflowStepIdx = 0
		}

	case "G", "end":
		if t.WorkflowShowLogs {
			t.WorkflowLogScroll = lastIndex(len(a.wfStepLogLines()))
			t.WorkflowLogFollow = true
		} else {
			t.WorkflowStepIdx = lastIndex(len(inst.Steps))
		}

	case "pgup", "ctrl+u":
		if t.WorkflowShowLogs {
			t.WorkflowLogScroll = clampScroll(t.WorkflowLogScroll-a.pageSize(), len(a.wfStepLogLines()))
			t.WorkflowLogFollow = false
		} else {
			t.WorkflowStepIdx = clampScroll(t.WorkflowStepIdx-a.pageSize(), len(inst.Steps))
		}

	case "pgdown", "ctrl+d", " ":
		if t.WorkflowShowLogs {
			t.WorkflowLogScroll = clampScroll(t.WorkflowLogScroll+a.pageSize(), len(a.wfStepLogLines()))
		} else {
			t.WorkflowStepIdx = clampScroll(t.WorkflowStepIdx+a.pageSize(), len(inst.Steps))
		}

	case "]", ">":
		// Switch to a newer workflow instance (forward in time). The slice is
		// newest-first, so newer = a lower index.
		if !t.WorkflowShowLogs && t.WorkflowInstanceIdx > 0 {
			a.selectWorkflowInstance(t, t.WorkflowInstanceIdx-1)
		}

	case "[", "<":
		// Switch to an older workflow instance (back in time, e.g. triage).
		if !t.WorkflowShowLogs && t.WorkflowInstanceIdx < len(t.WorkflowInstances)-1 {
			a.selectWorkflowInstance(t, t.WorkflowInstanceIdx+1)
		}

	case "l", "enter":
		// Open logs for the selected step.
		if !t.WorkflowShowLogs && t.WorkflowStepIdx < len(inst.Steps) {
			step := inst.Steps[t.WorkflowStepIdx]
			a.model.loading = true
			return a, a.fetchWorkflowStepLogs(inst.CellID, step.StepID, step.StartedAt, step.FinishedAt, true)
		}

	case "r":
		// Refresh the monitor.
		if inst != nil {
			a.model.loading = true
			return a, a.refreshWorkflowMonitor(inst.ID, inst.CellID)
		}

	case "X":
		// Stop the workflow instance (no restart).
		a.model.confirmAction = "stop"
		a.model.confirmTaskID = inst.ID

	case "R":
		// Restart the whole workflow (force-restart the cell).
		a.model.confirmAction = "restart"
		a.model.confirmTaskID = inst.CellID

	case "y", "n":
		if inst.Approval != nil {
			decision := "approve"
			if key == "n" {
				decision = "reject"
			}
			return a, a.approvalResponseCmd(inst.Approval.ID, decision)
		}
	}
	return a, nil
}

// selectWorkflowInstance points the monitor at a different workflow instance for
// the same task and resets the step cursor and log panel to that instance.
func (a *App) selectWorkflowInstance(t *TasksTab, idx int) {
	if idx < 0 || idx >= len(t.WorkflowInstances) {
		return
	}
	t.WorkflowInstanceIdx = idx
	t.WorkflowInstance = t.WorkflowInstances[idx]
	t.WorkflowStepIdx = 0
	t.WorkflowLogs = nil
	t.WorkflowLogScroll = 0
	t.WorkflowLogStepID = ""
	t.WorkflowShowLogs = false
}

// handleWorkflowsKey handles keys in the static Workflows config tab.
func (a *App) handleWorkflowsKey(key string) (tea.Model, tea.Cmd) {
	wt := a.model.workflowsTab
	if wt == nil {
		return a, nil
	}
	switch key {
	case "up", "k":
		if wt.Focus == WorkflowsViewList {
			if wt.SelectedIdx > 0 {
				wt.SelectedIdx--
				wt.StepIdx = 0
				wt.StepScroll = 0
			}
		} else {
			if wt.StepIdx > 0 {
				wt.StepIdx--
				// StepScroll is adjusted by the renderer to keep StepIdx visible.
			}
		}
	case "down", "j":
		if wt.Focus == WorkflowsViewList {
			if wt.SelectedIdx < len(wt.Workflows)-1 {
				wt.SelectedIdx++
				wt.StepIdx = 0
				wt.StepScroll = 0
			}
		} else {
			if wt.SelectedIdx < len(wt.Workflows) {
				steps := wt.Workflows[wt.SelectedIdx].Steps
				if wt.StepIdx < len(steps)-1 {
					wt.StepIdx++
					// StepScroll is adjusted by the renderer to keep StepIdx visible.
				}
			}
		}
	case "enter", "right", "l":
		wt.Focus = WorkflowsViewSteps
		wt.StepIdx = 0
		wt.StepScroll = 0
	case "esc", "left", "h", "backspace":
		wt.Focus = WorkflowsViewList
	}
	return a, nil
}

// pageSize approximates the number of visible body rows in a tab, used for
// page-up/down scrolling. Mirrors the box body height in View.
func (a *App) pageSize() int {
	n := a.model.height - 5
	if n < 1 {
		n = 1
	}
	return n
}

// pinToTail returns the scroll offset that anchors a viewport of rows lines to
// the end of total lines — the start of the last full page (0 when everything
// fits). Renderers use it to keep follow-mode log views pinned to the tail.
func pinToTail(total, rows int) int {
	off := total - rows
	if off < 0 {
		return 0
	}
	return off
}

// clampScroll clamps a scroll offset to [0, total-1].
func clampScroll(v, total int) int {
	if v < 0 || total == 0 {
		return 0
	}
	if v > total-1 {
		return total - 1
	}
	return v
}

// lastIndex returns the index of the last line (0 when empty).
func lastIndex(total int) int {
	if total <= 0 {
		return 0
	}
	return total - 1
}

// selectedTask returns the TaskItem under the cursor in the Tasks list. It
// indexes the filtered/sorted view (what the user actually sees), not the raw
// History — SelectedIdx tracks the filtered list.
func (a *App) selectedTask() (*TaskItem, bool) {
	t := a.model.tasksTab
	if t == nil {
		return nil, false
	}
	rows := a.filteredTasks(t)
	if t.SelectedIdx < 0 || t.SelectedIdx >= len(rows) {
		return nil, false
	}
	return &rows[t.SelectedIdx], true
}

// selectedTaskID returns the drill key (legacy cell id) of the task under the
// cursor in the Tasks list — what the execution/logs/workflow machinery keys on.
func (a *App) selectedTaskID() (string, bool) {
	row, ok := a.selectedTask()
	if !ok {
		return "", false
	}
	return row.TaskID, true
}

// focusedTaskURL returns the source URL of the task the user is currently
// looking at — the selected row in the Tasks list, the open task detail, or the
// task drilled into from an agent's activity. Reports false when there is no URL.
func (a *App) focusedTaskURL() (string, bool) {
	switch a.model.ActiveTab() {
	case "Tasks":
		t := a.model.tasksTab
		if t == nil {
			return "", false
		}
		if t.View == TaskViewDetail && t.Detail != nil {
			return t.Detail.URL, t.Detail.URL != ""
		}
		if rows := a.filteredTasks(t); t.SelectedIdx >= 0 && t.SelectedIdx < len(rows) {
			u := rows[t.SelectedIdx].URL
			return u, u != ""
		}
	case "Agents":
		ag := a.model.agentsTab
		if ag == nil {
			return "", false
		}
		if ag.View == AgentViewActivity || ag.View == AgentViewTaskLogs {
			if ag.ActivityIdx >= 0 && ag.ActivityIdx < len(ag.Activity) {
				u := ag.Activity[ag.ActivityIdx].URL
				return u, u != ""
			}
		}
	}
	return "", false
}

// focusedTaskPulls returns the persisted pull requests of the task the user is
// currently looking at, across the same views as focusedTaskURL. Reports false
// when there is no focused task.
func (a *App) focusedTaskPulls() ([]PullRequestItem, bool) {
	switch a.model.ActiveTab() {
	case "Tasks":
		t := a.model.tasksTab
		if t == nil {
			return nil, false
		}
		if t.View == TaskViewDetail && t.Detail != nil {
			return t.Detail.PullRequests, true
		}
		if rows := a.filteredTasks(t); t.SelectedIdx >= 0 && t.SelectedIdx < len(rows) {
			return rows[t.SelectedIdx].PullRequests, true
		}
	case "Agents":
		ag := a.model.agentsTab
		if ag == nil {
			return nil, false
		}
		if ag.View == AgentViewActivity || ag.View == AgentViewTaskLogs {
			if ag.ActivityIdx >= 0 && ag.ActivityIdx < len(ag.Activity) {
				return ag.Activity[ag.ActivityIdx].PullRequests, true
			}
		}
	}
	return nil, false
}

// focusedTaskPRURL returns the most recent pull request URL of the focused task —
// the tail of its PR list, since the list is ordered oldest-first. Reports false
// when the task has no PR (then the (p) key is a no-op, like (o) with no URL).
func (a *App) focusedTaskPRURL() (string, bool) {
	prs, ok := a.focusedTaskPulls()
	if !ok || len(prs) == 0 {
		return "", false
	}
	u := prs[len(prs)-1].URL
	return u, u != ""
}

// focusedTaskID returns the task ID the user is currently focused on.
func (a *App) focusedTaskID() (string, bool) {
	switch a.model.ActiveTab() {
	case "Tasks":
		t := a.model.tasksTab
		if t == nil {
			return "", false
		}
		if t.View == TaskViewDetail && t.Detail != nil {
			return t.Detail.TaskID, true
		}
		if rows := a.filteredTasks(t); t.SelectedIdx >= 0 && t.SelectedIdx < len(rows) {
			return rows[t.SelectedIdx].TaskID, true
		}
	case "Agents":
		ag := a.model.agentsTab
		if ag == nil {
			return "", false
		}
		if ag.View == AgentViewActivity || ag.View == AgentViewTaskLogs {
			if ag.ActivityIdx >= 0 && ag.ActivityIdx < len(ag.Activity) {
				return ag.Activity[ag.ActivityIdx].TaskID, true
			}
		}
	}
	return "", false
}

// ipcClient returns an HTTP client that dials a.socketPath over Unix.
func (a *App) ipcClient(timeout time.Duration) *http.Client {
	socketPath := a.socketPath
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: timeout,
	}
}

// ipcPost builds an authenticated POST request for a mutating IPC endpoint.
func (a *App) ipcPost(ctx context.Context, url string, body []byte) (*http.Request, error) {
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Apiary-Control", daemon.ReadControlToken(a.dataDir))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// restartTaskCmd sends a force-restart request to the daemon via the IPC socket.
func (a *App) restartTaskCmd(taskID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := a.ipcPost(ctx, fmt.Sprintf("http://apiary/restart/%s", taskID), nil)
		if err != nil {
			return nil
		}
		resp, err := a.ipcClient(5 * time.Second).Do(req)
		if err != nil {
			return nil
		}
		resp.Body.Close()
		return nil
	}
}

// refreshTaskPullsCmd asks the daemon to (re)discover the task's pull requests from
// its source and persist them. The daemon holds the source adapters/credentials;
// the dashboard then reads the persisted list from its own DB. No-op for rows with
// no internal task id (e.g. Agents-tab history rows). On success it emits a
// taskPullsRefreshedMsg so the open detail reloads with the fresh PRs.
func (a *App) refreshTaskPullsCmd(internalTaskID string) tea.Cmd {
	if internalTaskID == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		req, err := a.ipcPost(ctx, fmt.Sprintf("http://apiary/tasks/pulls/refresh/%s", internalTaskID), nil)
		if err != nil {
			return nil
		}
		resp, err := a.ipcClient(8 * time.Second).Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil
		}
		return taskPullsRefreshedMsg{internalTaskID: internalTaskID}
	}
}

func (a *App) clearLogsCmd(taskID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := a.ipcPost(ctx, fmt.Sprintf("http://apiary/clearlogs/%s", taskID), nil)
		if err != nil {
			return nil
		}
		resp, err := a.ipcClient(5 * time.Second).Do(req)
		if err != nil {
			return nil
		}
		resp.Body.Close()
		return nil
	}
}

// stopInstanceCmd sends a POST /instances/stop/{id} request to stop a workflow
// instance without re-dispatching it.
func (a *App) stopInstanceCmd(instanceID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := a.ipcPost(ctx, fmt.Sprintf("http://apiary/instances/stop/%s", instanceID), nil)
		if err != nil {
			return nil
		}
		resp, err := a.ipcClient(5 * time.Second).Do(req)
		if err != nil {
			return nil
		}
		resp.Body.Close()
		return nil
	}
}

func (a *App) approvalResponseCmd(requestID, decision string) tea.Cmd {
	return func() tea.Msg {
		actor := os.Getenv("USER")
		if actor == "" {
			actor = "dashboard-user"
		}
		body, _ := json.Marshal(map[string]any{"decision": decision, "actor": actor, "idempotency_key": fmt.Sprintf("dashboard:%s:%s:%s", requestID, actor, decision)})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := a.ipcPost(ctx, "http://apiary/approvals/"+requestID+"/respond", body)
		if err != nil {
			return nil
		}
		resp, err := a.ipcClient(5 * time.Second).Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return nil
	}
}

// openWorkflowMonitorOrLogs opens the workflow monitor if the task has a
// workflow instance, or the logs view otherwise.
func (a *App) openWorkflowMonitorOrLogs(taskID string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		if dbConn != nil {
			// A task can fan out to several workflow instances over its life
			// (e.g. triage → implementation); load all of them, newest first, so
			// the monitor can switch between them with [ and ].
			insts, err := dbConn.ListWorkflowInstancesByCell(ctx, taskID)
			if err == nil && len(insts) > 0 {
				items := make([]*WorkflowInstanceItem, 0, len(insts))
				for i := range insts {
					items = append(items, buildWorkflowInstanceItem(ctx, dbConn, &insts[i]))
				}
				return workflowMonitorMsg{taskID: taskID, instances: items}
			}
		}
		// No workflow instance — fall back to logs view.
		return taskLogsMsg{taskID: taskID, logs: nil, detail: nil}
	}
}

// buildWorkflowInstanceItem hydrates a WorkflowInstanceItem (steps + per-step
// usage) from a stored workflow instance, for the live monitor views.
func buildWorkflowInstanceItem(ctx context.Context, dbConn *db.Client, inst *db.WorkflowInstance) *WorkflowInstanceItem {
	item := &WorkflowInstanceItem{
		ID:        inst.ID,
		Workflow:  inst.WorkflowID,
		State:     inst.State,
		CellID:    inst.CellID,
		CreatedAt: inst.CreatedAt,
	}
	if inst.State == "approval_waiting" {
		item.Message = "Awaiting human approval — reply on the task to resume or abort."
		item.Approval, _ = dbConn.GetApprovalByInstance(ctx, inst.ID)
	}
	steps, err := dbConn.ListStepRuns(ctx, inst.ID)
	if err == nil {
		usage := loadStepUsageFallback(ctx, dbConn, inst.ID, steps)
		now := time.Now()
		for _, s := range steps {
			si := WorkflowStepItem{
				StepID:     s.StepID,
				Agent:      s.AgentID,
				State:      s.State,
				Duration:   wfStepDuration(s, now),
				Cached:     s.SkippedCached,
				Output:     s.Output,
				Summary:    s.Summary,
				StartedAt:  s.StartedAt,
				FinishedAt: s.FinishedAt,
			}
			si.InputTokens, si.OutputTokens, si.TotalTokens, si.NumTurns, si.NumToolCalls, si.CostUSD =
				stepUsageFromMap(s, usage)
			item.Steps = append(item.Steps, si)
		}
		aggregateInstance(item)
	}
	if polls, err := dbConn.ListCIPollChecks(ctx, inst.ID); err == nil {
		item.CIPolls = mapCIPolls(polls)
	}
	return item
}

// loadStepUsageFallback fetches the per-instance executions usage map, but only
// when at least one step lacks its own rollup — modern instances (every step_run
// carries usage) skip the query entirely. This replaces the old per-step
// GetStepUsage fan-out (N+1) with at most one query per instance.
func loadStepUsageFallback(ctx context.Context, dbConn *db.Client, instID string, steps []db.StepRun) map[string]db.Execution {
	for _, s := range steps {
		if !db.StepRunHasUsage(s) {
			if m, err := dbConn.GetInstanceStepUsage(ctx, instID); err == nil {
				return m
			}
			return nil
		}
	}
	return nil
}

// stepUsageFromMap returns a step's token/turn/cost rollup, preferring the
// step_runs row's own summed columns and falling back to the per-instance usage
// map (loadStepUsageFallback) for older steps that predate the rollup. Returns
// zeros when neither carries usage.
func stepUsageFromMap(s db.StepRun, usage map[string]db.Execution) (in, out, total, turns, calls int, cost float64) {
	if db.StepRunHasUsage(s) {
		return s.InputTokens, s.OutputTokens, s.TotalTokens, s.NumTurns, s.NumToolCalls, s.CostUSD
	}
	if u, ok := usage[s.StepID]; ok {
		return u.InputTokens, u.OutputTokens, u.TotalTokens, u.NumTurns, u.NumToolCalls, u.CostUSD
	}
	return 0, 0, 0, 0, 0, 0
}

// aggregateInstance fills an instance's span and token totals from its steps:
// StartedAt = earliest step start, FinishedAt = latest step finish, tokens/cost
// summed. This lets a workflow report its true wall-clock span and spend rather
// than inheriting only the last execution's numbers.
func aggregateInstance(item *WorkflowInstanceItem) {
	for _, s := range item.Steps {
		if s.StartedAt != nil && (item.StartedAt == nil || s.StartedAt.Before(*item.StartedAt)) {
			item.StartedAt = s.StartedAt
		}
		if s.FinishedAt != nil && (item.FinishedAt == nil || s.FinishedAt.After(*item.FinishedAt)) {
			item.FinishedAt = s.FinishedAt
		}
		item.InputTokens += s.InputTokens
		item.OutputTokens += s.OutputTokens
		item.TotalTokens += s.TotalTokens
		item.CacheCreationTokens += s.CacheCreationTokens
		item.CacheReadTokens += s.CacheReadTokens
		item.CostUSD += s.CostUSD
	}
}

// mapCIPolls converts recorded CI poll rows into dashboard view-models.
func mapCIPolls(checks []db.CIPollCheck) []CIPollItem {
	out := make([]CIPollItem, 0, len(checks))
	for _, c := range checks {
		out = append(out, CIPollItem{
			StepID:    c.StepID,
			Status:    c.Status,
			PRURL:     c.PRURL,
			Detail:    c.Detail,
			CheckedAt: c.CheckedAt,
		})
	}
	return out
}

// refreshWorkflowMonitor re-fetches a workflow instance and its steps for the
// live monitor, updating states in-place (without resetting the step cursor).
func (a *App) refreshWorkflowMonitor(instanceID, cellID string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		if dbConn == nil {
			return nil
		}
		inst, err := dbConn.GetWorkflowInstance(ctx, instanceID)
		if err != nil || inst == nil {
			return nil
		}
		// Preserve the step cursor — return a monitor refresh, not a full reset.
		return workflowMonitorRefreshMsg{instance: buildWorkflowInstanceItem(ctx, dbConn, inst)}
	}
}

// workflowMonitorRefreshMsg carries a live-refresh of the instance (no cursor reset).
type workflowMonitorRefreshMsg struct{ instance *WorkflowInstanceItem }

// fetchWorkflowStepLogs fetches task logs scoped to a specific step's time window.
// open marks a user-initiated panel open; a live-tail refresh passes false so the
// handler swaps data without resetting the scroll position.
func (a *App) fetchWorkflowStepLogs(cellID, stepID string, from, to *time.Time, open bool) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		var logs []LogEntry
		if dbConn != nil {
			if rows, err := dbConn.GetTaskLogsInRange(ctx, cellID, from, to); err == nil {
				for _, l := range rows {
					logs = append(logs, LogEntry{
						Timestamp: l.Timestamp,
						Level:     l.Level,
						Message:   l.Message,
					})
				}
			}
		}
		return workflowStepLogsMsg{stepID: stepID, open: open, logs: logs}
	}
}

// fetchWorkflowsConfig fetches workflow definitions from the local config (no IPC needed).
func (a *App) fetchWorkflowsConfig() tea.Cmd {
	cfg := a.cfg
	return func() tea.Msg {
		var items []WorkflowConfigItem
		if cfg != nil {
			for _, wf := range cfg.Workflows {
				item := WorkflowConfigItem{
					ID:          wf.ID,
					Description: wf.Description,
				}
				for _, step := range wf.Steps {
					stype := step.Type
					if stype == "" {
						stype = "agent"
					}
					item.Steps = append(item.Steps, WorkflowStepDef{
						ID:        step.ID,
						Type:      stype,
						Agent:     step.Agent,
						Condition: step.Condition,
						Prompt:    step.Prompt,
					})
				}
				items = append(items, item)
			}
		}
		return workflowsConfigMsg{workflows: items}
	}
}

// wfStepLogLines returns the log lines for the currently-selected workflow step.
func (a *App) wfStepLogLines() []string {
	t := a.model.tasksTab
	if t == nil {
		return nil
	}
	return a.logEntryLines(t.WorkflowLogs)
}

// updateAgentConfigCmd sends a PATCH /api/config/agent/{id} request to the
// daemon via the IPC socket to hot-reload model, runner, or max_workers.
// Falls back to direct file modification if the socket is unreachable.
// Always updates the local in-memory config so re-fetches reflect the change.
func (a *App) updateAgentConfigCmd(agentID, model, runner string, maxWorkers int) tea.Cmd {
	return func() tea.Msg {
		diff := config.AgentDiff{ID: agentID, Model: model, Runner: runner, MaxWorkers: maxWorkers}

		// 1. Try socket (dispatcher running)
		socketOK := a.patchAgentViaSocket(agentID, model, runner, maxWorkers) == nil

		// 2. Update local in-memory config (so re-fetches show the change)
		if a.cfg != nil {
			if !socketOK {
				// Socket failed — persist directly to file
				paths := []string{"apiary.yaml", ".apiary/apiary.yaml"}
				for _, p := range paths {
					if _, err := os.Stat(p); err == nil {
						_ = a.cfg.ApplyAgentDiff(p, diff)
						break
					}
				}
			} else {
				// Socket succeeded — still update local config in memory
				a.cfg.ApplyAgentDiff("", diff)
			}
		}
		return nil
	}
}

func (a *App) patchAgentViaSocket(agentID, model, runner string, maxWorkers int) error {
	body := map[string]any{}
	if model != "" {
		body["model"] = model
	}
	if runner != "" {
		body["runner"] = runner
	}
	if maxWorkers > 0 {
		body["max_workers"] = maxWorkers
	}
	payload, _ := json.Marshal(body)
	url := fmt.Sprintf("http://apiary/api/config/agent/%s", agentID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Apiary-Control", daemon.ReadControlToken(a.dataDir))
	resp, err := a.ipcClient(5 * time.Second).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("socket returned %d", resp.StatusCode)
	}
	return nil
}

// openURLCmd opens a URL in the user's default browser without blocking the UI.
func openURLCmd(target string) tea.Cmd {
	return func() tea.Msg {
		_ = openInBrowser(target)
		return nil
	}
}

// openInBrowser launches the OS default handler for a URL.
func openInBrowser(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{target}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		name, args = "xdg-open", []string{target}
	}
	return exec.Command(name, args...).Start()
}

// ── commands (run in goroutines; must NOT touch a.model) ─────────────────────

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// spinnerInterval drives the loading animation. It is far faster than the data
// refresh tick so the spinner reads as smooth motion while a query is in flight.
const spinnerInterval = 100 * time.Millisecond

// spinnerFrames are the heavy braille "orbit" cycle used by every loading
// indicator. The filled cells (vs. a sparse dot pattern) give the rotation
// strong contrast so the motion reads clearly across terminal fonts.
var spinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

// loadingLine renders the current spinner frame followed by a muted label, used
// in place of an "empty" message while a view is waiting on its first query.
func (a *App) loadingLine(label string) string {
	frame := spinnerFrames[a.model.spinnerFrame%len(spinnerFrames)]
	return StyleWarning.Render(frame) + " " + StyleMuted.Render(label)
}

// fetchActiveTab returns the query command for whichever tab is active. It is
// always called from Update (event loop), so reading the active tab is safe.
func (a *App) fetchActiveTab() tea.Cmd {
	switch a.model.ActiveTab() {
	case "Overview":
		return a.fetchOverview()
	case "Tasks":
		// Sub-views live-refresh by id (not by list index), so the open Detail/Logs
		// stay current without re-sorting History under the cursor — which would make
		// the SelectedIdx-keyed reload (r/d/l) target a different task than the one on
		// screen. The list itself is only re-queried while it (not a sub-view) shows.
		if t := a.model.tasksTab; t != nil {
			switch t.View {
			case TaskViewWorkflow:
				if inst := t.WorkflowInstance; inst != nil {
					cmds := []tea.Cmd{a.refreshWorkflowMonitor(inst.ID, inst.CellID)}
					// Live-tail the open step-log panel (throttled like the logs view).
					if t.WorkflowShowLogs && t.WorkflowLogStepID != "" &&
						t.WorkflowStepIdx < len(inst.Steps) && a.model.tickCount%2 == 0 {
						step := inst.Steps[t.WorkflowStepIdx]
						if step.StepID == t.WorkflowLogStepID {
							cmds = append(cmds, a.fetchWorkflowStepLogs(inst.CellID, step.StepID, step.StartedAt, step.FinishedAt, false))
						}
					}
					return tea.Batch(cmds...)
				}
				return nil
			case TaskViewTranscript:
				// Live-tail the open transcript file (throttled like the logs view).
				if t.TranscriptPath != "" && a.model.tickCount%2 == 0 {
					return a.loadTranscriptCmd(t.TranscriptPath)
				}
				return nil
			case TaskViewDetail:
				if t.Detail != nil {
					return a.refreshTaskDetail(t.Detail.TaskID, t.Detail.InternalTaskID)
				}
				return nil
			case TaskViewLogs:
				// The history query is heavier — throttle it to every other tick (~4s).
				if a.model.tickCount%2 != 0 {
					return nil
				}
				if t.LogInternalTaskID != "" {
					return a.refreshTaskHistory(t.LogInternalTaskID, t.LogDrillKey)
				}
				if t.LogTaskID != "" {
					return a.tailTaskLogs(t.LogTaskID, t.LogNewestID)
				}
				return nil
			}
		}
		return a.fetchTasks()
	case "Agents":
		// Live-tail the open task-log drill-down (throttled like the Tasks logs
		// view); the agent list itself only re-queries while it is on screen.
		if ag := a.model.agentsTab; ag != nil && ag.View == AgentViewTaskLogs {
			if ag.LogsTaskID != "" && a.model.tickCount%2 == 0 {
				return a.fetchAgentTaskLogs(ag.LogsTaskID)
			}
			return nil
		}
		return a.fetchAgents()
	case "Usage":
		return a.fetchUsage()
	case "Logs":
		return a.fetchLogs()
	case "Workflows":
		return a.fetchWorkflowsConfig()
	}
	return nil
}

func (a *App) fetchOverview() tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		data := OverviewTab{Status: "Unknown", Concurrency: 4}

		// Enrich agent breakdown from config + running stats
		if a.cfg != nil {
			for _, ac := range a.cfg.Agents {
				mw := ac.MaxWorkers
				if mw < 1 {
					mw = 1
				}
				data.AgentBreakdown = append(data.AgentBreakdown, AgentCount{ID: ac.ID, MaxWorkers: mw})
			}
		}

		if dbConn != nil {
			if stats, err := dbConn.GetDashboardStats(ctx, time.Now().AddDate(0, 0, -1)); err == nil && stats != nil {
				data.Status = stats.DispatcherStatus
				data.ActiveAgents = stats.ActiveAgents
				data.ActiveRuns = stats.ActiveRuns
				data.QueuedTasks = stats.QueuedTasks
				data.CompletedToday = stats.CompletedToday
				data.FailedToday = stats.FailedToday
				data.AvgDuration = fmt.Sprintf("%.1fs", float64(stats.AvgDurationMs)/1000)
				data.SuccessRate = fmt.Sprintf("%.1f%%", stats.SuccessRate*100)
				data.TodayCostUSD = stats.TodayCostUSD
				data.TodayTokens = stats.TodayTokens
				data.TodayInputTokens = stats.TodayInputTokens
				data.TodayOutputTokens = stats.TodayOutputTokens
				if stats.CompletedToday > 0 {
					data.ThroughputRatio = fmt.Sprintf("%.1f", float64(stats.CompletedToday)/24)
				} else {
					data.ThroughputRatio = "0.0"
				}
			}
			// Fill agent running counts from agent stats
			if agentRows, err := dbConn.GetAgentStats(ctx); err == nil {
				runMap := map[string]int{}
				for _, ag := range agentRows {
					runMap[ag.ID] = ag.RunningCount
				}
				for i := range data.AgentBreakdown {
					data.AgentBreakdown[i].Running = runMap[data.AgentBreakdown[i].ID]
				}
			}
		}
		return overviewDataMsg{data: data}
	}
}

func (a *App) fetchTasks() tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		items := make([]TaskItem, 0)
		if dbConn != nil {
			// Phase 9: the Tasks tab is keyed on the canonical InternalTask, not
			// per-execution rows. Each row carries the internal id (for lineage /
			// bindings / instances) plus a drill key (the primary binding's source
			// item id, or the task id when binding-less) that the legacy
			// detail/logs/monitor machinery resolves.
			if tasks, err := dbConn.InternalTasks().ListTasks(ctx, 100); err == nil {
				for _, tk := range tasks {
					bindings, _ := dbConn.ListBindingsByTask(ctx, tk.ID)
					prs, _ := dbConn.ListTaskPullRequests(ctx, tk.ID)
					items = append(items, taskItemFromInternal(tk, bindings, prs))
				}
			}
		}
		return tasksDataMsg{items: items}
	}
}

// taskItemFromInternal converts an InternalTask (plus its source bindings) into a
// dashboard Tasks-tab row. Execution-scoped fields (Agent/Model/tokens/duration)
// are left zero here and hydrated on drill-down; StartedAt/CompletedAt mirror the
// task timestamps so the list's "when" column and time sort stay meaningful.
func taskItemFromInternal(t model.InternalTask, bindings []model.SourceBinding, prs []db.TaskPullRequest) TaskItem {
	created := t.CreatedAt
	item := TaskItem{
		TaskID:               drillKeyFor(t, bindings), // legacy machinery key (== DrillKey)
		Title:                t.Title,
		Status:               string(t.State),
		StartedAt:            &created,
		InternalTaskID:       t.ID,
		DrillKey:             drillKeyFor(t, bindings),
		ParentTaskID:         t.ParentTaskID,
		OutstandingWorkflows: t.OutstandingWorkflows,
		Bindings:             mapBindings(bindings),
		PullRequests:         mapPullRequests(prs),
	}
	if t.State == model.TaskStateDone || t.State == model.TaskStateFailed {
		updated := t.UpdatedAt
		item.CompletedAt = &updated
	}
	if len(bindings) > 0 {
		item.Number = bindings[0].SourceItemNumber
		item.URL = bindings[0].SourceItemURL
	}
	return item
}

// drillKeyFor returns the key the legacy task_executions/task_logs/workflow_instances
// (by cell_id) machinery uses for a task: the primary binding's source item id when
// bound, otherwise the task's own id (which the engine uses as the cell id for
// binding-less spawned tasks).
func drillKeyFor(t model.InternalTask, bindings []model.SourceBinding) string {
	if len(bindings) > 0 {
		return bindings[0].SourceItemID
	}
	return t.ID
}

// mapBindings converts model.SourceBinding rows into dashboard view-models.
func mapBindings(bindings []model.SourceBinding) []SourceBindingItem {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]SourceBindingItem, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, SourceBindingItem{
			SourceID:   b.SourceID,
			ItemNumber: b.SourceItemNumber,
			ItemURL:    b.SourceItemURL,
			ItemID:     b.SourceItemID,
		})
	}
	return out
}

// mapPullRequests converts persisted PR rows (oldest first) to their view items.
func mapPullRequests(prs []db.TaskPullRequest) []PullRequestItem {
	if len(prs) == 0 {
		return nil
	}
	out := make([]PullRequestItem, 0, len(prs))
	for _, p := range prs {
		out = append(out, PullRequestItem{Number: p.PRNumber, URL: p.PRURL, State: p.PRState})
	}
	return out
}

// taskItemFromHistory converts a DB history row into a dashboard TaskItem.
func taskItemFromHistory(r db.TaskHistoryItem) TaskItem {
	return TaskItem{
		TaskID:       r.TaskID,
		Number:       r.Number,
		URL:          r.URL,
		Title:        r.Title,
		Agent:        r.AgentID,
		Model:        r.Model,
		Runner:       r.Runner,
		Status:       r.Status,
		Attempt:      r.Attempt,
		Duration:     time.Duration(r.DurationMs) * time.Millisecond,
		StartedAt:    r.StartedAt,
		CompletedAt:  r.CompletedAt,
		Error:        r.Error,
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
		TotalTokens:  r.TotalTokens,
		NumTurns:     r.NumTurns,
		NumToolCalls: r.NumToolCalls,
		CostUSD:      r.CostUSD,
	}
}

// fetchAgentActivity loads the recent tasks handled by an agent.
func (a *App) fetchAgentActivity(agentID string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		items := make([]TaskItem, 0)
		if dbConn != nil {
			if rows, err := dbConn.GetTasksByAgent(ctx, agentID, 200); err == nil {
				for _, r := range rows {
					items = append(items, taskItemFromHistory(r))
				}
			}
		}
		return agentActivityMsg{agentID: agentID, items: items}
	}
}

// fetchAgentTaskLogs loads the per-task logs for a task selected in the agent
// activity list (reuses the same source as the Tasks tab logs view).
func (a *App) fetchAgentTaskLogs(taskID string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		var detail *TaskItem
		logs := make([]LogEntry, 0)
		if dbConn != nil {
			if r, err := dbConn.GetTaskDetail(ctx, taskID); err == nil && r != nil {
				detail = &TaskItem{
					TaskID:       r.TaskID,
					Number:       r.Number,
					URL:          r.URL,
					Title:        r.Title,
					Agent:        r.AgentID,
					Model:        r.Model,
					Runner:       r.Runner,
					Status:       r.Status,
					Attempt:      r.Attempt,
					Duration:     time.Duration(r.DurationMs) * time.Millisecond,
					StartedAt:    r.StartedAt,
					CompletedAt:  r.CompletedAt,
					Error:        r.Error,
					InputTokens:  r.InputTokens,
					OutputTokens: r.OutputTokens,
					TotalTokens:  r.TotalTokens,
					NumTurns:     r.NumTurns,
					NumToolCalls: r.NumToolCalls,
					CostUSD:      r.CostUSD,
				}
			}
			if rows, err := dbConn.GetTaskLogs(ctx, taskID, taskLogTailLimit); err == nil {
				for _, l := range rows {
					logs = append(logs, LogEntry{
						Timestamp: l.Timestamp,
						Level:     l.Level,
						Message:   l.Message,
					})
				}
			}
		}
		return agentTaskLogsMsg{taskID: taskID, logs: logs, detail: detail}
	}
}

// fetchTaskDetail loads the detail panel for a task. drillKey is the legacy
// cell id used by the execution/logs/workflow-instance machinery; internalTaskID
// is the canonical InternalTask id used to augment the panel with source
// bindings, lineage (ancestors + children), and the full list of workflow
// instances. internalTaskID may be empty for legacy/agent-drilled rows.
func (a *App) fetchTaskDetail(drillKey, internalTaskID string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		detail, instance := a.loadTaskDetail(ctx, dbConn, drillKey, internalTaskID)
		return taskDetailMsg{taskID: drillKey, detail: detail, instance: instance}
	}
}

// loadTaskDetail builds the detail panel (the execution row, augmented with
// InternalTask bindings/lineage/instances) plus its workflow instance. Shared by
// the initial open (fetchTaskDetail) and the live refresh (refreshTaskDetail).
func (a *App) loadTaskDetail(ctx context.Context, dbConn *db.Client, drillKey, internalTaskID string) (*TaskItem, *WorkflowInstanceItem) {
	var detail *TaskItem
	if dbConn != nil {
		if r, err := dbConn.GetTaskDetail(ctx, drillKey); err == nil && r != nil {
			detail = &TaskItem{
				TaskID:       r.TaskID,
				Number:       r.Number,
				URL:          r.URL,
				Title:        r.Title,
				Agent:        r.AgentID,
				Model:        r.Model,
				Runner:       r.Runner,
				Status:       r.Status,
				Attempt:      r.Attempt,
				Duration:     time.Duration(r.DurationMs) * time.Millisecond,
				StartedAt:    r.StartedAt,
				CompletedAt:  r.CompletedAt,
				Error:        r.Error,
				InputTokens:  r.InputTokens,
				OutputTokens: r.OutputTokens,
				TotalTokens:  r.TotalTokens,
				NumTurns:     r.NumTurns,
				NumToolCalls: r.NumToolCalls,
				CostUSD:      r.CostUSD,
			}
		}
		if internalTaskID != "" {
			detail = a.augmentTaskDetail(ctx, dbConn, detail, drillKey, internalTaskID)
		}
	}
	return detail, a.fetchWorkflowInstance(ctx, dbConn, drillKey)
}

// refreshTaskDetail re-loads the open detail panel by id (not by list index), so
// status/tokens/instances stay live without re-sorting the list under the cursor.
func (a *App) refreshTaskDetail(drillKey, internalTaskID string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		detail, instance := a.loadTaskDetail(ctx, dbConn, drillKey, internalTaskID)
		return taskDetailRefreshMsg{detail: detail, instance: instance}
	}
}

// augmentTaskDetail enriches a task detail with InternalTask data: parent/
// outstanding counters, source bindings, the lineage breadcrumb (ancestors,
// root-first), spawned children, and every workflow instance bound to the task.
// When the task has no execution row yet (e.g. a registered task that never ran)
// it synthesizes a minimal detail from the InternalTask so the panel still
// renders. Returns the (possibly newly-created) detail, or the input unchanged
// when the InternalTask is gone.
func (a *App) augmentTaskDetail(ctx context.Context, dbConn *db.Client, detail *TaskItem, drillKey, internalTaskID string) *TaskItem {
	tk, err := dbConn.InternalTasks().GetTask(ctx, internalTaskID)
	if err != nil || tk == nil {
		return detail
	}
	if detail == nil {
		created := tk.CreatedAt
		detail = &TaskItem{
			TaskID:    drillKey,
			Title:     tk.Title,
			Status:    string(tk.State),
			StartedAt: &created,
		}
	}
	detail.InternalTaskID = tk.ID
	detail.DrillKey = drillKey
	detail.ParentTaskID = tk.ParentTaskID
	detail.OutstandingWorkflows = tk.OutstandingWorkflows
	// Show the InternalTask lifecycle state so the detail Status matches the list
	// (Phase 9's primary unit). Per-execution/instance outcomes remain visible in
	// the Workflow Instances section below.
	detail.Status = string(tk.State)

	if b, _ := dbConn.ListBindingsByTask(ctx, internalTaskID); len(b) > 0 {
		detail.Bindings = mapBindings(b)
	}
	if anc, _ := dbConn.InternalTasks().GetTaskAncestors(ctx, internalTaskID); len(anc) > 0 {
		detail.Lineage = mapLineage(ctx, dbConn, anc)
		if len(anc) >= 2 { // the node just before self (self is last) is the parent
			detail.ParentTitle = anc[len(anc)-2].Title
		}
	}
	if kids, _ := dbConn.InternalTasks().ListChildTasks(ctx, internalTaskID); len(kids) > 0 {
		detail.Children = mapLineage(ctx, dbConn, kids)
	}
	if insts, _ := dbConn.ListWorkflowInstancesByTask(ctx, internalTaskID); len(insts) > 0 {
		detail.Instances = mapInstances(ctx, dbConn, insts)
	}
	detail.Events, _ = dbConn.ListExecutionEvents(ctx, db.ExecutionEventFilter{TaskID: internalTaskID, Limit: 200})
	// PRs are persisted by the daemon (refreshed on detail open); read them back so
	// the (p) shortcut and the detail panel stay in sync. Re-runs of this function
	// from the live tick pick up freshly-discovered PRs automatically.
	if prs, _ := dbConn.ListTaskPullRequests(ctx, internalTaskID); len(prs) > 0 {
		detail.PullRequests = mapPullRequests(prs)
	}
	return detail
}

// mapLineage converts InternalTask rows into lineage nodes, enriching each with
// whether it has a source binding and how many workflow instances it has (bounded
// fan-out per node, run in the background fetch command).
func mapLineage(ctx context.Context, dbConn *db.Client, tasks []model.InternalTask) []TaskLineageItem {
	out := make([]TaskLineageItem, 0, len(tasks))
	for _, t := range tasks {
		node := TaskLineageItem{TaskID: t.ID, Title: t.Title, State: string(t.State)}
		if b, _ := dbConn.ListBindingsByTask(ctx, t.ID); len(b) > 0 {
			node.HasBinding = true
		}
		if insts, _ := dbConn.ListWorkflowInstancesByTask(ctx, t.ID); len(insts) > 0 {
			node.InstanceCount = len(insts)
		}
		out = append(out, node)
	}
	return out
}

// mapInstances converts db workflow-instance rows into dashboard view-models,
// enriching each with its span and token totals (aggregated from its step runs)
// so the Workflow Instances list can show per-workflow start/end and spend, and
// the task header can roll them up across every instance.
func mapInstances(ctx context.Context, dbConn *db.Client, insts []db.WorkflowInstance) []WorkflowInstanceItem {
	out := make([]WorkflowInstanceItem, 0, len(insts))
	for _, in := range insts {
		item := WorkflowInstanceItem{
			ID:               in.ID,
			Workflow:         in.WorkflowID,
			State:            in.State,
			CellID:           in.CellID,
			ParentInstanceID: in.ParentInstanceID,
			ResumedFrom:      in.ResumedFrom,
			CreatedAt:        in.CreatedAt,
		}
		if steps, err := dbConn.ListStepRuns(ctx, in.ID); err == nil {
			usage := loadStepUsageFallback(ctx, dbConn, in.ID, steps)
			for _, s := range steps {
				si := WorkflowStepItem{StartedAt: s.StartedAt, FinishedAt: s.FinishedAt}
				si.InputTokens, si.OutputTokens, si.TotalTokens, si.NumTurns, si.NumToolCalls, si.CostUSD =
					stepUsageFromMap(s, usage)
				item.Steps = append(item.Steps, si)
			}
			aggregateInstance(&item)
			item.Steps = nil // the list view shows only the rollup, not the rows
		}
		out = append(out, item)
	}
	return out
}

// fetchWorkflowInstance loads the workflow instance (and its steps) bound to a
// task, returning nil when the task ran through the legacy single-shot path.
func (a *App) fetchWorkflowInstance(ctx context.Context, dbConn *db.Client, taskID string) *WorkflowInstanceItem {
	if dbConn == nil {
		return nil
	}
	inst, err := dbConn.GetLatestInstanceByCell(ctx, taskID)
	if err != nil || inst == nil {
		return nil
	}
	return buildWorkflowInstanceItem(ctx, dbConn, inst)
}

// wfStepDuration renders a short duration for a step run: final span for a
// finished step, live elapsed for a running one, "—" when not yet started.
func wfStepDuration(s db.StepRun, now time.Time) string {
	if s.StartedAt == nil {
		return "—"
	}
	end := now
	if s.FinishedAt != nil {
		end = *s.FinishedAt
	}
	d := end.Sub(*s.StartedAt).Round(time.Second)
	if d < 0 {
		return "—"
	}
	return d.String()
}

func (a *App) fetchTaskLogs(taskID string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		var detail *TaskItem
		logs := make([]LogEntry, 0)
		var oldestID, newestID int64
		var hasMore bool
		if dbConn != nil {
			detail = taskDetailItem(ctx, dbConn, taskID)
			if rows, err := dbConn.GetTaskLogs(ctx, taskID, taskLogTailLimit); err == nil {
				logs = mapLogLines(rows)
				if len(rows) > 0 {
					oldestID = rows[0].ID           // chronological: first row is the oldest loaded
					newestID = rows[len(rows)-1].ID // …and the last is the newest (live-tail cursor)
				}
				hasMore = len(rows) == taskLogTailLimit // a full page implies older rows exist
			}
		}
		return taskLogsMsg{taskID: taskID, logs: logs, detail: detail, oldestID: oldestID, newestID: newestID, hasMore: hasMore}
	}
}

// tailTaskLogs fetches flat-log lines newer than afterID for the live logs view,
// so a running task's logs stream in without resetting the scroll position.
func (a *App) tailTaskLogs(taskID string, afterID int64) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		msg := tailTaskLogsMsg{taskID: taskID, newestID: afterID}
		if dbConn != nil {
			if rows, err := dbConn.GetTaskLogsAfter(ctx, taskID, afterID, taskLogTailLimit); err == nil && len(rows) > 0 {
				msg.logs = mapLogLines(rows)
				msg.newestID = rows[len(rows)-1].ID
			}
		}
		return msg
	}
}

// loadOlderLogsCmd starts a lazy fetch of the next older page of flat task logs
// when the logs view is scrolled to the top and an older page may exist. Returns
// nil when there is nothing to load (history view, no cursor, already loading, or
// no more pages).
func (a *App) loadOlderLogsCmd(t *TasksTab) tea.Cmd {
	if t.View != TaskViewLogs || len(t.InstanceHistory) > 0 ||
		t.LogTaskID == "" || !t.LogHasMore || t.LogLoadingMore {
		return nil
	}
	t.LogLoadingMore = true
	return a.fetchOlderTaskLogs(t.LogTaskID, t.LogOldestID)
}

// fetchOlderTaskLogs lazily loads the page of flat task logs immediately older
// than beforeID, for the scroll-to-top tail-pagination of the logs view.
func (a *App) fetchOlderTaskLogs(taskID string, beforeID int64) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		msg := olderTaskLogsMsg{taskID: taskID}
		if dbConn != nil {
			if rows, err := dbConn.GetTaskLogsBefore(ctx, taskID, beforeID, taskLogTailLimit); err == nil {
				msg.logs = mapLogLines(rows)
				if len(rows) > 0 {
					msg.oldestID = rows[0].ID
				}
				msg.hasMore = len(rows) == taskLogTailLimit
			}
		}
		return msg
	}
}

// fetchTaskHistory loads the full per-instance history for the repurposed logs
// view: every workflow instance bound to the InternalTask (e.g. investigator →
// implementation), each with its steps and the logs scoped to its time window.
// internalTaskID keys the history; drillKey hydrates the detail header. Falls back
// to the flat log stream when there is no InternalTask id (legacy/pre-Phase-9 rows
// or the Agents-tab drill-down).
func (a *App) fetchTaskHistory(internalTaskID, drillKey string) tea.Cmd {
	if internalTaskID == "" {
		return a.fetchTaskLogs(drillKey)
	}
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		detail, segments := a.buildTaskHistory(ctx, dbConn, internalTaskID, drillKey)
		return taskHistoryMsg{taskID: internalTaskID, drillKey: drillKey, segments: segments, detail: detail}
	}
}

// buildTaskHistory assembles the per-instance history (each workflow instance with
// its steps, CI polls, and time-scoped logs) plus the detail header. Shared by the
// initial open (fetchTaskHistory) and the live refresh (refreshTaskHistory).
func (a *App) buildTaskHistory(ctx context.Context, dbConn *db.Client, internalTaskID, drillKey string) (*TaskItem, []TaskHistorySegmentItem) {
	var detail *TaskItem
	segments := make([]TaskHistorySegmentItem, 0)
	if dbConn != nil {
		detail = taskDetailItem(ctx, dbConn, drillKey)
		if segs, err := dbConn.GetTaskWorkflowHistory(ctx, internalTaskID); err == nil {
			now := time.Now()
			for _, seg := range segs {
				item := mapInstances(ctx, dbConn, []db.WorkflowInstance{seg.Instance})[0]
				item.Steps = mapStepRuns(seg.Steps, now)
				if polls, err := dbConn.ListCIPollChecks(ctx, seg.Instance.ID); err == nil {
					item.CIPolls = mapCIPolls(polls)
				}
				if item.State == db.InstanceStateApprovalWaiting {
					item.Message = "Awaiting human approval — reply on the task to resume or abort."
				}
				segments = append(segments, TaskHistorySegmentItem{
					Instance: item,
					Logs:     mapLogLines(seg.Logs),
				})
			}
		}
	}
	return detail, segments
}

// refreshTaskHistory re-builds the open per-instance history view by id, so a
// running task's steps and logs stay live. The handler preserves scroll.
func (a *App) refreshTaskHistory(internalTaskID, drillKey string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		detail, segments := a.buildTaskHistory(ctx, dbConn, internalTaskID, drillKey)
		return taskHistoryRefreshMsg{segments: segments, detail: detail}
	}
}

// taskDetailItem builds the detail-header TaskItem from the latest execution for a
// (drill) cell id, or nil when none exists. Shared by the logs and history fetches.
func taskDetailItem(ctx context.Context, dbConn *db.Client, taskID string) *TaskItem {
	r, err := dbConn.GetTaskDetail(ctx, taskID)
	if err != nil || r == nil {
		return nil
	}
	return &TaskItem{
		TaskID:       r.TaskID,
		Number:       r.Number,
		URL:          r.URL,
		Title:        r.Title,
		Agent:        r.AgentID,
		Model:        r.Model,
		Runner:       r.Runner,
		Status:       r.Status,
		Attempt:      r.Attempt,
		Duration:     time.Duration(r.DurationMs) * time.Millisecond,
		StartedAt:    r.StartedAt,
		CompletedAt:  r.CompletedAt,
		Error:        r.Error,
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
		TotalTokens:  r.TotalTokens,
		NumTurns:     r.NumTurns,
		NumToolCalls: r.NumToolCalls,
		CostUSD:      r.CostUSD,
	}
}

// mapLogLines converts db log rows into dashboard log entries.
func mapLogLines(rows []db.TaskLogLine) []LogEntry {
	out := make([]LogEntry, 0, len(rows))
	for _, l := range rows {
		out = append(out, LogEntry{Timestamp: l.Timestamp, Level: l.Level, Message: l.Message})
	}
	return out
}

// mapStepRuns converts db step-run rows into dashboard step items.
func mapStepRuns(steps []db.StepRun, now time.Time) []WorkflowStepItem {
	out := make([]WorkflowStepItem, 0, len(steps))
	for _, s := range steps {
		out = append(out, WorkflowStepItem{
			StepID:              s.StepID,
			Agent:               s.AgentID,
			State:               s.State,
			Duration:            wfStepDuration(s, now),
			Cached:              s.SkippedCached,
			Output:              s.Output,
			Summary:             s.Summary,
			InputTokens:         s.InputTokens,
			OutputTokens:        s.OutputTokens,
			TotalTokens:         s.TotalTokens,
			CacheCreationTokens: s.CacheCreationTokens,
			CacheReadTokens:     s.CacheReadTokens,
			CostUSD:             s.CostUSD,
			NumTurns:            s.NumTurns,
			NumToolCalls:        s.NumToolCalls,
			StartedAt:           s.StartedAt,
			FinishedAt:          s.FinishedAt,
		})
	}
	return out
}

func (a *App) fetchAgents() tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		// Index DB stats by agent ID.
		dbStats := map[string]db.AgentStats{}
		if dbConn != nil {
			if rows, err := dbConn.GetAgentStats(ctx); err == nil {
				for _, ag := range rows {
					dbStats[ag.ID] = ag
				}
			}
		}

		// Build list from config so every configured agent appears, even with no runs.
		agents := make([]AgentStatus, 0)
		if a.cfg != nil {
			for _, ac := range a.cfg.Agents {
				mw := ac.MaxWorkers
				if mw < 1 {
					mw = 1
				}
				s := AgentStatus{
					ID:          ac.ID,
					Status:      "idle",
					MaxWorkers:  mw,
					RunnerType:  ac.Runner,
					Model:       ac.Model,
					SoulFile:    ac.SoulFile,
					Skills:      ac.Skills,
					Description: ac.Description,
					SourceName:  ac.SourceName,
					SourceEmail: ac.SourceEmail,
				}
				// Merge DB stats if this agent has any runs.
				if ag, ok := dbStats[ac.ID]; ok {
					s.Status = ag.Status
					s.RunningCount = ag.RunningCount
					s.CurrentTask = ag.CurrentTask
					s.QueuedCount = ag.QueuedCount
					s.CompletedCount = ag.CompletedCount
					s.AvgDurationMs = ag.AvgDurationMs
					s.SuccessRate = ag.SuccessRate
					s.LastTaskEndedAt = ag.LastTaskEndedAt
					s.PID = ag.PID
					s.HeartbeatAt = ag.HeartbeatAt
					s.HeartbeatCount = ag.HeartbeatCount
					s.TotalCostUSD = ag.TotalCostUSD
					s.TotalTokens = ag.TotalTokens
				}
				// Collect all runner IDs and models for the current runner.
				s.Runners = make([]string, 0, len(a.cfg.Runners))
				for _, rc := range a.cfg.Runners {
					s.Runners = append(s.Runners, rc.ID)
					if rc.ID == s.RunnerType || (s.RunnerType == "" && rc.ID == a.cfg.DefaultRunner) {
						s.RunnerModels = rc.Models
					}
				}
				agents = append(agents, s)
			}
		}
		return agentsDataMsg{agents: agents}
	}
}

func (a *App) fetchLogs() tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		logs := make([]LogEntry, 0)
		if dbConn != nil {
			if rows, err := dbConn.GetRecentLogs(ctx, 500); err == nil {
				for _, l := range rows {
					logs = append(logs, LogEntry{
						Timestamp: l.Timestamp,
						Level:     l.Level,
						Component: l.Component,
						Message:   l.Message,
					})
				}
			}
		}
		return logsDataMsg{logs: logs}
	}
}

func (a *App) fetchUsage() tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		data := UsageTab{}
		if dbConn != nil {
			if rows, err := dbConn.GetDailyUsage(ctx, 14); err == nil {
				data.Daily = rows
			}
			if rows, err := dbConn.GetAgentUsage(ctx); err == nil {
				data.Agents = rows
			}
		}
		return usageDataMsg{data: data}
	}
}

// ── view ─────────────────────────────────────────────────────────────────────

// View renders the dashboard.
func (a *App) View() string {
	if a == nil || a.model == nil {
		return "Error: Dashboard not initialized"
	}
	if a.model.width == 0 || a.model.height == 0 {
		return "Loading..."
	}

	tabs := a.renderTabs()
	tabsHeight := lipgloss.Height(tabs)

	footer := a.renderFooter()
	footerHeight := lipgloss.Height(footer)

	contentHeight := a.model.height - tabsHeight - footerHeight - 1
	if contentHeight < 3 {
		contentHeight = 3
	}

	var content string
	switch a.model.ActiveTab() {
	case "Overview":
		content = a.renderOverviewTab(contentHeight)
	case "Tasks":
		content = a.renderTasksTab(contentHeight)
	case "Agents":
		content = a.renderAgentsTab(contentHeight)
	case "Usage":
		content = a.renderUsageTab(contentHeight)
	case "Logs":
		content = a.renderLogsTab(contentHeight)
	case "Workflows":
		content = a.renderWorkflowsTab(contentHeight)
	default:
		content = a.box("UNKNOWN", "Unknown tab\n", contentHeight)
	}

	view := lipgloss.JoinVertical(lipgloss.Left, tabs, content, footer)
	if a.model.confirmAction != "" {
		view = a.renderConfirmModal(view)
	}
	return view
}

func (a *App) renderConfirmModal(view string) string {
	label := "Restart task"
	msg := "Are you sure you want to restart this task?"
	switch a.model.confirmAction {
	case "clear":
		label = "Clear logs"
		msg = "Are you sure you want to clear all logs for this task?"
	case "stop":
		label = "Stop workflow"
		msg = "Stop this workflow instance? It will be marked interrupted."
	}

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorWarning).
		Padding(1, 3).
		Width(44).
		Render(
			lipgloss.JoinVertical(lipgloss.Center,
				StyleBoxTitle.Render(" "+label+" "),
				"",
				msg,
				"",
				lipgloss.JoinHorizontal(lipgloss.Center,
					StyleFooterKey.Render(" y ")+" "+StyleFooterLbl.Render("yes")+"  "+
						StyleFooterKey.Render(" n ")+" "+StyleFooterLbl.Render("no"),
				),
			),
		)

	dialogHeight := lipgloss.Height(dialog)
	topPad := (a.model.height - dialogHeight) / 2
	if topPad < 0 {
		topPad = 0
	}

	return strings.Repeat("\n", topPad) +
		lipgloss.NewStyle().Width(a.model.width).Align(lipgloss.Center).Render(dialog)
}

func (a *App) renderTabs() string {
	title := StyleHeader.Render(" APIARY ")
	var tabs []string
	for i, tab := range a.model.tabs {
		style := StyleTab
		if i == a.model.activeTab {
			style = StyleActiveTab
		}
		tabs = append(tabs, style.Render(tab))
	}
	row := append([]string{title}, tabs...)
	return lipgloss.JoinHorizontal(lipgloss.Center, row...) + "\n"
}

// box frames inner content in a titled, full 4-sided border that spans the
// terminal width and is exactly `height` lines tall. Each content line is
// padded/truncated (ANSI-aware) so the right border lines up.
func (a *App) box(label, content string, height int) string {
	width := a.model.width
	if width < 24 {
		width = 24
	}
	inner := width - 2 // columns between the two vertical bars
	bodyRows := height - 2
	if bodyRows < 1 {
		bodyRows = 1
	}

	prefixPlain := "┌─ " + label + " "
	dashes := width - lipgloss.Width(prefixPlain) - 1
	if dashes < 0 {
		dashes = 0
	}
	top := StyleBorder.Render("┌─ ") + StyleBoxTitle.Render(label) +
		StyleBorder.Render(" "+strings.Repeat("─", dashes)+"┐")
	bottom := StyleBorder.Render("└" + strings.Repeat("─", width-2) + "┘")
	bar := StyleBorder.Render("│")

	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	var b strings.Builder
	b.WriteString(top + "\n")
	for i := 0; i < bodyRows; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		b.WriteString(bar + fitLine(line, inner) + bar + "\n")
	}
	b.WriteString(bottom + "\n")
	return b.String()
}

// fitLine pads (with spaces) or truncates s to exactly w visible columns,
// preserving ANSI styling. Used to align the right border of a box.
func fitLine(s string, w int) string {
	if w < 0 {
		w = 0
	}
	vis := lipgloss.Width(s)
	switch {
	case vis == w:
		return s
	case vis < w:
		return s + strings.Repeat(" ", w-vis)
	default:
		return ansi.Truncate(s, w, "")
	}
}

func (a *App) renderOverviewTab(height int) string {
	o := a.model.overviewTab
	if o == nil {
		return a.box("OVERVIEW", "no data\n", height)
	}

	status := StyleSuccess.Render("●")
	if a.model.loading {
		status = StyleWarning.Render("⟳")
	}

	var b strings.Builder
	fmt.Fprintf(&b,
		"Status:       %s %s\n"+
			"Concurrency:  %d workers\n"+
			"Agents:       %d active\n"+
			"\n"+
			"Agents:\n",
		status, valueOr(o.Status, "Unknown"),
		o.Concurrency,
		o.ActiveAgents)

	for _, ac := range o.AgentBreakdown {
		label := fmt.Sprintf("  %-16s", ac.ID)
		runStr := fmt.Sprintf("%d/%d", ac.Running, ac.MaxWorkers)
		if ac.Running >= ac.MaxWorkers {
			runStr = StyleWarning.Render(runStr)
		} else if ac.Running > 0 {
			runStr = StyleSuccess.Render(runStr)
		}
		b.WriteString(label + runStr + "\n")
	}

	costStr := "—"
	if o.TodayCostUSD > 0 {
		costStr = fmt.Sprintf("$%.4f", o.TodayCostUSD)
	}
	tokensStr := "—"
	if o.TodayTokens > 0 {
		tokensStr = fmt.Sprintf("%d in / %d out / %d total", o.TodayInputTokens, o.TodayOutputTokens, o.TodayTokens)
	}
	fmt.Fprintf(&b,
		"\nTasks (24h):\n"+
			"  Running:    %d\n"+
			"  Queued:     %d\n"+
			"  Completed:  %s\n"+
			"  Failed:     %s\n"+
			"\n"+
			"Usage (24h):\n"+
			"  Cost:     %s\n"+
			"  Tokens:   %s\n"+
			"\n"+
			"Metrics:\n"+
			"  Throughput:   %s tasks/min\n"+
			"  Avg Duration: %s\n"+
			"  Success Rate: %s\n",
		o.ActiveRuns,
		o.QueuedTasks,
		StyleSuccess.Render(fmt.Sprintf("%d ✓", o.CompletedToday)),
		StyleError.Render(fmt.Sprintf("%d ✗", o.FailedToday)),
		costStr,
		tokensStr,
		valueOr(o.ThroughputRatio, "0.0"),
		valueOr(o.AvgDuration, "0.0s"),
		valueOr(o.SuccessRate, "0.0%"),
	)
	return a.box("OVERVIEW", b.String(), height)
}

func (a *App) renderUsageTab(height int) string {
	u := a.model.usageTab
	if u == nil {
		return a.box("USAGE", StyleMuted.Render("No data yet")+"\n", height)
	}

	innerW := a.model.width - 4
	if innerW < 20 {
		innerW = 20
	}

	var b strings.Builder

	// ── Daily cost bar chart ──────────────────────────────────────────────
	// One bar per day (date as the row label) rather than an interpolated line:
	// daily totals are discrete, and a line between sparse points implies trends
	// that don't exist (e.g. a lone day with tokens looks like gradual growth).
	if len(u.Daily) > 0 {
		items := make([]barItem, len(u.Daily))
		anyCost := false
		for i, d := range u.Daily {
			items[i] = barItem{Label: d.Date, Value: d.CostUSD}
			if d.CostUSD > 0 {
				anyCost = true
			}
		}
		b.WriteString(StyleTableHeader.Render("Daily Cost (USD) — last 14 days") + "\n")
		if anyCost {
			b.WriteString(barChart(items, barOpts{maxWidth: innerW}))
		} else {
			b.WriteString(StyleMuted.Render("  No cost data yet") + "\n")
		}
		b.WriteString("\n")
	}

	// ── Daily tokens bar chart ────────────────────────────────────────────
	if len(u.Daily) > 0 {
		items := make([]barItem, len(u.Daily))
		anyTokens := false
		for i, d := range u.Daily {
			items[i] = barItem{Label: d.Date, Value: float64(d.TotalTokens)}
			if d.TotalTokens > 0 {
				anyTokens = true
			}
		}
		b.WriteString(StyleTableHeader.Render("Daily Tokens — last 14 days") + "\n")
		if anyTokens {
			b.WriteString(barChart(items, barOpts{maxWidth: innerW, valueFmt: formatTokens}))
		} else {
			b.WriteString(StyleMuted.Render("  No token data yet") + "\n")
		}
		b.WriteString("\n")
	}

	// ── Per-agent cost bar chart ──────────────────────────────────────────
	if len(u.Agents) > 0 {
		items := make([]barItem, len(u.Agents))
		for i, a := range u.Agents {
			items[i] = barItem{Label: a.AgentID, Value: a.CostUSD}
		}
		b.WriteString(StyleTableHeader.Render("Cost by Agent (all time)") + "\n")
		b.WriteString(barChart(items, barOpts{maxWidth: innerW, showPct: true, pctOfTotal: true}))
	}

	if b.Len() == 0 {
		b.WriteString(StyleMuted.Render("  No usage data yet. Run some tasks first.") + "\n")
	}

	return a.box("USAGE", b.String(), height)
}

func (a *App) renderTasksTab(height int) string {
	t := a.model.tasksTab
	if t == nil {
		return a.box("TASKS", StyleMuted.Render("No tasks yet")+"\n", height)
	}
	switch t.View {
	case TaskViewDetail:
		return a.renderTaskDetail(t, height)
	case TaskViewLogs:
		return a.renderTaskLogs(t, height)
	case TaskViewWorkflow:
		return a.renderWorkflowMonitor(t, height)
	case TaskViewTranscript:
		return a.renderTaskTranscript(t, height)
	default:
		return a.renderTaskList(t, height)
	}
}

func (a *App) renderTaskList(t *TasksTab, height int) string {
	items := a.filteredTasks(t)
	if len(items) == 0 {
		msg := "No tasks yet — start the dispatcher and give it work."
		if t.FilterText != "" {
			msg = "No tasks match filter"
		}
		title := "TASKS"
		if t.FilterText != "" {
			title += " [/" + t.FilterText + "]"
		}
		return a.box(title, StyleMuted.Render(msg)+"\n", height)
	}

	const (
		cursorW = 2
		numW    = 10
		agentW  = 16
		statusW = 8
		whenW   = 11
	)
	inner := a.model.width - 2
	titleW := inner - cursorW - numW - agentW - statusW - whenW - 5
	if titleW < 10 {
		titleW = 10
	}

	var b strings.Builder

	// Box title with sort/filter indicators
	title := "TASKS"
	parts := []string{}
	if t.FilterText != "" {
		parts = append(parts, "/"+t.FilterText)
	}
	if t.SortField != "" {
		dir := "↓"
		if t.SortAsc {
			dir = "↑"
		}
		parts = append(parts, "sort:"+t.SortField+dir)
	}
	if len(parts) > 0 {
		title += " [" + strings.Join(parts, " ") + "]"
	}

	header := pad("", cursorW) + " " + pad("#", numW) + " " + pad("TASK", titleW) + " " + pad("AGENT", agentW) + " " + pad("STATUS", statusW) + " " + "WHEN"
	b.WriteString(StyleTableHeader.Render(header) + "\n")

	rowsAvail := height - 3 // borders + header
	if rowsAvail < 1 {
		rowsAvail = 1
	}

	// Window the list so the cursor row stays visible
	start := t.SelectedIdx - rowsAvail/2
	if start > len(items)-rowsAvail {
		start = len(items) - rowsAvail
	}
	if start < 0 {
		start = 0
	}
	end := start + rowsAvail
	if end > len(items) {
		end = len(items)
	}

	for i := start; i < end; i++ {
		it := items[i]
		selected := i == t.SelectedIdx
		cursor := "  "
		num := pad(truncate(valueOr(it.Number, "—"), numW), numW)
		titleText := pad(truncate(valueOr(it.Title, it.TaskID), titleW), titleW)
		agent := pad(truncate(valueOr(it.Agent, "—"), agentW), agentW)
		status := taskStatusBadge(it.Status)
		when := taskWhen(it)
		if selected {
			cursor = StyleFocusedArrow.Render("▶") + " "
			num = StyleSelectedRow.Render(num)
			titleText = StyleSelectedRow.Render(titleText)
			agent = StyleSelectedRow.Render(agent)
			status = StyleSelectedRow.Render(status)
			when = StyleSelectedRow.Render(when)
		} else {
			num = StyleAccent.Render(num)
			when = StyleMuted.Render(when)
		}
		b.WriteString(cursor + " " + num + " " + titleText + " " + agent + " " + status + " " + when + "\n")
	}

	return a.box(title, b.String(), height)
}

// compareTimePtr is a 3-way comparison for optional timestamps, treating a nil
// pointer as the smallest value so missing times sort consistently at one end
// regardless of direction. Returning 0 for equal/both-nil keeps the comparator
// a valid strict weak ordering.
func compareTimePtr(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case a.Before(*b):
		return -1
	case a.After(*b):
		return 1
	default:
		return 0
	}
}

// filteredTasks returns the task list after applying filter and sort.
func (a *App) filteredTasks(t *TasksTab) []TaskItem {
	out := t.History
	if t.FilterText != "" {
		filter := strings.ToLower(t.FilterText)
		var filtered []TaskItem
		for _, it := range out {
			if strings.Contains(strings.ToLower(it.Title), filter) ||
				strings.Contains(strings.ToLower(it.TaskID), filter) ||
				strings.Contains(strings.ToLower(it.Agent), filter) ||
				strings.Contains(strings.ToLower(it.Number), filter) ||
				strings.Contains(strings.ToLower(it.Status), filter) {
				filtered = append(filtered, it)
			}
		}
		out = filtered
	}

	sortField := t.SortField
	if sortField == "" {
		sortField = "time"
	}
	sort.SliceStable(out, func(i, j int) bool {
		var cmp int
		switch sortField {
		case "status":
			cmp = strings.Compare(out[i].Status, out[j].Status)
		case "agent":
			cmp = strings.Compare(out[i].Agent, out[j].Agent)
		case "number":
			cmp = strings.Compare(out[i].Number, out[j].Number)
		case "title":
			cmp = strings.Compare(out[i].Title, out[j].Title)
		case "updated":
			cmp = compareTimePtr(lastUpdate(out[i]), lastUpdate(out[j]))
		default:
			// time: newest first by default (StartedAt desc)
			cmp = compareTimePtr(out[i].StartedAt, out[j].StartedAt)
		}
		// Equal elements must compare false in both directions, otherwise the
		// comparator is not a valid strict weak ordering and SliceStable
		// reshuffles ties on every pass (the list never settles, especially on
		// status-desc where ties are common).
		if cmp == 0 {
			return false
		}
		if t.SortAsc {
			return cmp < 0
		}
		return cmp > 0
	})

	return out
}

// taskRollup derives the whole-task span and token totals from its workflow
// instances (each already aggregated from its steps), so the header reflects the
// entire run rather than only the last execution row (e.g. the merge step). It
// falls back to the detail instance when the task has no internal-task instance
// list, and to the execution-row values when neither carries a span/usage.
func taskRollup(d *TaskItem, detail *WorkflowInstanceItem) (started, completed *time.Time, inTok, outTok, totalTok, cacheCreate, cacheRead int, cost float64) {
	insts := d.Instances
	if len(insts) == 0 && detail != nil {
		insts = []WorkflowInstanceItem{*detail}
	}
	for _, in := range insts {
		if in.StartedAt != nil && (started == nil || in.StartedAt.Before(*started)) {
			started = in.StartedAt
		}
		if in.FinishedAt != nil && (completed == nil || in.FinishedAt.After(*completed)) {
			completed = in.FinishedAt
		}
		inTok += in.InputTokens
		outTok += in.OutputTokens
		totalTok += in.TotalTokens
		cacheCreate += in.CacheCreationTokens
		cacheRead += in.CacheReadTokens
		cost += in.CostUSD
	}
	if started == nil {
		started = d.StartedAt
	}
	if completed == nil {
		completed = d.CompletedAt
	}
	if totalTok == 0 { // no instance-level usage — fall back to the execution row
		inTok, outTok, totalTok, cost = d.InputTokens, d.OutputTokens, d.TotalTokens, d.CostUSD
	}
	return
}

func (a *App) renderTaskDetail(t *TasksTab, height int) string {
	d := t.Detail
	if d == nil {
		return a.box("TASK DETAILS", StyleMuted.Render("No details")+"\n", height)
	}

	lines := a.taskDetailLines(t)
	label := taskDetailLabel(d)

	rows := height - 2 // top + bottom borders
	if rows < 1 {
		rows = 1
	}
	// Clamp the scroll offset so the window always shows real content: never past
	// the line that leaves a single page visible at the bottom.
	maxStart := len(lines) - rows
	if maxStart < 0 {
		maxStart = 0
	}
	if t.DetailScroll > maxStart {
		t.DetailScroll = maxStart
	}
	if t.DetailScroll < 0 {
		t.DetailScroll = 0
	}
	start := t.DetailScroll
	end := start + rows
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(lines[i] + "\n")
	}
	return a.box(label, b.String(), height)
}

// taskDetailLines builds the full, styled content of the task detail view as
// individual visual lines. renderTaskDetail windows these by DetailScroll so a
// task with many workflow instances/children scrolls instead of being cut off.
func (a *App) taskDetailLines(t *TasksTab) []string {
	d := t.Detail
	rStart, rEnd, inTok, outTok, totalTok, cacheCreate, cacheRead, cost := taskRollup(d, t.DetailInstance)
	started, completed, dur := "—", "—", "—"
	if rStart != nil {
		started = rStart.Format("2006-01-02 15:04:05")
	}
	if rEnd != nil {
		completed = rEnd.Format("2006-01-02 15:04:05")
	}
	switch {
	case rStart != nil && rEnd != nil:
		dur = rEnd.Sub(*rStart).Round(time.Second).String()
	case d.Duration > 0:
		dur = d.Duration.Round(time.Second).String()
	}

	var b strings.Builder
	row := func(k, v string) {
		b.WriteString("  " + StyleLabel.Render(pad(k+":", 14)) + " " + v + "\n")
	}
	row("Number", StyleAccent.Render(valueOr(d.Number, "—")))
	row("Task ID", StyleValueStrong.Render(d.TaskID))
	row("Title", valueOr(d.Title, "—"))
	row("Status", taskStatusBadge(d.Status))
	if d.OutstandingWorkflows > 0 {
		row("Outstanding", fmt.Sprintf("%d workflow(s) running", d.OutstandingWorkflows))
	}
	if d.ParentTitle != "" {
		row("Parent", StyleMuted.Render(d.ParentTitle))
	}
	row("Agent", valueOr(d.Agent, "—"))
	row("Model", valueOr(d.Model, "—"))
	row("Runner", valueOr(d.Runner, "—"))
	row("Attempts", fmt.Sprintf("%d", d.Attempt))
	row("Started", started)
	// Label the terminal timestamp by outcome: a failed task that shows
	// "Completed: <time>" reads as success even though the Status badge says
	// failed. CompletedAt is populated for both done and failed terminal states.
	endedLabel := "Completed"
	if d.Status == "failed" {
		endedLabel = "Failed at"
	}
	row(endedLabel, completed)
	row("Duration", dur)
	row("Tokens", fmt.Sprintf("%d in / %d out / %d total", inTok, outTok, totalTok))
	if cacheCreate > 0 || cacheRead > 0 {
		row("Cache", fmt.Sprintf("%d write / %d read", cacheCreate, cacheRead))
	}
	row("Turns / Calls", fmt.Sprintf("%d / %d", d.NumTurns, d.NumToolCalls))
	row("Cost", fmt.Sprintf("$%.4f", cost))
	if d.URL != "" {
		row("URL", StyleInfo.Render(d.URL))
	}
	if d.Error != "" {
		b.WriteString("\n")
		b.WriteString("  " + StyleError.Render("Error:") + "\n")
		b.WriteString("  " + StyleError.Render(truncate(d.Error, a.model.width-4)) + "\n")
	}
	if t.DetailInstance != nil {
		b.WriteString(renderWorkflowSteps(t.DetailInstance))
	}
	b.WriteString(renderSourceBindings(d))
	b.WriteString(renderTaskLineage(d))
	b.WriteString(renderTaskInstances(d))
	b.WriteString(renderTaskTimeline(d))
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

// renderSourceBindings renders a task's source bindings (9.1.2): one row per
// bound source item showing its number, source id, and deep link. Empty when the
// task has no bindings (e.g. a spawned task).
func renderSourceBindings(d *TaskItem) string {
	if len(d.Bindings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n  " + StyleLabel.Render("Source Bindings") + "\n")
	for _, sb := range d.Bindings {
		b.WriteString(fmt.Sprintf("    %s  %s  %s\n",
			StyleAccent.Render(pad(valueOr(sb.ItemNumber, "—"), 10)),
			StyleMuted.Render(pad(sb.SourceID, 10)),
			StyleInfo.Render(sb.ItemURL),
		))
	}
	return b.String()
}

// renderTaskLineage renders a task's lineage (9.1.3 + 9.2): a root→self
// breadcrumb of ancestors and a tree of direct children. Each child node shows a
// state badge, a binding indicator, its title, and its workflow-instance count
// (9.2.2). Empty when the task is a root with no children.
func renderTaskLineage(d *TaskItem) string {
	var b strings.Builder
	if len(d.Lineage) > 1 { // a single-element lineage is just the task itself
		parts := make([]string, 0, len(d.Lineage))
		for i, n := range d.Lineage {
			title := valueOr(n.Title, n.TaskID)
			if i == len(d.Lineage)-1 {
				parts = append(parts, StyleValueStrong.Render(title)) // self, last
			} else {
				parts = append(parts, StyleMuted.Render(title))
			}
		}
		b.WriteString("\n  " + StyleLabel.Render("Lineage") + "  " + strings.Join(parts, StyleMuted.Render(" > ")) + "\n")
	}
	if len(d.Children) > 0 {
		b.WriteString("\n  " + StyleLabel.Render(fmt.Sprintf("Children (%d)", len(d.Children))) + "\n")
		for _, c := range d.Children {
			b.WriteString("    " + lineageNodeRow(c) + "\n")
		}
	}
	return b.String()
}

// lineageNodeRow renders one lineage tree node: state badge, binding indicator
// (● when the task has a source binding), title, and instance count.
func lineageNodeRow(n TaskLineageItem) string {
	bind := StyleFooterDim.Render("·")
	if n.HasBinding {
		bind = StyleAccent.Render("●")
	}
	return fmt.Sprintf("%s %s %s  %s",
		taskStatusBadge(n.State),
		bind,
		valueOr(n.Title, n.TaskID),
		StyleMuted.Render(fmt.Sprintf("(%d inst)", n.InstanceCount)),
	)
}

// renderTaskInstances renders every workflow instance bound to a task (9.1.4):
// a task may fan out to several workflows, so all instances are listed with their
// state, workflow id, creation time, and a resumed/sub-workflow marker.
func renderTaskInstances(d *TaskItem) string {
	if len(d.Instances) == 0 {
		return ""
	}
	const (
		colState = 12
		colWf    = 18
	)
	var b strings.Builder
	b.WriteString("\n  " + StyleLabel.Render(fmt.Sprintf("Workflow Instances (%d)", len(d.Instances))) + "\n")
	b.WriteString("    " + StyleLabel.Render(fmt.Sprintf("%s %s %s %s %s %s",
		pad("STATE", colState),
		pad("WORKFLOW", colWf),
		pad("STARTED", colTime),
		pad("ENDED", colTime),
		padLeft("DURATION", colDur),
		padLeft("TOKENS", colTok),
	)) + "\n")
	for _, in := range d.Instances {
		marker := ""
		switch {
		case in.ResumedFrom != "":
			marker = StyleMuted.Render("  (resumed)")
		case in.ParentInstanceID != "":
			marker = StyleMuted.Render("  (sub)")
		}
		dur := "—"
		if in.StartedAt != nil && in.FinishedAt != nil {
			dur = in.FinishedAt.Sub(*in.StartedAt).Round(time.Second).String()
		}
		b.WriteString(fmt.Sprintf("    %s %s %s %s %s %s%s\n",
			pad(wfInstanceBadge(in.State), colState),
			pad(truncate(in.Workflow, colWf), colWf),
			StyleMuted.Render(pad(tsCell(in.StartedAt), colTime)),
			StyleMuted.Render(pad(tsCell(in.FinishedAt), colTime)),
			StyleMuted.Render(padLeft(dur, colDur)),
			StyleMuted.Render(padLeft(fmtTokensShort(in.TotalTokens), colTok)),
			marker,
		))
	}
	return b.String()
}

func renderTaskTimeline(d *TaskItem) string {
	if len(d.Events) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n  " + StyleLabel.Render(fmt.Sprintf("Timeline (%d)", len(d.Events))) + "\n")
	for _, event := range d.Events {
		b.WriteString(fmt.Sprintf("    %s  %s", StyleMuted.Render(event.Timestamp.Local().Format("15:04:05")), event.Type))
		if event.StepID != "" {
			b.WriteString(StyleMuted.Render(" · " + event.StepID))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderWorkflowSteps renders the step-progress panel for a task that ran
// through the workflow engine: the workflow id + instance state, an
// approval-waiting banner when parked, and one row per step.
func renderWorkflowSteps(inst *WorkflowInstanceItem) string {
	var b strings.Builder
	b.WriteString("\n  " + StyleLabel.Render("Workflow") + "      " +
		StyleValueStrong.Render(valueOr(inst.Workflow, "—")) + "  " +
		wfInstanceBadge(inst.State) + "\n")

	if summary := wfInstanceSummary(inst); summary != "" {
		b.WriteString("              " + summary + "\n")
	}

	if inst.State == db.InstanceStateApprovalWaiting && inst.Message != "" {
		b.WriteString("  " + StyleWarning.Render("⏸ "+inst.Message) + "\n")
		if inst.Approval != nil {
			b.WriteString("  " + StyleMuted.Render("Approvers: "+strings.Join(inst.Approval.Approvers, ", ")) + "\n")
			if len(inst.Approval.Fields) == 0 {
				b.WriteString("  " + StyleAccent.Render("Press y to approve or n to reject") + "\n")
			} else {
				b.WriteString("  " + StyleMuted.Render("Structured fields required; submit through the signed webhook channel.") + "\n")
			}
		}
	}

	if len(inst.Steps) > 0 {
		b.WriteString("\n  " + StyleLabel.Render("Steps") + "\n")
		b.WriteString("    " + wfStepHeader() + "\n")
		for i, s := range inst.Steps {
			if i > 0 {
				if w := wfWaitRow(inst.Steps[i-1], s); w != "" {
					b.WriteString("    " + w + "\n")
				}
			}
			b.WriteString("    " + wfStepRow(s) + "\n")
		}
	}
	if m := wfFailureMarker(inst); m != "" {
		b.WriteString("  " + m + "\n")
	}
	b.WriteString(renderCIPolls(inst.CIPolls))
	return b.String()
}

// renderCIPolls renders the wait_for CI poll history: a header with the count
// and the latest status, then the most recent poll rows (oldest of the window
// first). Empty when the instance never polled CI.
func renderCIPolls(polls []CIPollItem) string {
	if len(polls) == 0 {
		return ""
	}
	const window = 8
	var b strings.Builder
	last := polls[len(polls)-1]
	b.WriteString("\n  " + StyleLabel.Render(fmt.Sprintf("CI Polls (%d)", len(polls))) +
		"   " + StyleMuted.Render("last: ") + ciPollStatusStyle(last.Status).Render(last.Status) +
		StyleMuted.Render(" · "+last.CheckedAt.Format("01-02 15:04:05")) + "\n")

	start := 0
	if len(polls) > window {
		start = len(polls) - window
		b.WriteString("    " + StyleMuted.Render(fmt.Sprintf("… %d earlier", start)) + "\n")
	}
	for _, p := range polls[start:] {
		row := StyleMuted.Render(pad(p.CheckedAt.Format("01-02 15:04:05"), 17)) + "  " +
			ciPollStatusStyle(p.Status).Render(pad(p.Status, 8))
		if p.Detail != "" {
			row += "  " + StyleMuted.Render(truncate(p.Detail, 50))
		}
		b.WriteString("    " + row + "\n")
	}
	return b.String()
}

// ciPollStatusStyle maps a recorded CI poll status to a display style.
func ciPollStatusStyle(status string) lipgloss.Style {
	switch status {
	case "passed":
		return StyleSuccess
	case "failed", "timeout", "error":
		return StyleError
	case "pending":
		return StyleWarning
	default:
		return StyleMuted
	}
}

// wfFailureMarker returns a one-line notice when an instance is failed but none
// of its recorded steps is — i.e. the run died after (or between) the steps that
// were persisted (e.g. a later CI-wait or gate), so the all-passed step list
// would otherwise read as a completed run.
func wfFailureMarker(inst *WorkflowInstanceItem) string {
	if inst.State != db.InstanceStateFailed {
		return ""
	}
	for _, s := range inst.Steps {
		if s.State == db.StepStateFailed {
			return "" // the failing step is already visible in the list
		}
	}
	after := "before any step completed"
	if n := len(inst.Steps); n > 0 {
		after = "after step '" + inst.Steps[n-1].StepID + "'"
	}
	return StyleError.Render("✗ workflow failed " + after + " — no further steps recorded")
}

// wfInstanceSummary renders a one-line dated-span/duration/token/cost rollup for
// an instance, or "" when it has no recorded span (e.g. a pending instance with no
// started step). Shown under the Workflow header.
func wfInstanceSummary(inst *WorkflowInstanceItem) string {
	if inst.StartedAt == nil {
		return ""
	}
	parts := []string{datedSpan(inst.StartedAt, inst.FinishedAt)}
	if inst.FinishedAt != nil {
		parts = append(parts, inst.FinishedAt.Sub(*inst.StartedAt).Round(time.Second).String())
	}
	parts = append(parts, fmtTokensShort(inst.TotalTokens)+" tokens")
	if inst.CacheCreationTokens > 0 || inst.CacheReadTokens > 0 {
		parts = append(parts, fmtTokensShort(inst.CacheCreationTokens+inst.CacheReadTokens)+" cache")
	}
	if inst.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", inst.CostUSD))
	}
	return StyleMuted.Render(strings.Join(parts, "  ·  "))
}

// Step/instance table column widths, shared by header and rows so they align.
// colTime fits tsTimeFmt ("01-02 15:04:05").
const (
	colStep  = 14
	colAgent = 14
	colTime  = 14
	colDur   = 8
	colTok   = 8
)

// stepGapColumn is the number of characters from the start of a step row (after
// the caller's indent) to where the STARTED cell begins: glyph + sep + step +
// sep + agent + sep. The wait-between-steps marker is padded to it so it sits
// under the timestamps.
const stepGapColumn = 1 + 1 + colStep + 1 + colAgent + 1

// minStepWait is the smallest inter-step gap worth showing: below it the gap is
// just dispatch/queue latency (a few seconds), which would only add noise lines.
const minStepWait = 30 * time.Second

// wfWaitRow renders the idle gap between a finished step and the next step's
// start (CI waits, approvals, queue time), aligned under the STARTED column.
// Returns "" when either timestamp is missing or the gap is below minStepWait.
func wfWaitRow(prev, cur WorkflowStepItem) string {
	if prev.FinishedAt == nil || cur.StartedAt == nil {
		return ""
	}
	gap := cur.StartedAt.Sub(*prev.FinishedAt).Round(time.Second)
	if gap < minStepWait {
		return ""
	}
	return strings.Repeat(" ", stepGapColumn) + StyleMuted.Render("↓ "+gap.String()+" waiting")
}

// wfStepHeader renders the column header for the step table. The leading single
// space stands in for the per-row status glyph so "STEP" aligns over the step id.
func wfStepHeader() string {
	return StyleLabel.Render(fmt.Sprintf("%s %s %s %s %s %s %s  %s",
		" ",
		pad("STEP", colStep),
		pad("AGENT", colAgent),
		pad("STARTED", colTime),
		pad("ENDED", colTime),
		padLeft("DURATION", colDur),
		padLeft("TOKENS", colTok),
		"STATE",
	))
}

// wfStepRow formats a single workflow step as one columnar display row (glyph,
// step id, agent, started, ended, duration, tokens, state) without indentation or
// trailing newline, aligned under wfStepHeader. Callers add the indent.
func wfStepRow(s WorkflowStepItem) string {
	state := s.State
	if s.Cached {
		state += " (cached)"
	}
	return fmt.Sprintf("%s %s %s %s %s %s %s  %s",
		wfStepGlyph(s.State),
		pad(truncate(s.StepID, colStep), colStep),
		pad(truncate(s.Agent, colAgent), colAgent),
		StyleMuted.Render(pad(tsCell(s.StartedAt), colTime)),
		StyleMuted.Render(pad(tsCell(s.FinishedAt), colTime)),
		StyleMuted.Render(padLeft(valueOr(s.Duration, "—"), colDur)),
		StyleMuted.Render(padLeft(fmtTokensShort(s.TotalTokens), colTok)),
		wfStateStyle(s.State).Render(state),
	)
}

func wfInstanceBadge(state string) string {
	switch state {
	case db.InstanceStateDone:
		return StyleSuccess.Render("done")
	case db.InstanceStateFailed:
		return StyleError.Render("failed")
	case db.InstanceStateRunning:
		return StyleWarning.Render("running")
	case db.InstanceStateApprovalWaiting:
		return StyleWarning.Render("approval_waiting")
	case db.InstanceStateWaiting:
		return StyleWarning.Render("waiting")
	case db.InstanceStateInterrupted:
		return StyleError.Render("interrupted")
	default:
		return StyleMuted.Render(state)
	}
}

func wfStepGlyph(state string) string {
	switch state {
	case db.StepStatePassed:
		return StyleSuccess.Render("✓")
	case db.StepStateFailed:
		return StyleError.Render("✗")
	case db.StepStateRunning:
		return StyleWarning.Render("●")
	case db.StepStateInterrupted:
		return StyleError.Render("⊗")
	case db.StepStateSkipped, db.StepStateSkippedCached:
		return StyleMuted.Render("⊘")
	default:
		return StyleMuted.Render("○")
	}
}

func wfStateStyle(state string) lipgloss.Style {
	switch state {
	case db.StepStatePassed:
		return StyleSuccess
	case db.StepStateFailed, db.StepStateInterrupted:
		return StyleError
	case db.StepStateRunning:
		return StyleWarning
	default:
		return StyleMuted
	}
}

func taskDetailLabel(d *TaskItem) string {
	prefix := ""
	if d.Number != "" {
		prefix = d.Number + " "
	}
	title := strings.TrimSpace(d.Title)
	const maxTitle = 60
	if len(title) > maxTitle {
		title = title[:maxTitle-1] + "…"
	}
	if title != "" {
		return "TASK " + prefix + "— " + title
	}
	return "TASK " + prefix + "— " + d.TaskID
}

func (a *App) renderTaskLogs(t *TasksTab, height int) string {
	if len(t.Logs) == 0 && len(t.InstanceHistory) == 0 {
		label := "TASK LOGS"
		if t.Detail != nil {
			label = taskDetailLabel(t.Detail)
		}
		body := StyleMuted.Render("No logs recorded for this task.")
		if a.model.loading {
			body = a.loadingLine("Loading logs…")
		}
		return a.box(label, body+"\n", height)
	}

	lines := a.taskLogLines()

	rows := height - 2 // top + bottom borders
	if rows < 1 {
		rows = 1
	}
	// Follow mode: keep the viewport pinned to the tail; scrolling down to the
	// last page re-engages it (matching the G/end semantics).
	maxStart := pinToTail(len(lines), rows)
	if t.LogScroll >= maxStart {
		t.LogFollow = true
	}
	if t.LogFollow {
		t.LogScroll = maxStart
	}
	// A "load older" hint takes the top row when the flat-log tail is scrolled to
	// its start and an older page is available (or being fetched).
	hint := ""
	if t.LogScroll == 0 && len(t.InstanceHistory) == 0 {
		if t.LogLoadingMore {
			hint = "↑ loading older logs…"
		} else if t.LogHasMore {
			hint = "↑ older logs — press ↑/PgUp to load"
		}
	}
	if hint != "" && rows > 1 {
		rows-- // reserve a row for the hint so the box height is unchanged
	}
	start := t.LogScroll
	if start > len(lines)-1 {
		start = len(lines) - 1
	}
	if start < 0 {
		start = 0
	}
	end := start + rows
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	if hint != "" {
		b.WriteString(StyleMuted.Render(hint) + "\n")
	}
	for i := start; i < end; i++ {
		b.WriteString(lines[i] + "\n")
	}
	label := "TASK LOGS"
	if t.Detail != nil {
		label = taskDetailLabel(t.Detail)
	}
	return a.box(label, b.String(), height)
}

// taskLogLines expands the per-task log entries into fully-wrapped, styled
// visual lines: messages with embedded newlines (the prompt, the multi-line
// agent conversation) are split, and long lines are wrapped to the box width,
// so the *whole* log is viewable by scrolling rather than truncated to one line.
func (a *App) taskLogLines() []string {
	t := a.model.tasksTab
	if t == nil {
		return nil
	}
	if len(t.InstanceHistory) > 0 {
		return a.taskHistoryLines()
	}
	return a.logEntryLines(t.Logs)
}

// taskHistoryLines flattens the per-instance history into styled visual lines for
// the repurposed logs view: each workflow instance becomes a labeled header, then
// its step rows, then its time-scoped log lines — oldest instance first, so a
// multi-workflow task reads top-to-bottom as a chronological story.
func (a *App) taskHistoryLines() []string {
	t := a.model.tasksTab
	if t == nil {
		return nil
	}
	sw := a.model.width - 8
	if sw < 20 {
		sw = 20
	}
	var out []string
	for i, seg := range t.InstanceHistory {
		if i > 0 {
			out = append(out, "") // blank separator between instances
		}
		out = append(out, historySegmentHeader(seg.Instance))
		if seg.Instance.State == db.InstanceStateApprovalWaiting && seg.Instance.Message != "" {
			out = append(out, "  "+StyleWarning.Render("⏸ "+seg.Instance.Message))
		}
		if len(seg.Instance.Steps) > 0 {
			out = append(out, "  "+wfStepHeader())
		}
		for i, s := range seg.Instance.Steps {
			if i > 0 {
				if w := wfWaitRow(seg.Instance.Steps[i-1], s); w != "" {
					out = append(out, "  "+w)
				}
			}
			out = append(out, "  "+wfStepRow(s))
			if s.Summary != "" {
				out = append(out, "      "+StyleMuted.Render(truncate(s.Summary, sw)))
			}
		}
		if m := wfFailureMarker(&seg.Instance); m != "" {
			out = append(out, "  "+m)
		}
		if polls := renderCIPolls(seg.Instance.CIPolls); polls != "" {
			out = append(out, strings.Split(strings.Trim(polls, "\n"), "\n")...)
		}
		out = append(out, a.logEntryLines(seg.Logs)...)
	}
	return out
}

// historySegmentHeader renders the section header for one workflow instance: a
// state badge, the workflow id, and when it started.
func historySegmentHeader(in WorkflowInstanceItem) string {
	when := ""
	if !in.CreatedAt.IsZero() {
		when = StyleMuted.Render(" · " + in.CreatedAt.Format("01-02 15:04"))
	}
	return StyleMuted.Render("── ") + wfInstanceBadge(in.State) + " " +
		StyleValueStrong.Render(valueOr(in.Workflow, "—")) + when + StyleMuted.Render(" ──")
}

// agentTaskLogLines renders the drill-down logs of the task selected in an
// agent's activity list.
func (a *App) agentTaskLogLines() []string {
	ag := a.model.agentsTab
	if ag == nil {
		return nil
	}
	return a.logEntryLines(ag.TaskLogs)
}

// logEntryLines expands per-task log entries into fully-wrapped, styled visual
// lines: messages with embedded newlines (the prompt, the multi-line agent
// conversation) are split, and long lines are wrapped to the box width, so the
// whole log is viewable by scrolling rather than truncated to one line.
func (a *App) logEntryLines(logs []LogEntry) []string {
	msgWidth := a.logMsgWidth()
	indent := strings.Repeat(" ", logPrefixWidth)

	var out []string
	for _, entry := range logs {
		ts := StyleMuted.Render(entry.Timestamp.Format("15:04:05"))
		level := levelStyle(entry.Level).Render(fmt.Sprintf("%-5s", entry.Level))
		wrapped := a.logMessageLines(entry.Message, msgWidth)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for j, w := range wrapped {
			if j == 0 {
				out = append(out, fmt.Sprintf("%s %s %s", ts, level, w))
			} else {
				out = append(out, indent+w)
			}
		}
	}
	return out
}

// logMessageLines renders a log message to display lines at the given content
// width. Operational one-liners wrap directly. Multi-line messages go through
// the width-scoped cache: glamour-rendered markdown arrives there asynchronously
// (warmMarkdownCmd) because rendering it inline is what made log views slow to
// open (#175) — until the warmed lines land, markdown shows plain-wrapped.
// Multi-line messages glamour will never style (oversized or non-markdown) are
// plain-wrapped and cached eagerly so periodic refreshes stop re-wrapping them.
func (a *App) logMessageLines(msg string, width int) []string {
	if width < 1 {
		width = 1
	}
	if !strings.Contains(msg, "\n") {
		return wrapPlain(msg, width)
	}

	a.ensureLogCache(width)
	if lines, ok := a.logMDCache[msg]; ok {
		return lines
	}
	lines := wrapPlain(msg, width)
	if glamourEligible(msg) {
		// Don't cache: the warm-up replaces this with the styled render, and the
		// cache miss is what tells warmMarkdownCmd the message still needs work.
		return lines
	}
	a.logMDCache[msg] = lines
	return lines
}

// ensureLogCache resets the width-scoped log line caches when the wrap width
// changes (terminal resize) or on first use.
func (a *App) ensureLogCache(width int) {
	if a.logMDCache == nil || a.logMDWidth != width {
		a.logMDCache = make(map[string][]string)
		a.logMDPending = make(map[string]bool)
		a.logMDWidth = width
	}
}

// glamourEligible reports whether a message is worth styling with glamour:
// markdown-looking and small enough that rendering it is not the bottleneck.
func glamourEligible(msg string) bool {
	return len(msg) <= maxGlamourBytes && looksLikeMarkdown(msg)
}

// logMsgWidth is the content width log messages wrap to (the active log view's
// width minus the timestamp/level prefix column). Full-screen log views use the
// terminal width minus the box border; the workflow monitor's STEP LOGS panel
// uses the right-panel width so lines wrap inside the panel instead of being
// clipped by fitLine. Shared by the render paths and warmMarkdownCmd so warmed
// cache entries match render-time lookups.
func (a *App) logMsgWidth() int {
	w := a.model.width - 2 - logPrefixWidth
	if t := a.model.tasksTab; t != nil && t.View == TaskViewWorkflow && t.WorkflowShowLogs {
		_, rightW := a.wfMonitorPanelWidths()
		w = rightW - logPrefixWidth
	}
	if w < 20 {
		w = 20
	}
	return w
}

// warmMarkdownCmd returns a command that glamour-renders the given entries'
// markdown messages off the UI thread and delivers them as a mdWarmedMsg. It
// skips messages already cached or already in flight, and returns nil when
// nothing needs work — so handlers can call it on every data refresh cheaply.
func (a *App) warmMarkdownCmd(entries []LogEntry) tea.Cmd {
	width := a.logMsgWidth()
	a.ensureLogCache(width)
	var msgs []string
	for _, e := range entries {
		m := e.Message
		if !strings.Contains(m, "\n") || !glamourEligible(m) {
			continue
		}
		if _, ok := a.logMDCache[m]; ok {
			continue
		}
		if a.logMDPending[m] {
			continue
		}
		a.logMDPending[m] = true
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 {
		return nil
	}
	return func() tea.Msg {
		// One renderer for the whole batch; each message falls back to its plain
		// wrap if glamour errors, so the merge always has lines to deliver.
		r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(width))
		rendered := make(map[string][]string, len(msgs))
		for _, m := range msgs {
			lines := wrapPlain(m, width)
			if err == nil {
				if out, rerr := r.Render(m); rerr == nil {
					lines = clampToWidth(strings.Split(strings.TrimRight(out, "\n"), "\n"), width)
				}
			}
			rendered[m] = lines
		}
		return mdWarmedMsg{width: width, rendered: rendered}
	}
}

// segmentLogs flattens the per-instance history segments into one entry list
// for the markdown warm-up.
func segmentLogs(segments []TaskHistorySegmentItem) []LogEntry {
	var out []LogEntry
	for _, seg := range segments {
		out = append(out, seg.Logs...)
	}
	return out
}

// warmOpenLogsCmd re-warms every loaded log entry. Used after a terminal resize,
// which drops the width-scoped cache: tail refreshes only warm newly-appended
// lines, so without this the already-loaded history would stay plain forever.
func (a *App) warmOpenLogsCmd() tea.Cmd {
	var entries []LogEntry
	if t := a.model.tasksTab; t != nil {
		entries = append(entries, t.Logs...)
		for _, seg := range t.InstanceHistory {
			entries = append(entries, seg.Logs...)
		}
		entries = append(entries, t.WorkflowLogs...)
	}
	if ag := a.model.agentsTab; ag != nil {
		entries = append(entries, ag.TaskLogs...)
	}
	if l := a.model.logsTab; l != nil {
		entries = append(entries, l.Logs...)
	}
	return a.warmMarkdownCmd(entries)
}

// looksLikeMarkdown is a cheap heuristic: only multi-line messages with at least
// one markdown marker (heading, list item, blockquote, code fence, table, or
// bold) are treated as markdown. Single-line operational logs stay plain.
func looksLikeMarkdown(s string) bool {
	if !strings.Contains(s, "\n") {
		return false
	}
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "#"),
			strings.HasPrefix(t, "- "),
			strings.HasPrefix(t, "* "),
			strings.HasPrefix(t, "> "),
			strings.HasPrefix(t, "```"),
			strings.HasPrefix(t, "|"):
			return true
		}
		if strings.Contains(t, "**") {
			return true
		}
	}
	return false
}

// wrapPlain splits s on newlines and hard-wraps each line to width runes,
// expanding tabs so the output aligns in the fixed-width terminal.
func wrapPlain(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		raw = strings.ReplaceAll(raw, "\t", "    ")
		if raw == "" {
			out = append(out, "")
			continue
		}
		r := []rune(raw)
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	return out
}

func (a *App) renderAgentsTab(height int) string {
	ag := a.model.agentsTab
	if ag == nil {
		return a.box("AGENTS", StyleMuted.Render("No agents yet")+"\n", height)
	}
	switch ag.View {
	case AgentViewDetail:
		return a.renderAgentDetail(ag, height)
	case AgentViewActivity:
		return a.renderAgentActivity(ag, height)
	case AgentViewTaskLogs:
		return a.renderAgentTaskLogs(ag, height)
	case AgentViewFiles:
		return a.renderAgentFiles(ag, height)
	case AgentViewFileContent:
		return a.renderAgentFileContent(ag, height)
	default:
		return a.renderAgentList(ag, height)
	}
}

func (a *App) renderAgentList(ag *AgentsTab, height int) string {
	if len(ag.Agents) == 0 {
		return a.box("AGENTS", StyleMuted.Render("No agents yet — they appear after running at least one task.")+"\n", height)
	}

	const (
		cursorW    = 2
		workersW   = 8
		statusW    = 11
		completedW = 11
		avgW       = 10
		successW   = 8
		costW      = 10
	)
	inner := a.model.width - 2
	agentW := inner - cursorW - workersW - statusW - completedW - avgW - successW - costW - 12
	if agentW < 12 {
		agentW = 12
	}

	var b strings.Builder
	header := pad("", cursorW) + " " + pad("AGENT", agentW) + "    " + padLeft("WORKERS", workersW) + " " + pad("STATUS", statusW) + "  " + padLeft("COMPLETED", completedW) + "" + padLeft("AVG", avgW) + "  " + padLeft("SUCCESS", successW) + "  " + padLeft("COST", costW)
	b.WriteString(StyleTableHeader.Render(header) + "\n")
	for i, agent := range ag.Agents {
		selected := i == ag.SelectedIdx
		cursor := "  "
		name := pad(truncate(valueOr(agent.ID, "—"), agentW), agentW)
		workers := fmt.Sprintf("%d/%d", agent.RunningCount, agent.MaxWorkers)
		if agent.MaxWorkers > 0 {
			if agent.RunningCount >= agent.MaxWorkers {
				workers = StyleWarning.Render(workers)
			} else if agent.RunningCount > 0 {
				workers = StyleSuccess.Render(workers)
			} else {
				workers = StyleMuted.Render(workers)
			}
		}
		status := pad(agentStatusText(agent.Status), statusW)
		completed := padLeft(fmt.Sprintf("%d", agent.CompletedCount), completedW)
		avg := padLeft(fmt.Sprintf("%.1fs", float64(agent.AvgDurationMs)/1000), avgW)
		success := padLeft(successRateStyled(agent.SuccessRate), successW)
		cost := "—"
		if agent.TotalCostUSD > 0 {
			cost = fmt.Sprintf("$%.2f", agent.TotalCostUSD)
		}
		cost = padLeft(cost, costW)
		if selected {
			cursor = StyleFocusedArrow.Render("▶") + " "
			name = StyleSelectedRow.Render(name)
			workers = StyleSelectedRow.Render(workers)
			status = StyleSelectedRow.Render(status)
			completed = StyleSelectedRow.Render(completed)
			avg = StyleSelectedRow.Render(avg)
			success = StyleSelectedRow.Render(success)
			cost = StyleSelectedRow.Render(cost)
		}
		b.WriteString(cursor + " " + name + "    " + padLeft(workers, workersW) + " " + status + "  " + completed + avg + "  " + success + "  " + padLeft(cost, costW) + "\n")
	}
	return a.box("AGENTS", b.String(), height)
}

func (a *App) renderAgentDetail(ag *AgentsTab, height int) string {
	d := ag.Detail
	if d == nil {
		return a.box("AGENT DETAILS", StyleMuted.Render("No details")+"\n", height)
	}

	lastEnded := "—"
	if d.LastTaskEndedAt != nil {
		lastEnded = d.LastTaskEndedAt.Format("2006-01-02 15:04:05")
	}
	completed := d.CompletedCount
	// Derive succeeded/failed from the success rate (best-effort from aggregates).
	succeeded := int(float64(completed)*d.SuccessRate + 0.5)
	failed := completed - succeeded
	if failed < 0 {
		failed = 0
	}

	var b strings.Builder
	row := func(k, v string) {
		b.WriteString("  " + StyleLabel.Render(pad(k+":", 16)) + " " + v + "\n")
	}
	row("Agent", StyleValueStrong.Render(d.ID))
	if d.Description != "" {
		row("Description", d.Description)
	}
	row("Status", agentStatusText(d.Status))

	busy := d.RunningCount
	avail := d.MaxWorkers - busy
	if avail < 0 {
		avail = 0
	}
	row("Workers", fmt.Sprintf("%d/%d busy  (%d available)", busy, d.MaxWorkers, avail))

	running := "0"
	if d.RunningCount > 0 {
		running = StyleWarning.Render(fmt.Sprintf("%d ⟳", d.RunningCount))
	}
	row("Running now", running)
	row("Current task", valueOr(d.CurrentTask, "—"))

	if d.RunnerType != "" {
		row("Runner", d.RunnerType)
	}
	if d.Model != "" {
		row("Model", d.Model)
	}
	if d.SoulFile != "" {
		row("Soul file", d.SoulFile)
	}
	if len(d.Skills) > 0 {
		row("Skills", strings.Join(d.Skills, ", "))
	}
	// Related-files hint: how many files (soul + skills) can be inspected, and
	// the key that opens the navigable list.
	if nFiles := len(a.buildAgentFiles(d)); nFiles > 0 {
		hint := fmt.Sprintf("%d file(s) — press %s to browse", nFiles, StyleAccent.Render("f"))
		row("Related files", StyleMuted.Render(hint))
	}
	if d.SourceName != "" || d.SourceEmail != "" {
		id := d.SourceName
		if d.SourceEmail != "" {
			if id != "" {
				id += " <" + d.SourceEmail + ">"
			} else {
				id = d.SourceEmail
			}
		}
		row("Git identity", id)
	}
	if d.PID > 0 {
		row("PID", fmt.Sprintf("%d", d.PID))
		hb := "—"
		if d.HeartbeatAt != nil {
			ago := time.Since(*d.HeartbeatAt).Round(time.Second)
			hb = fmt.Sprintf("%s ago", ago)
		}
		row("Heartbeat", hb)
	}

	b.WriteString("\n")
	row("Completed", StyleSuccess.Render(fmt.Sprintf("%d", completed)))
	row("Succeeded", StyleSuccess.Render(fmt.Sprintf("%d ✓", succeeded)))
	row("Failed", StyleError.Render(fmt.Sprintf("%d ✗", failed)))
	row("Success rate", successRateStyled(d.SuccessRate))
	row("Queued", fmt.Sprintf("%d", d.QueuedCount))
	b.WriteString("\n")
	row("Avg duration", fmt.Sprintf("%.1fs", float64(d.AvgDurationMs)/1000))
	row("Last task", lastEnded)
	if d.TotalTokens > 0 || d.TotalCostUSD > 0 {
		b.WriteString("\n")
		if d.TotalTokens > 0 {
			row("Total tokens", fmt.Sprintf("%d", d.TotalTokens))
		}
		if d.TotalCostUSD > 0 {
			row("Total cost", fmt.Sprintf("$%.4f", d.TotalCostUSD))
		}
	}
	return a.box("AGENT DETAILS — "+d.ID, b.String(), height)
}

// buildAgentFiles returns the files related to an agent — its soul prompt and
// each configured skill — with paths resolved relative to the working directory
// (where the dashboard runs, the same root the dispatcher reads them from).
// Files that cannot be found are kept in the list and flagged Missing so the
// user sees a misconfiguration rather than a silently shorter list.
func (a *App) buildAgentFiles(d *AgentStatus) []AgentFileItem {
	if d == nil {
		return nil
	}
	var files []AgentFileItem
	if d.SoulFile != "" {
		files = append(files, AgentFileItem{
			Kind:    "soul",
			Name:    filepath.Base(d.SoulFile),
			Path:    d.SoulFile,
			Missing: !fileExists(d.SoulFile),
		})
	}
	for _, skill := range d.Skills {
		path := filepath.Join(".claude", "skills", skill, "SKILL.md")
		files = append(files, AgentFileItem{
			Kind:    "skill",
			Name:    skill,
			Path:    path,
			Missing: !fileExists(path),
		})
	}
	return files
}

// fileExists reports whether path names a readable regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// openSelectedAgentFile reads the file under the cursor in the Files list into
// the model and switches to the content viewer. Read errors are stored on the
// model (FileErr) and rendered in place rather than aborting the view switch.
func (a *App) openSelectedAgentFile() {
	ag := a.model.agentsTab
	if ag == nil || ag.FilesIdx < 0 || ag.FilesIdx >= len(ag.Files) {
		return
	}
	f := ag.Files[ag.FilesIdx]
	ag.FileName = f.Name
	ag.FilePath = f.Path
	ag.FileContent = ""
	ag.FileErr = ""
	ag.FileScroll = 0
	ag.FileRaw = false
	ag.invalidateFileLines()
	data, err := os.ReadFile(f.Path)
	if err != nil {
		ag.FileErr = err.Error()
	} else {
		ag.FileContent = string(data)
	}
	ag.View = AgentViewFileContent
}

// invalidateFileLines drops the memoized display lines so the next
// agentFileLines call re-renders (after a toggle, resize, or file change).
func (ag *AgentsTab) invalidateFileLines() {
	ag.fileLines = nil
	ag.fileLinesValid = false
}

// isMarkdownFile reports whether path looks like a markdown document by extension.
func isMarkdownFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	default:
		return false
	}
}

// agentFileLines returns the open file's display lines wrapped to the box inner
// width. Markdown files are rendered with glamour unless raw mode is on; other
// files (or a render failure) fall back to plain wrapping. The result is
// memoized per (width, raw mode) so glamour runs once, not on every keystroke.
func (a *App) agentFileLines() []string {
	ag := a.model.agentsTab
	if ag == nil {
		return nil
	}
	inner := a.model.width - 2
	if inner < 1 {
		inner = 1
	}
	if ag.fileLinesValid && ag.fileLinesWidth == inner && ag.fileLinesRaw == ag.FileRaw {
		return ag.fileLines
	}

	content := sanitizeForTUI(ag.FileContent)
	var lines []string
	if !ag.FileRaw && isMarkdownFile(ag.FilePath) {
		if rendered, err := renderMarkdown(content, inner); err == nil {
			// glamour emits its own wrapping + a trailing blank line; trim it and
			// hard-wrap any over-long line so the box border stays aligned.
			lines = clampToWidth(strings.Split(strings.TrimRight(rendered, "\n"), "\n"), inner)
		}
	}
	if lines == nil {
		lines = wrapPlain(content, inner)
	}

	ag.fileLines = lines
	ag.fileLinesWidth = inner
	ag.fileLinesRaw = ag.FileRaw
	ag.fileLinesValid = true
	return lines
}

// renderMarkdown renders markdown to ANSI-styled terminal text wrapped to width.
// It uses glamour's auto style so it adapts to the terminal background.
func renderMarkdown(src string, width int) (string, error) {
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return "", err
	}
	return r.Render(src)
}

// clampToWidth hard-wraps any line whose visible width exceeds w, splitting on
// runes while preserving ANSI styling so colored markdown never overflows the
// box border. Lines already within width pass through untouched.
func clampToWidth(lines []string, w int) []string {
	if w < 1 {
		w = 1
	}
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if lipgloss.Width(ln) <= w {
			out = append(out, ln)
			continue
		}
		for lipgloss.Width(ln) > w {
			out = append(out, ansi.Truncate(ln, w, ""))
			ln = ansi.TruncateLeft(ln, w, "")
		}
		out = append(out, ln)
	}
	return out
}

func (a *App) renderAgentFiles(ag *AgentsTab, height int) string {
	name := "—"
	if ag.Detail != nil {
		name = ag.Detail.ID
	}
	if len(ag.Files) == 0 {
		return a.box("AGENT FILES — "+name, StyleMuted.Render("No soul or skill files configured for this agent.")+"\n", height)
	}

	const (
		cursorW = 2
		kindW   = 6
	)
	inner := a.model.width - 2
	nameW := inner - cursorW - kindW - 4
	if nameW < 10 {
		nameW = 10
	}

	var b strings.Builder
	for i, f := range ag.Files {
		selected := i == ag.FilesIdx
		kind := pad(f.Kind, kindW)
		label := f.Name
		if f.Missing {
			label += "  " + StyleError.Render("(missing)")
		}
		label = pad(truncate(label, nameW), nameW)
		path := StyleMuted.Render(f.Path)
		cursor := "  "
		if selected {
			cursor = StyleFocusedArrow.Render("▶") + " "
			kind = StyleSelectedRow.Render(kind)
			label = StyleSelectedRow.Render(label)
		} else {
			kind = StyleAccent.Render(kind)
		}
		b.WriteString(cursor + " " + kind + "  " + label + "  " + path + "\n")
	}
	return a.box("AGENT FILES — "+name, b.String(), height)
}

func (a *App) renderAgentFileContent(ag *AgentsTab, height int) string {
	title := "FILE — " + valueOr(ag.FileName, ag.FilePath)
	if ag.FileRaw && isMarkdownFile(ag.FilePath) {
		title += " (raw)"
	}
	if ag.FileErr != "" {
		body := StyleError.Render("Could not read "+ag.FilePath+":") + "\n" + StyleMuted.Render(ag.FileErr) + "\n"
		return a.box(title, body, height)
	}

	lines := a.agentFileLines()
	if len(lines) == 0 {
		return a.box(title, StyleMuted.Render("(empty file)")+"\n", height)
	}

	rows := height - 2 // top + bottom borders
	if rows < 1 {
		rows = 1
	}
	start := clampScroll(ag.FileScroll, len(lines))
	end := start + rows
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(lines[i] + "\n")
	}
	return a.box(title, b.String(), height)
}

func (a *App) renderAgentActivity(ag *AgentsTab, height int) string {
	name := "—"
	if ag.Detail != nil {
		name = ag.Detail.ID
	} else if id, ok := a.selectedAgentID(); ok {
		name = id
	}
	if len(ag.Activity) == 0 {
		return a.box("AGENT ACTIVITY — "+name, StyleMuted.Render("No tasks recorded for this agent yet.")+"\n", height)
	}

	const (
		cursorW = 2
		numW    = 10
		statusW = 8
		durW    = 8
		whenW   = 11
	)
	inner := a.model.width - 2
	titleW := inner - cursorW - numW - statusW - durW - whenW - 5
	if titleW < 10 {
		titleW = 10
	}

	var b strings.Builder
	header := pad("", cursorW) + " " + pad("#", numW) + " " + pad("TASK", titleW) + " " + pad("STATUS", statusW) + " " + pad("DURATION", durW) + " " + "WHEN"
	b.WriteString(StyleTableHeader.Render(header) + "\n")

	rows := height - 3 // borders + header
	if rows < 1 {
		rows = 1
	}
	// Window the list so the cursor row stays visible.
	start := ag.ActivityIdx - rows/2
	if start > len(ag.Activity)-rows {
		start = len(ag.Activity) - rows
	}
	if start < 0 {
		start = 0
	}
	end := start + rows
	if end > len(ag.Activity) {
		end = len(ag.Activity)
	}
	for i := start; i < end; i++ {
		it := ag.Activity[i]
		selected := i == ag.ActivityIdx
		num := pad(truncate(valueOr(it.Number, "—"), numW), numW)
		title := pad(truncate(valueOr(it.Title, it.TaskID), titleW), titleW)
		status := taskStatusBadge(it.Status)
		dur := "—"
		if it.Duration > 0 {
			dur = it.Duration.Round(time.Second).String()
		}
		durStr := pad(dur, durW)
		when := taskWhen(it)
		cursor := "  "
		if selected {
			cursor = StyleFocusedArrow.Render("▶") + " "
			num = StyleSelectedRow.Render(num)
			title = StyleSelectedRow.Render(title)
			status = StyleSelectedRow.Render(status)
			durStr = StyleSelectedRow.Render(durStr)
			when = StyleSelectedRow.Render(when)
		} else {
			num = StyleAccent.Render(num)
			when = StyleMuted.Render(when)
		}
		b.WriteString(cursor + " " + num + " " + title + " " + status + " " + durStr + " " + when + "\n")
	}
	return a.box("AGENT ACTIVITY — "+name, b.String(), height)
}

// renderAgentTaskLogs shows the per-task logs of the task drilled into from an
// agent's activity list — the same view the Tasks tab offers.
func (a *App) renderAgentTaskLogs(ag *AgentsTab, height int) string {
	title := "TASK LOGS — " + valueOr(ag.LogsTaskID, "")
	if ag.LogsTask != nil {
		title = taskDetailLabel(ag.LogsTask)
	}
	if len(ag.TaskLogs) == 0 {
		body := StyleMuted.Render("No logs recorded for this task.")
		if a.model.loading {
			body = a.loadingLine("Loading logs…")
		}
		return a.box(title, body+"\n", height)
	}

	lines := a.agentTaskLogLines()
	rows := height - 2 // top + bottom borders
	if rows < 1 {
		rows = 1
	}
	// Follow mode: keep the viewport pinned to the tail; scrolling down to the
	// last page re-engages it (matching the G/end semantics).
	maxStart := pinToTail(len(lines), rows)
	if ag.TaskLogIdx >= maxStart {
		ag.TaskLogFollow = true
	}
	if ag.TaskLogFollow {
		ag.TaskLogIdx = maxStart
	}
	start := clampScroll(ag.TaskLogIdx, len(lines))
	end := start + rows
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(lines[i] + "\n")
	}
	return a.box(title, b.String(), height)
}

// agentStatusText returns a readable label for an agent status.
func agentStatusText(s string) string {
	switch s {
	case "active":
		return StyleSuccess.Render("running")
	case "stale":
		return StyleWarning.Render("stale")
	case "zombie":
		return StyleError.Render("zombie")
	case "error":
		return StyleError.Render("error")
	case "idle":
		return StyleMuted.Render("idle")
	default:
		return valueOr(s, "—")
	}
}

// successRateStyled colors a success rate: green high, yellow mid, red low.
func successRateStyled(rate float64) string {
	txt := fmt.Sprintf("%.0f%%", rate*100)
	switch {
	case rate >= 0.9:
		return StyleSuccess.Render(txt)
	case rate >= 0.6:
		return StyleWarning.Render(txt)
	default:
		return StyleError.Render(txt)
	}
}

func (a *App) renderLogsTab(height int) string {
	l := a.model.logsTab
	if l == nil || len(l.Logs) == 0 {
		body := StyleMuted.Render("No logs yet")
		if a.model.loading {
			body = a.loadingLine("Loading logs…")
		}
		return a.box("LOGS", body+"\n", height)
	}

	lines := a.logVisualLines()

	rows := height - 2 // top + bottom borders
	if rows < 1 {
		rows = 1
	}
	// Follow mode: keep the viewport pinned to the tail; scrolling down to the
	// last page re-engages it (matching the end-key semantics).
	maxStart := pinToTail(len(lines), rows)
	if l.Scrolled >= maxStart {
		l.Follow = true
	}
	if l.Follow {
		l.Scrolled = maxStart
	}
	start := l.Scrolled
	if start > len(lines)-1 {
		start = len(lines) - 1
	}
	if start < 0 {
		start = 0
	}
	end := start + rows
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(lines[i] + "\n")
	}
	return a.box("LOGS", b.String(), height)
}

// logVisualLines renders the service logs into display lines. When Wrap is on,
// long messages break across lines; otherwise each entry is a single line that
// can be scrolled horizontally via HScroll.
func (a *App) logVisualLines() []string {
	l := a.model.logsTab
	const prefixWidth = 15 // "15:04:05" + space + 5-char level + space
	indent := strings.Repeat(" ", prefixWidth)
	msgWidth := a.model.width - 2 - prefixWidth // inner minus prefix
	if msgWidth < 10 {
		msgWidth = 10
	}

	var out []string
	for _, entry := range l.Logs {
		ts := StyleMuted.Render(entry.Timestamp.Format("15:04:05"))
		level := levelStyle(entry.Level).Render(fmt.Sprintf("%-5s", entry.Level))
		msg := entry.Message

		if l.Wrap {
			wrapped := a.logMessageLines(msg, msgWidth)
			if len(wrapped) == 0 {
				wrapped = []string{""}
			}
			for j, w := range wrapped {
				if j == 0 {
					out = append(out, fmt.Sprintf("%s %s %s", ts, level, w))
				} else {
					out = append(out, indent+w)
				}
			}
		} else {
			// Single line; scroll the message horizontally by HScroll columns.
			flat := strings.ReplaceAll(strings.ReplaceAll(msg, "\t", "    "), "\n", " ")
			out = append(out, fmt.Sprintf("%s %s %s", ts, level, hScroll(flat, l.HScroll)))
		}
	}
	return out
}

// hScroll drops the first n runes of s (horizontal scroll for plain text).
func hScroll(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if n >= len(r) {
		return ""
	}
	return string(r[n:])
}

func (a *App) renderFooter() string {
	if a == nil || a.model == nil {
		return ""
	}
	updated := "—"
	if !a.model.lastRefresh.IsZero() {
		updated = time.Since(a.model.lastRefresh).Round(time.Second).String() + " ago"
	}
	var seg []string
	for _, f := range a.footerKeys() {
		seg = append(seg, StyleFooterKey.Render(" "+f.k+" ")+" "+StyleFooterLbl.Render(f.d))
	}
	left := strings.Join(seg, StyleFooterDim.Render("  "))
	right := StyleFooterDim.Render(a.footerStatus(updated))

	gap := a.model.width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		gap = 1
	}
	line := " " + left + strings.Repeat(" ", gap) + right
	return fitLine(line, a.model.width)
}

// fkey is one key/label pair shown in the footer.
type fkey struct{ k, d string }

// footerKeys returns the navigation hints for the current tab + sub-view.
func (a *App) footerKeys() []fkey {
	switch a.model.ActiveTab() {
	case "Tasks":
		if t := a.model.tasksTab; t != nil {
			switch t.View {
			case TaskViewDetail:
				return []fkey{{"esc", "back"}, {"↑/↓", "scroll"}, {"l", "logs"}, {"o", "open"}, {"p", "open PR"}, {"t", "transcript"}, {"R", "restart"}, {"C", "clear"}, {"r", "reload"}, {"q", "quit"}}
			case TaskViewLogs:
				return []fkey{{"esc", "back"}, {"d", "details"}, {"↑/↓", "scroll"}, {"o", "open"}, {"p", "open PR"}, {"t", "transcript"}, {"C", "clear"}, {"q", "quit"}}
			case TaskViewTranscript:
				label := "raw"
				if t.TranscriptRaw {
					label = "rendered"
				}
				return []fkey{{"esc", "back"}, {"↑/↓", "scroll"}, {"end", "follow"}, {"t", label}, {"r", "reload"}, {"q", "quit"}}
			case TaskViewWorkflow:
				if t.WorkflowShowLogs {
					return []fkey{{"esc", "back"}, {"↑/↓", "scroll"}, {"q", "quit"}}
				}
				keys := []fkey{{"↑/↓", "step"}, {"enter/l", "logs"}, {"t", "transcript"}}
				if len(t.WorkflowInstances) > 1 {
					keys = append(keys, fkey{"[ ]", "workflow"})
				}
				return append(keys, fkey{"r", "refresh"}, fkey{"X", "stop"}, fkey{"R", "restart"}, fkey{"esc", "back"}, fkey{"q", "quit"})
			}
		}
		return []fkey{{"↑/↓", "select"}, {"enter", "workflow"}, {"d", "details"}, {"o", "open"}, {"p", "open PR"}, {"t", "transcript"}, {"R", "restart"}, {"C", "clear"}, {"tab", "switch"}, {"q", "quit"}}
	case "Workflows":
		if wt := a.model.workflowsTab; wt != nil && wt.Focus == WorkflowsViewSteps {
			return []fkey{{"↑/↓", "step"}, {"esc/←", "back"}, {"tab", "next tab"}, {"q", "quit"}}
		}
		return []fkey{{"↑/↓", "workflow"}, {"enter/→", "steps"}, {"tab", "next tab"}, {"q", "quit"}}
	case "Agents":
		if ag := a.model.agentsTab; ag != nil {
			switch ag.View {
			case AgentViewDetail:
				return []fkey{{"esc", "back"}, {"f", "files"}, {"l", "activity"}, {"m", "model"}, {"r", "runner"}, {"w", "workers"}, {"q", "quit"}}
			case AgentViewActivity:
				return []fkey{{"esc", "back"}, {"↑/↓", "select"}, {"enter/l", "logs"}, {"o", "open"}, {"p", "open PR"}, {"t", "transcript"}, {"pgup/dn", "page"}, {"q", "quit"}}
			case AgentViewTaskLogs:
				return []fkey{{"esc", "back"}, {"↑/↓", "scroll"}, {"o", "open"}, {"p", "open PR"}, {"t", "transcript"}, {"home/end", "ends"}, {"r", "reload"}, {"q", "quit"}}
			case AgentViewFiles:
				return []fkey{{"esc", "back"}, {"↑/↓", "select"}, {"enter/l", "view"}, {"q", "quit"}}
			case AgentViewFileContent:
				keys := []fkey{{"esc", "back"}, {"↑/↓", "scroll"}, {"pgup/dn", "page"}, {"home/end", "ends"}}
				if isMarkdownFile(ag.FilePath) {
					label := "raw"
					if ag.FileRaw {
						label = "rendered"
					}
					keys = append(keys, fkey{"t", label})
				}
				return append(keys, fkey{"q", "quit"})
			}
		}
		return []fkey{{"↑/↓", "select"}, {"enter/l", "activity"}, {"d", "details"}, {"tab", "switch"}, {"r", "refresh"}, {"q", "quit"}}
	case "Logs":
		wrap := "wrap off"
		if a.model.logsTab != nil && a.model.logsTab.Wrap {
			wrap = "wrap on"
		}
		return []fkey{{"w", wrap}, {"←/→", "scroll"}, {"↑/↓", "lines"}, {"pgup/dn", "page"}, {"home/end", "ends"}, {"tab", "switch"}, {"q", "quit"}}
	default: // Overview
		return []fkey{{"tab", "next"}, {"⇧tab", "prev"}, {"r", "refresh"}, {"q", "quit"}}
	}
}

// footerStatus is the right-aligned status: last refresh + any scroll position.
func (a *App) footerStatus(updated string) string {
	pos := ""
	switch a.model.ActiveTab() {
	case "Tasks":
		if t := a.model.tasksTab; t != nil {
			switch t.View {
			case TaskViewLogs:
				if n := len(a.taskLogLines()); n > 0 {
					pos = fmt.Sprintf("line %d/%d   ", t.LogScroll+1, n)
				}
			case TaskViewDetail:
				if n := len(a.taskDetailLines(t)); n > 0 {
					pos = fmt.Sprintf("line %d/%d   ", t.DetailScroll+1, n)
				}
			}
		}
	case "Logs":
		if a.model.logsTab != nil {
			if n := len(a.logVisualLines()); n > 0 {
				pos = fmt.Sprintf("line %d/%d   ", a.model.logsTab.Scrolled+1, n)
			}
		}
	case "Agents":
		if ag := a.model.agentsTab; ag != nil {
			switch ag.View {
			case AgentViewActivity:
				if n := len(ag.Activity); n > 0 {
					pos = fmt.Sprintf("task %d/%d   ", ag.ActivityIdx+1, n)
				}
			case AgentViewTaskLogs:
				if n := len(a.agentTaskLogLines()); n > 0 {
					pos = fmt.Sprintf("line %d/%d   ", ag.TaskLogIdx+1, n)
				}
			case AgentViewFiles:
				if n := len(ag.Files); n > 0 {
					pos = fmt.Sprintf("file %d/%d   ", ag.FilesIdx+1, n)
				}
			case AgentViewFileContent:
				if n := len(a.agentFileLines()); n > 0 {
					pos = fmt.Sprintf("line %d/%d   ", ag.FileScroll+1, n)
				}
			}
		}
	}
	return pos + "updated " + updated
}

// ── helpers ──────────────────────────────────────────────────────────────────

func levelStyle(level string) lipgloss.Style {
	switch level {
	case "ERROR":
		return StyleError
	case "WARN":
		return StyleWarning
	case "INFO":
		return StyleInfo
	default:
		return StyleMuted
	}
}

func valueOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// fmtTokensShort renders a token count compactly (842, 1.2k, 3.4M); "—" for zero.
func fmtTokensShort(n int) string {
	switch {
	case n <= 0:
		return "—"
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// tsTimeFmt is the fixed-width timestamp shown in step/instance table cells:
// month-day plus wall-clock, so a run that spans days (or midnight) is unambiguous.
const tsTimeFmt = "01-02 15:04:05"

// tsCell renders a single timestamp table cell, or "—" when absent.
func tsCell(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Format(tsTimeFmt)
}

// datedSpan renders "01-02 15:04:05 → 01-02 15:04:05" for a start/end pair, used
// in the one-line workflow rollup: an unfinished span ends with "…", an unstarted
// one is "—".
func datedSpan(start, end *time.Time) string {
	if start == nil {
		return "—"
	}
	e := "…"
	if end != nil {
		e = end.Format(tsTimeFmt)
	}
	return start.Format(tsTimeFmt) + " → " + e
}

// pad right-pads s with spaces to a minimum display width.
func pad(s string, width int) string {
	gap := width - lipgloss.Width(s)
	if gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

func padLeft(s string, width int) string {
	gap := width - lipgloss.Width(s)
	if gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}

// taskStatusBadge renders a colored, fixed-width status label. It handles both
// legacy execution statuses (success/failed/running, used by the Agents tab) and
// InternalTask lifecycle states (registered/running/approval_waiting/done/failed,
// used by the Tasks tab since Phase 9). The longer internal states are shown with
// short synonyms so the badge stays within its 8-char column.
func taskStatusBadge(status string) string {
	label, style := status, StyleMuted
	switch status {
	case "success", "done":
		style = StyleSuccess
	case "failed":
		style = StyleError
	case "running":
		style = StyleWarning
	case "approval_waiting":
		label, style = "approval", StyleWarning
	case "registered":
		label, style = "queued", StyleMuted
	}
	return style.Render(pad(valueOr(label, "—"), 8))
}

// taskWhen returns a short "when" description for a task list row.
// lastUpdate returns the most recent timestamp for a task — CompletedAt if
// set, otherwise StartedAt. Used for "updated" sort.
func lastUpdate(it TaskItem) *time.Time {
	if it.CompletedAt != nil {
		return it.CompletedAt
	}
	return it.StartedAt
}

func taskWhen(it TaskItem) string {
	if it.Status == "running" && it.StartedAt != nil {
		return time.Since(*it.StartedAt).Round(time.Second).String() + " ago"
	}
	if it.CompletedAt != nil {
		return it.CompletedAt.Format("01-02 15:04")
	}
	if it.StartedAt != nil {
		return it.StartedAt.Format("01-02 15:04")
	}
	return "—"
}

func truncate(s string, max int) string {
	if max < 1 {
		max = 1
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string([]rune(s)[:max-1]) + "…"
}

// wfMonitorPanelWidths returns the step-list (left) and detail/log (right)
// panel widths of the workflow monitor. Shared with logMsgWidth so the STEP
// LOGS panel wraps log lines to the panel it renders in instead of the full
// terminal width (which fitLine would then clip on the right).
func (a *App) wfMonitorPanelWidths() (leftW, rightW int) {
	totalW := a.model.width - 2
	leftW = totalW * 2 / 5
	rightW = totalW - leftW - 1
	if leftW < 20 {
		leftW = 20
	}
	return leftW, rightW
}

// renderWorkflowMonitor renders the live workflow instance monitor.
// Layout: left panel (step list) | right panel (step detail or logs).
func (a *App) renderWorkflowMonitor(t *TasksTab, height int) string {
	inst := t.WorkflowInstance
	if inst == nil {
		return a.box("WORKFLOW MONITOR", StyleMuted.Render("No workflow instance")+"\n", height)
	}

	leftW, rightW := a.wfMonitorPanelWidths()

	label := "WORKFLOW  " + StyleValueStrong.Render(inst.Workflow) + "  " + wfInstanceBadge(inst.State)
	// When the task fanned out to several workflows, show this one's chronological
	// position (oldest = 1) and a hint that [ / ] switch between them.
	if n := len(t.WorkflowInstances); n > 1 {
		label += "   " + StyleMuted.Render(fmt.Sprintf("workflow %d/%d · [ ] switch", n-t.WorkflowInstanceIdx, n))
	}

	// ── left panel: step list ───────────────────────────────────────────────
	var left strings.Builder
	if inst.Message != "" {
		left.WriteString(StyleWarning.Render("  ⏸ "+inst.Message) + "\n")
	}
	bodyRows := height - 4
	if bodyRows < 1 {
		bodyRows = 1
	}

	// Header row
	hdr := pad("", 2) + pad("STEP", 18) + " " + pad("AGENT", 14) + " " + pad("STATE", 10) + " " + "DUR"
	left.WriteString(StyleTableHeader.Render(fitLine(hdr, leftW)) + "\n")
	bodyRows--

	// Windowed step list
	start := t.WorkflowStepIdx - bodyRows/2
	if start > len(inst.Steps)-bodyRows {
		start = len(inst.Steps) - bodyRows
	}
	if start < 0 {
		start = 0
	}
	end := start + bodyRows
	if end > len(inst.Steps) {
		end = len(inst.Steps)
	}

	for i := start; i < end; i++ {
		s := inst.Steps[i]
		selected := i == t.WorkflowStepIdx
		glyph := wfStepGlyph(s.State)
		stepName := truncate(s.StepID, 17)
		agent := truncate(valueOr(s.Agent, "—"), 13)
		state := truncate(s.State, 10)
		dur := truncate(s.Duration, 8)

		row := glyph + " " + pad(stepName, 17) + " " + pad(agent, 13) + "   " + pad(state, 10) + " " + StyleMuted.Render(dur)
		if selected {
			row = StyleSelectedRow.Render(fitLine("  "+stepName+" "+agent+"   "+state+" "+dur, leftW))
			row = StyleFocusedArrow.Render("▶") + " " + StyleSelectedRow.Render(fitLine(stepName+" "+agent+"   "+state+" "+dur, leftW-2))
		}
		left.WriteString(fitLine(row, leftW) + "\n")
	}

	// ── right panel: step detail or logs ───────────────────────────────────
	var right strings.Builder
	if t.WorkflowShowLogs {
		// Log panel.
		right.WriteString(StyleTableHeader.Render(fitLine("STEP LOGS", rightW)) + "\n")
		lines := a.wfStepLogLines()
		logRows := height - 4
		if logRows < 1 {
			logRows = 1
		}
		// Follow mode: keep the panel pinned to the tail; scrolling down to the
		// last page re-engages it (matching the G/end semantics).
		maxStart := pinToTail(len(lines), logRows)
		if t.WorkflowLogScroll >= maxStart {
			t.WorkflowLogFollow = true
		}
		if t.WorkflowLogFollow {
			t.WorkflowLogScroll = maxStart
		}
		ls := t.WorkflowLogScroll
		if ls > len(lines)-1 {
			ls = len(lines) - 1
		}
		if ls < 0 {
			ls = 0
		}
		le := ls + logRows
		if le > len(lines) {
			le = len(lines)
		}
		for i := ls; i < le; i++ {
			right.WriteString(fitLine(lines[i], rightW) + "\n")
		}
		if len(lines) == 0 {
			if a.model.loading {
				right.WriteString(a.loadingLine("Loading logs…") + "\n")
			} else {
				right.WriteString(StyleMuted.Render("No logs for this step.") + "\n")
			}
		}
	} else if t.WorkflowStepIdx < len(inst.Steps) {
		// Detail panel for selected step.
		s := inst.Steps[t.WorkflowStepIdx]
		row2 := func(k, v string) {
			right.WriteString("  " + StyleLabel.Render(pad(k+":", 12)) + " " + v + "\n")
		}
		right.WriteString(StyleTableHeader.Render(fitLine(" "+s.StepID, rightW)) + "\n")
		row2("State", wfStepGlyph(s.State)+" "+wfStateStyle(s.State).Render(s.State))
		row2("Agent", valueOr(s.Agent, "—"))
		row2("Duration", valueOr(s.Duration, "—"))
		if s.Cached {
			row2("Cache", StyleMuted.Render("skipped (cached)"))
		}
		right.WriteString("\n")

		if s.TotalTokens > 0 {
			row2("Tokens", fmt.Sprintf("%d in / %d out / %d total", s.InputTokens, s.OutputTokens, s.TotalTokens))
			if s.CacheCreationTokens > 0 || s.CacheReadTokens > 0 {
				row2("Cache", fmt.Sprintf("%d write / %d read", s.CacheCreationTokens, s.CacheReadTokens))
			}
			row2("Cost", fmt.Sprintf("$%.5f", s.CostUSD))
			row2("Turns", fmt.Sprintf("%d turns / %d calls", s.NumTurns, s.NumToolCalls))
			right.WriteString("\n")
		}

		if s.Summary != "" {
			right.WriteString("  " + StyleLabel.Render("Summary:") + "\n")
			for _, line := range wrapPlain(s.Summary, rightW-4) {
				right.WriteString("    " + StyleMuted.Render(line) + "\n")
			}
			right.WriteString("\n")
		}

		if s.Output != "" {
			right.WriteString("  " + StyleLabel.Render("Output:") + "\n")
			outputLines := wrapPlain(s.Output, rightW-4)
			maxOut := height - lipgloss.Height(right.String()) - 2
			if maxOut < 1 {
				maxOut = 1
			}
			for i, line := range outputLines {
				if i >= maxOut {
					right.WriteString("    " + StyleMuted.Render("…") + "\n")
					break
				}
				right.WriteString("    " + line + "\n")
			}
		}

		if s.State == db.StepStateRunning || s.State == db.StepStatePassed || s.State == db.StepStateFailed || s.State == db.StepStateInterrupted {
			right.WriteString("\n  " + StyleMuted.Render("enter/l: logs  X: stop workflow  R: restart workflow") + "\n")
		}
	} else {
		right.WriteString(StyleMuted.Render("  Select a step") + "\n")
	}

	// Render side-by-side within a single box by stitching lines.
	leftLines := strings.Split(strings.TrimRight(left.String(), "\n"), "\n")
	rightLines := strings.Split(strings.TrimRight(right.String(), "\n"), "\n")
	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}

	sep := StyleBorder.Render("│")
	var body strings.Builder
	for i := 0; i < maxLines; i++ {
		l := ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		r := ""
		if i < len(rightLines) {
			r = rightLines[i]
		}
		body.WriteString(fitLine(l, leftW) + sep + fitLine(r, rightW) + "\n")
	}
	return a.box(label, body.String(), height)
}

// renderWorkflowsTab renders the static workflow config navigation tab.
// Layout: left workflow list | right panel with all steps always fully expanded.
// Every step always shows type, ID, agent, depends, if, and prompt — navigation
// just moves the highlight; nothing is hidden or revealed by cursor movement.
func (a *App) renderWorkflowsTab(height int) string {
	wt := a.model.workflowsTab
	if wt == nil || len(wt.Workflows) == 0 {
		return a.box("WORKFLOWS", StyleMuted.Render("No workflows defined in config.")+"\n", height)
	}

	totalW := a.model.width - 2
	leftW := 28
	rightW := totalW - leftW - 1

	// ── left panel: workflow list ───────────────────────────────────────────
	var left strings.Builder
	left.WriteString(StyleTableHeader.Render(fitLine("WORKFLOW", leftW)) + "\n")
	for i, wf := range wt.Workflows {
		selected := i == wt.SelectedIdx
		label := truncate(wf.ID, leftW-4)
		if selected && wt.Focus == WorkflowsViewList {
			left.WriteString(StyleSelectedRow.Render(fitLine(" ▶ "+label, leftW)) + "\n")
		} else if selected {
			left.WriteString(StyleAccent.Render(fitLine(" ● "+label, leftW)) + "\n")
		} else {
			left.WriteString(fitLine("   "+label, leftW) + "\n")
		}
	}

	// ── right panel: all steps always fully expanded ────────────────────────
	// Each step block has fixed height determined by its own data (not cursor),
	// so j/k navigation never shifts or reflows any content.
	var right strings.Builder
	if wt.SelectedIdx >= len(wt.Workflows) {
		// nothing to show
	} else {
		wf := wt.Workflows[wt.SelectedIdx]
		desc := valueOr(wf.Description, "(no description)")

		// Header: 2 lines (title + divider).
		right.WriteString(StyleValueStrong.Render(wf.ID) + "  " + StyleMuted.Render(desc) + "\n")
		right.WriteString(StyleMuted.Render(strings.Repeat("─", rightW)) + "\n")

		if len(wf.Steps) == 0 {
			right.WriteString(StyleMuted.Render("  No steps defined.") + "\n")
		} else {
			// Build all step lines up-front so we can compute scroll.
			type stepBlock struct {
				lines []string
				start int // line index within the body (after the 2-line header)
			}
			blocks := make([]stepBlock, 0, len(wf.Steps))
			cursor := 0

			for i, step := range wf.Steps {
				selected := i == wt.StepIdx && wt.Focus == WorkflowsViewSteps
				var bl []string

				// Line 1: cursor glyph + type badge + ID + agent.
				typeLabel := styleStepType(step.Type)
				headline := typeLabel + " " + StyleValueStrong.Render(step.ID)
				if step.Agent != "" {
					headline += "  " + StyleAccent.Render("→ "+step.Agent)
				}
				if selected {
					bl = append(bl,
						StyleFocusedArrow.Render("▶")+" "+StyleSelectedRow.Render(fitLine(ansi.Strip(headline), rightW-2)))
				} else {
					bl = append(bl, fitLine("  "+headline, rightW))
				}

				// Sub-lines: always rendered, height stable regardless of selection.
				if step.Condition != "" {
					bl = append(bl,
						"    "+StyleMuted.Render("if:      ")+truncate(step.Condition, rightW-16))
				}
				if step.Prompt != "" {
					promptW := rightW - 16
					pLines := wrapPlain(step.Prompt, promptW)
					for j, pl := range pLines {
						if j == 0 {
							bl = append(bl, "    "+StyleMuted.Render("prompt:  ")+pl)
						} else {
							bl = append(bl, "             "+pl)
						}
						if j >= 2 {
							bl = append(bl, "             "+StyleMuted.Render("…"))
							break
						}
					}
				}

				// Blank separator between steps (not after the last one).
				if i < len(wf.Steps)-1 {
					bl = append(bl, "")
				}

				blocks = append(blocks, stepBlock{lines: bl, start: cursor})
				cursor += len(bl)
			}

			// Auto-scroll: keep the selected step visible in the body window.
			// bodyRows = total visible lines minus the 2-line header.
			bodyRows := height - 4 - 2
			if bodyRows < 1 {
				bodyRows = 1
			}
			if wt.Focus == WorkflowsViewSteps && wt.StepIdx < len(blocks) {
				b := blocks[wt.StepIdx]
				stepEnd := b.start + len(b.lines)
				if b.start < wt.StepScroll {
					wt.StepScroll = b.start
				}
				if stepEnd > wt.StepScroll+bodyRows {
					wt.StepScroll = stepEnd - bodyRows
				}
			}
			if wt.StepScroll < 0 {
				wt.StepScroll = 0
			}

			// Flatten and window.
			allLines := make([]string, 0, cursor)
			for _, b := range blocks {
				allLines = append(allLines, b.lines...)
			}
			lo := wt.StepScroll
			hi := lo + bodyRows
			if hi > len(allLines) {
				hi = len(allLines)
			}
			for i := lo; i < hi; i++ {
				right.WriteString(fitLine(allLines[i], rightW) + "\n")
			}

			// Scroll hint when content overflows.
			if len(allLines) > bodyRows {
				hint := fmt.Sprintf("(%d–%d / %d lines  j/k scroll)", lo+1, hi, len(allLines))
				right.WriteString(StyleMuted.Render(fitLine(hint, rightW)) + "\n")
			}
		}
	}

	// Stitch left + right side-by-side.
	leftLines := strings.Split(strings.TrimRight(left.String(), "\n"), "\n")
	rightLines := strings.Split(strings.TrimRight(right.String(), "\n"), "\n")
	maxLines := len(leftLines)
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}

	sep := StyleBorder.Render("│")
	var body strings.Builder
	for i := 0; i < maxLines; i++ {
		l := ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		r := ""
		if i < len(rightLines) {
			r = rightLines[i]
		}
		body.WriteString(fitLine(l, leftW) + sep + fitLine(r, rightW) + "\n")
	}
	return a.box("WORKFLOWS", body.String(), height)
}

func styleStepType(t string) string {
	switch t {
	case "agent", "":
		return StyleInfo.Render("[agent]    ")
	case "approval":
		return StyleWarning.Render("[approval] ")
	case "foreach":
		return StyleAccent.Render("[foreach]  ")
	case "parallel":
		return StyleAccent.Render("[parallel] ")
	case "split":
		return StyleMuted.Render("[split]    ")
	default:
		return StyleMuted.Render("[" + pad(t, 9) + "]")
	}
}

// Run starts the dashboard.
func (a *App) Run() error {
	p := tea.NewProgram(a, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
