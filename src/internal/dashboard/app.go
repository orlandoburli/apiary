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
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
)

// refreshInterval controls how often the active tab re-queries the database.
const refreshInterval = 2 * time.Second

// queryTimeout bounds each database query so a locked DB never blocks the UI.
const queryTimeout = 2 * time.Second

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
	cfg        *config.Config
}

func New(dbConn *db.Client, socketPath string, cfg *config.Config) *App {
	return &App{
		model:      NewModel(),
		dbConn:     dbConn,
		socketPath: socketPath,
		cfg:        cfg,
	}
}

// ── messages ────────────────────────────────────────────────────────────────

// tickMsg fires on the refresh timer.
type tickMsg time.Time

// Each *DataMsg carries the result of a background query. They are produced by
// commands (goroutines) and consumed by Update (event loop), never sharing
// memory with the model directly.
type overviewDataMsg struct{ data OverviewTab }
type tasksDataMsg struct{ items []TaskItem }
type taskDetailMsg struct {
	taskID string
	detail *TaskItem
}
type taskLogsMsg struct {
	taskID string
	logs   []LogEntry
	detail *TaskItem
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

// ── lifecycle ───────────────────────────────────────────────────────────────

// Init initializes the app: enter alt-screen, fetch the first tab, start timer.
func (a *App) Init() tea.Cmd {
	return tea.Batch(
		tea.EnterAltScreen,
		a.fetchActiveTab(),
		tickCmd(),
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

	case tickMsg:
		// Re-query the active tab and schedule the next tick.
		return a, tea.Batch(a.fetchActiveTab(), tickCmd())

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
			a.model.tasksTab.Detail = msg.detail
			a.model.tasksTab.View = TaskViewDetail
		}
		a.model.loading = false

	case taskLogsMsg:
		if a.model.tasksTab != nil {
			a.model.tasksTab.Logs = msg.logs
			a.model.tasksTab.Detail = msg.detail
			a.model.tasksTab.LogScroll = 0
			a.model.tasksTab.View = TaskViewLogs
		}
		a.model.loading = false

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
		if a.model.agentsTab != nil {
			a.model.agentsTab.LogsTaskID = msg.taskID
			a.model.agentsTab.LogsTask = msg.detail
			a.model.agentsTab.TaskLogs = msg.logs
			a.model.agentsTab.TaskLogIdx = 0
			a.model.agentsTab.View = AgentViewTaskLogs
		}
		a.model.loading = false

	case logsDataMsg:
		if a.model.logsTab != nil {
			a.model.logsTab.Logs = msg.logs
		}
		a.model.loading = false
		a.model.lastRefresh = time.Now()
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

	// While a Tasks sub-view (detail/logs) is open, keys are scoped to it.
	if a.model.ActiveTab() == "Tasks" && a.model.tasksTab != nil && a.model.tasksTab.View != TaskViewList {
		return a.handleTaskSubViewKey(key)
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
				return a, a.fetchTaskLogs(id)
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
			if id, ok := a.selectedTaskID(); ok {
				a.model.loading = true
				return a, a.fetchTaskDetail(id)
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
			if ag.TaskLogIdx > 0 {
				ag.TaskLogIdx--
			}
		case "down":
			if ag.TaskLogIdx < lastIndex(len(a.agentTaskLogLines())) {
				ag.TaskLogIdx++
			}
		case "g", "home":
			ag.TaskLogIdx = 0
		case "G", "end":
			ag.TaskLogIdx = lastIndex(len(a.agentTaskLogLines()))
		case "pgup", "ctrl+u":
			ag.TaskLogIdx = clampScroll(ag.TaskLogIdx-a.pageSize(), len(a.agentTaskLogLines()))
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

// handleTaskSubViewKey handles keys while a task detail/logs sub-view is open.
func (a *App) handleTaskSubViewKey(key string) (tea.Model, tea.Cmd) {
	t := a.model.tasksTab
	switch key {
	case "esc", "backspace", "h", "left":
		// Back to the list.
		t.View = TaskViewList
		t.Detail = nil
		t.Logs = nil
		t.LogScroll = 0
	case "d":
		if id, ok := a.selectedTaskID(); ok {
			a.model.loading = true
			return a, a.fetchTaskDetail(id)
		}
	case "l", "enter":
		if id, ok := a.selectedTaskID(); ok {
			a.model.loading = true
			return a, a.fetchTaskLogs(id)
		}
	case "r":
		if id, ok := a.selectedTaskID(); ok {
			a.model.loading = true
			if t.View == TaskViewLogs {
				return a, a.fetchTaskLogs(id)
			}
			return a, a.fetchTaskDetail(id)
		}
	case "up":
		if t.View == TaskViewLogs && t.LogScroll > 0 {
			t.LogScroll--
		}
	case "down":
		if t.View == TaskViewLogs && t.LogScroll < len(a.taskLogLines())-1 {
			t.LogScroll++
		}
	case "g", "home":
		if t.View == TaskViewLogs {
			t.LogScroll = 0
		}
	case "G", "end":
		if t.View == TaskViewLogs {
			t.LogScroll = lastIndex(len(a.taskLogLines()))
		}
	case "pgup", "ctrl+u":
		if t.View == TaskViewLogs {
			t.LogScroll = clampScroll(t.LogScroll-a.pageSize(), len(a.taskLogLines()))
		}
	case "pgdown", "ctrl+d", " ":
		if t.View == TaskViewLogs {
			t.LogScroll = clampScroll(t.LogScroll+a.pageSize(), len(a.taskLogLines()))
		}
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

// selectedTaskID returns the task id under the cursor in the Tasks list.
func (a *App) selectedTaskID() (string, bool) {
	t := a.model.tasksTab
	if t == nil || t.SelectedIdx < 0 || t.SelectedIdx >= len(t.History) {
		return "", false
	}
	return t.History[t.SelectedIdx].TaskID, true
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
		if t.SelectedIdx >= 0 && t.SelectedIdx < len(t.History) {
			u := t.History[t.SelectedIdx].URL
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
		if t.SelectedIdx >= 0 && t.SelectedIdx < len(t.History) {
			return t.History[t.SelectedIdx].TaskID, true
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

// restartTaskCmd sends a force-restart request to the daemon via the IPC socket.
func (a *App) restartTaskCmd(taskID string) tea.Cmd {
	return func() tea.Msg {
		socketPath := a.socketPath
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		}
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		url := fmt.Sprintf("http://apiary/restart/%s", taskID)
		resp, err := client.Post(url, "application/json", nil)
		if err != nil {
			return nil
		}
		resp.Body.Close()
		return nil
	}
}

func (a *App) clearLogsCmd(taskID string) tea.Cmd {
	return func() tea.Msg {
		socketPath := a.socketPath
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		}
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		url := fmt.Sprintf("http://apiary/clearlogs/%s", taskID)
		resp, err := client.Post(url, "application/json", nil)
		if err != nil {
			return nil
		}
		resp.Body.Close()
		return nil
	}
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
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", a.socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

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

	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
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

// fetchActiveTab returns the query command for whichever tab is active. It is
// always called from Update (event loop), so reading the active tab is safe.
func (a *App) fetchActiveTab() tea.Cmd {
	switch a.model.ActiveTab() {
	case "Overview":
		return a.fetchOverview()
	case "Tasks":
		return a.fetchTasks()
	case "Agents":
		return a.fetchAgents()
	case "Logs":
		return a.fetchLogs()
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
			if rows, err := dbConn.GetTaskHistory(ctx, 100); err == nil {
				for _, r := range rows {
					items = append(items, taskItemFromHistory(r))
				}
			}
		}
		return tasksDataMsg{items: items}
	}
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
			if rows, err := dbConn.GetTaskLogs(ctx, taskID, 5000); err == nil {
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

func (a *App) fetchTaskDetail(taskID string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		var detail *TaskItem
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
		}
		return taskDetailMsg{taskID: taskID, detail: detail}
	}
}

func (a *App) fetchTaskLogs(taskID string) tea.Cmd {
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
			if rows, err := dbConn.GetTaskLogs(ctx, taskID, 5000); err == nil {
				for _, l := range rows {
					logs = append(logs, LogEntry{
						Timestamp: l.Timestamp,
						Level:     l.Level,
						Message:   l.Message,
					})
				}
			}
		}
		return taskLogsMsg{taskID: taskID, logs: logs, detail: detail}
	}
}

func (a *App) fetchAgents() tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		agents := make([]AgentStatus, 0)
		if dbConn != nil {
			if rows, err := dbConn.GetAgentStats(ctx); err == nil {
				for _, ag := range rows {
					s := AgentStatus{
						ID:              ag.ID,
						Status:          ag.Status,
						RunningCount:    ag.RunningCount,
						CurrentTask:     ag.CurrentTask,
						QueuedCount:     ag.QueuedCount,
						CompletedCount:  ag.CompletedCount,
						AvgDurationMs:   ag.AvgDurationMs,
						SuccessRate:     ag.SuccessRate,
						LastTaskEndedAt: ag.LastTaskEndedAt,
						PID:             ag.PID,
						HeartbeatAt:     ag.HeartbeatAt,
						HeartbeatCount:  ag.HeartbeatCount,
						TotalCostUSD:    ag.TotalCostUSD,
						TotalTokens:     ag.TotalTokens,
					}
					// Enrich from config
					if a.cfg != nil {
						for _, ac := range a.cfg.Agents {
							if ac.ID == s.ID {
								s.MaxWorkers = ac.MaxWorkers
								s.RunnerType = ac.Runner
								s.Model = ac.Model
								s.SoulFile = ac.SoulFile
								s.Description = ac.Description
								s.SourceName = ac.SourceName
								s.SourceEmail = ac.SourceEmail
								break
							}
						}
						if s.MaxWorkers < 1 {
							s.MaxWorkers = 1
						}
						// Collect all runner IDs and models for the current runner
						s.Runners = make([]string, 0, len(a.cfg.Runners))
						for _, rc := range a.cfg.Runners {
							s.Runners = append(s.Runners, rc.ID)
							if rc.ID == s.RunnerType || (s.RunnerType == "" && rc.ID == a.cfg.DefaultRunner) {
								s.RunnerModels = rc.Models
							}
						}
					}
					agents = append(agents, s)
				}
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
	case "Logs":
		content = a.renderLogsTab(contentHeight)
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
	if a.model.confirmAction == "clear" {
		label = "Clear logs"
		msg = "Are you sure you want to clear all logs for this task?"
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
		var less bool
		switch sortField {
		case "status":
			less = out[i].Status < out[j].Status
		case "agent":
			less = out[i].Agent < out[j].Agent
		case "number":
			less = out[i].Number < out[j].Number
		case "title":
			less = out[i].Title < out[j].Title
		case "updated":
			ti := lastUpdate(out[i])
			tj := lastUpdate(out[j])
			if ti == nil && tj == nil {
				return false
			}
			if ti == nil {
				return t.SortAsc
			}
			if tj == nil {
				return !t.SortAsc
			}
			less = ti.Before(*tj)
		default:
			// time: newest first by default (StartedAt desc)
			ti := out[i].StartedAt
			tj := out[j].StartedAt
			if ti == nil && tj == nil {
				return false
			}
			if ti == nil {
				return t.SortAsc
			}
			if tj == nil {
				return !t.SortAsc
			}
			less = ti.Before(*tj)
		}
		if t.SortAsc {
			return less
		}
		return !less
	})

	return out
}

func (a *App) renderTaskDetail(t *TasksTab, height int) string {
	d := t.Detail
	if d == nil {
		return a.box("TASK DETAILS", StyleMuted.Render("No details")+"\n", height)
	}

	started, completed, dur := "—", "—", "—"
	if d.StartedAt != nil {
		started = d.StartedAt.Format("2006-01-02 15:04:05")
	}
	if d.CompletedAt != nil {
		completed = d.CompletedAt.Format("2006-01-02 15:04:05")
	}
	if d.Duration > 0 {
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
	row("Agent", valueOr(d.Agent, "—"))
	row("Model", valueOr(d.Model, "—"))
	row("Runner", valueOr(d.Runner, "—"))
	row("Attempts", fmt.Sprintf("%d", d.Attempt))
	row("Started", started)
	row("Completed", completed)
	row("Duration", dur)
	row("Tokens", fmt.Sprintf("%d in / %d out / %d total", d.InputTokens, d.OutputTokens, d.TotalTokens))
	row("Turns / Calls", fmt.Sprintf("%d / %d", d.NumTurns, d.NumToolCalls))
	row("Cost", fmt.Sprintf("$%.4f", d.CostUSD))
	if d.URL != "" {
		row("URL", StyleInfo.Render(d.URL))
	}
	if d.Error != "" {
		b.WriteString("\n")
		b.WriteString("  " + StyleError.Render("Error:") + "\n")
		b.WriteString("  " + StyleError.Render(truncate(d.Error, a.model.width-4)) + "\n")
	}
	label := taskDetailLabel(d)
	return a.box(label, b.String(), height)
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
	if len(t.Logs) == 0 {
		label := "TASK LOGS"
		if t.Detail != nil {
			label = taskDetailLabel(t.Detail)
		}
		return a.box(label, StyleMuted.Render("No logs recorded for this task.")+"\n", height)
	}

	lines := a.taskLogLines()

	rows := height - 2 // top + bottom borders
	if rows < 1 {
		rows = 1
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
	return a.logEntryLines(t.Logs)
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
	const prefixWidth = 15                      // "15:04:05" + space + 5-char level + space
	msgWidth := a.model.width - 2 - prefixWidth // inner minus the prefix column
	if msgWidth < 20 {
		msgWidth = 20
	}
	indent := strings.Repeat(" ", prefixWidth)

	var out []string
	for _, entry := range logs {
		ts := StyleMuted.Render(entry.Timestamp.Format("15:04:05"))
		level := levelStyle(entry.Level).Render(fmt.Sprintf("%-5s", entry.Level))
		wrapped := wrapPlain(entry.Message, msgWidth)
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
		return a.box(title, StyleMuted.Render("No logs recorded for this task.")+"\n", height)
	}

	lines := a.agentTaskLogLines()
	rows := height - 2 // top + bottom borders
	if rows < 1 {
		rows = 1
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
		return a.box("LOGS", StyleMuted.Render("No logs yet")+"\n", height)
	}

	lines := a.logVisualLines()

	rows := height - 2 // top + bottom borders
	if rows < 1 {
		rows = 1
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
			wrapped := wrapPlain(msg, msgWidth)
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
				return []fkey{{"esc", "back"}, {"l", "logs"}, {"o", "open"}, {"R", "restart"}, {"C", "clear"}, {"r", "reload"}, {"q", "quit"}}
			case TaskViewLogs:
				return []fkey{{"esc", "back"}, {"d", "details"}, {"↑/↓", "scroll"}, {"o", "open"}, {"C", "clear"}, {"q", "quit"}}
			}
		}
		return []fkey{{"↑/↓", "select"}, {"enter/l", "logs"}, {"d", "details"}, {"o", "open"}, {"R", "restart"}, {"C", "clear"}, {"tab", "switch"}, {"q", "quit"}}
	case "Agents":
		if ag := a.model.agentsTab; ag != nil {
			switch ag.View {
		case AgentViewDetail:
			return []fkey{{"esc", "back"}, {"l", "activity"}, {"m", "model"}, {"r", "runner"}, {"w", "workers"}, {"q", "quit"}}
			case AgentViewActivity:
				return []fkey{{"esc", "back"}, {"↑/↓", "select"}, {"enter/l", "logs"}, {"o", "open"}, {"pgup/dn", "page"}, {"q", "quit"}}
			case AgentViewTaskLogs:
				return []fkey{{"esc", "back"}, {"↑/↓", "scroll"}, {"o", "open"}, {"home/end", "ends"}, {"r", "reload"}, {"q", "quit"}}
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
		if t := a.model.tasksTab; t != nil && t.View == TaskViewLogs {
			if n := len(a.taskLogLines()); n > 0 {
				pos = fmt.Sprintf("line %d/%d   ", t.LogScroll+1, n)
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

// taskStatusBadge renders a colored, fixed-width status label.
func taskStatusBadge(status string) string {
	label := pad(valueOr(status, "—"), 8)
	switch status {
	case "success":
		return StyleSuccess.Render(label)
	case "failed":
		return StyleError.Render(label)
	case "running":
		return StyleWarning.Render(label)
	default:
		return StyleMuted.Render(label)
	}
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

// Run starts the dashboard.
func (a *App) Run() error {
	p := tea.NewProgram(a, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
