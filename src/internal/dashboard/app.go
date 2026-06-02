package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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
	model  *Model
	dbConn *db.Client
}

// New creates a new dashboard app backed by the given database client.
func New(dbConn *db.Client) *App {
	return &App{
		model:  NewModel(),
		dbConn: dbConn,
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
}
type agentsDataMsg struct{ agents []AgentStatus }
type agentActivityMsg struct {
	agentID string
	items   []TaskItem
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
		}
		a.model.loading = false
		a.model.lastRefresh = time.Now()

	case agentActivityMsg:
		if a.model.agentsTab != nil {
			a.model.agentsTab.Activity = msg.items
			a.model.agentsTab.ActivityScroll = 0
			a.model.agentsTab.View = AgentViewActivity
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
			if a.model.tasksTab != nil && a.model.tasksTab.SelectedIdx < len(a.model.tasksTab.History)-1 {
				a.model.tasksTab.SelectedIdx++
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
		if a.model.ActiveTab() == "Logs" && a.model.logsTab != nil {
			a.model.logsTab.Scrolled = 0
		}
	case "end":
		if a.model.ActiveTab() == "Logs" && a.model.logsTab != nil {
			a.model.logsTab.Scrolled = lastIndex(len(a.logVisualLines()))
		}
	case "pgup", "ctrl+u":
		if a.model.ActiveTab() == "Logs" && a.model.logsTab != nil {
			a.model.logsTab.Scrolled = clampScroll(a.model.logsTab.Scrolled-a.pageSize(), len(a.logVisualLines()))
		}
	case "pgdown", "ctrl+d", " ":
		if a.model.ActiveTab() == "Logs" && a.model.logsTab != nil {
			a.model.logsTab.Scrolled = clampScroll(a.model.logsTab.Scrolled+a.pageSize(), len(a.logVisualLines()))
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

// handleAgentSubViewKey handles keys while an agent detail/activity view is open.
func (a *App) handleAgentSubViewKey(key string) (tea.Model, tea.Cmd) {
	ag := a.model.agentsTab
	switch key {
	case "esc", "backspace", "h", "left":
		ag.View = AgentViewList
		ag.Detail = nil
		ag.Activity = nil
		ag.ActivityScroll = 0
	case "d":
		if a2, ok := a.selectedAgent(); ok {
			ag.Detail = a2
			ag.View = AgentViewDetail
		}
	case "l", "enter":
		if id, ok := a.selectedAgentID(); ok {
			a.model.loading = true
			return a, a.fetchAgentActivity(id)
		}
	case "r":
		if id, ok := a.selectedAgentID(); ok && ag.View == AgentViewActivity {
			a.model.loading = true
			return a, a.fetchAgentActivity(id)
		}
	case "up":
		if ag.View == AgentViewActivity && ag.ActivityScroll > 0 {
			ag.ActivityScroll--
		}
	case "down":
		if ag.View == AgentViewActivity && ag.ActivityScroll < len(ag.Activity)-1 {
			ag.ActivityScroll++
		}
	}
	return a, nil
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
				if stats.CompletedToday > 0 {
					data.ThroughputRatio = fmt.Sprintf("%.1f", float64(stats.CompletedToday)/24)
				} else {
					data.ThroughputRatio = "0.0"
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
					items = append(items, TaskItem{
						TaskID:      r.TaskID,
						Title:       r.Title,
						Agent:       r.AgentID,
						Model:       r.Model,
						Runner:      r.Runner,
						Status:      r.Status,
						Attempt:     r.Attempt,
						Duration:    time.Duration(r.DurationMs) * time.Millisecond,
						StartedAt:   r.StartedAt,
						CompletedAt: r.CompletedAt,
						Error:       r.Error,
					})
				}
			}
		}
		return tasksDataMsg{items: items}
	}
}

// taskItemFromHistory converts a DB history row into a dashboard TaskItem.
func taskItemFromHistory(r db.TaskHistoryItem) TaskItem {
	return TaskItem{
		TaskID:      r.TaskID,
		Title:       r.Title,
		Agent:       r.AgentID,
		Model:       r.Model,
		Runner:      r.Runner,
		Status:      r.Status,
		Attempt:     r.Attempt,
		Duration:    time.Duration(r.DurationMs) * time.Millisecond,
		StartedAt:   r.StartedAt,
		CompletedAt: r.CompletedAt,
		Error:       r.Error,
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

func (a *App) fetchTaskDetail(taskID string) tea.Cmd {
	dbConn := a.dbConn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		var detail *TaskItem
		if dbConn != nil {
			if r, err := dbConn.GetTaskDetail(ctx, taskID); err == nil && r != nil {
				detail = &TaskItem{
					TaskID:      r.TaskID,
					Title:       r.Title,
					Agent:       r.AgentID,
					Model:       r.Model,
					Runner:      r.Runner,
					Status:      r.Status,
					Attempt:     r.Attempt,
					Duration:    time.Duration(r.DurationMs) * time.Millisecond,
					StartedAt:   r.StartedAt,
					CompletedAt: r.CompletedAt,
					Error:       r.Error,
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

		logs := make([]LogEntry, 0)
		if dbConn != nil {
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
		return taskLogsMsg{taskID: taskID, logs: logs}
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
					agents = append(agents, AgentStatus{
						ID:              ag.ID,
						Status:          ag.Status,
						QueuedCount:     ag.QueuedCount,
						CompletedCount:  ag.CompletedCount,
						AvgDurationMs:   ag.AvgDurationMs,
						SuccessRate:     ag.SuccessRate,
						LastTaskEndedAt: ag.LastTaskEndedAt,
					})
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

	return lipgloss.JoinVertical(lipgloss.Left, tabs, content, footer)
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

	content := fmt.Sprintf(
		"Status:       %s %s\n"+
			"Concurrency:  %d workers\n"+
			"Agents:       %d active\n"+
			"\n"+
			"Tasks (24h):\n"+
			"  Running:    %d\n"+
			"  Queued:     %d\n"+
			"  Completed:  %s\n"+
			"  Failed:     %s\n"+
			"\n"+
			"Metrics:\n"+
			"  Throughput:   %s tasks/min\n"+
			"  Avg Duration: %s\n"+
			"  Success Rate: %s\n",
		status, valueOr(o.Status, "Unknown"),
		o.Concurrency,
		o.ActiveAgents,
		o.ActiveRuns,
		o.QueuedTasks,
		StyleSuccess.Render(fmt.Sprintf("%d ✓", o.CompletedToday)),
		StyleError.Render(fmt.Sprintf("%d ✗", o.FailedToday)),
		valueOr(o.ThroughputRatio, "0.0"),
		valueOr(o.AvgDuration, "0.0s"),
		valueOr(o.SuccessRate, "0.0%"),
	)
	return a.box("OVERVIEW", content, height)
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
	if len(t.History) == 0 {
		body := StyleMuted.Render("No tasks yet — start the dispatcher and give it work.") + "\n"
		return a.box("TASKS", body, height)
	}

	const (
		cursorW = 2
		agentW  = 16
		statusW = 8
		whenW   = 11
	)
	inner := a.model.width - 2
	titleW := inner - cursorW - agentW - statusW - whenW - 4 // 4 single-space separators
	if titleW < 10 {
		titleW = 10
	}

	var b strings.Builder
	header := pad("", cursorW) + " " + pad("TASK", titleW) + " " + pad("AGENT", agentW) + " " + pad("STATUS", statusW) + " " + "WHEN"
	b.WriteString(StyleTableHeader.Render(header) + "\n")
	for i, it := range t.History {
		selected := i == t.SelectedIdx
		cursor := "  "
		titleText := pad(truncate(valueOr(it.Title, it.TaskID), titleW), titleW)
		if selected {
			cursor = StyleFocusedArrow.Render("▶") + " "
			titleText = StyleSelectedRow.Render(titleText)
		}
		agent := pad(truncate(valueOr(it.Agent, "—"), agentW), agentW)
		status := taskStatusBadge(it.Status) // already padded to width 8
		when := StyleMuted.Render(taskWhen(it))
		b.WriteString(cursor + " " + titleText + " " + agent + " " + status + " " + when + "\n")
	}
	return a.box("TASKS", b.String(), height)
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
	if d.Error != "" {
		b.WriteString("\n")
		b.WriteString("  " + StyleError.Render("Error:") + "\n")
		b.WriteString("  " + StyleError.Render(truncate(d.Error, a.model.width-4)) + "\n")
	}
	return a.box("TASK DETAILS — "+valueOr(d.TaskID, ""), b.String(), height)
}

func (a *App) renderTaskLogs(t *TasksTab, height int) string {
	if len(t.Logs) == 0 {
		return a.box("TASK LOGS", StyleMuted.Render("No logs recorded for this task.")+"\n", height)
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
	return a.box("TASK LOGS", b.String(), height)
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
	const prefixWidth = 15                      // "15:04:05" + space + 5-char level + space
	msgWidth := a.model.width - 2 - prefixWidth // inner minus the prefix column
	if msgWidth < 20 {
		msgWidth = 20
	}
	indent := strings.Repeat(" ", prefixWidth)

	var out []string
	for _, entry := range t.Logs {
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
		statusW    = 9
		completedW = 10
		avgW       = 9
		successW   = 7
	)
	inner := a.model.width - 2
	agentW := inner - cursorW - statusW - completedW - avgW - successW - 5 // 5 separators
	if agentW < 12 {
		agentW = 12
	}

	var b strings.Builder
	header := pad("", cursorW) + " " + pad("AGENT", agentW) + " " + pad("STATUS", statusW) + " " + pad("COMPLETED", completedW) + " " + pad("AVG", avgW) + " " + "SUCCESS"
	b.WriteString(StyleTableHeader.Render(header) + "\n")
	for i, agent := range ag.Agents {
		selected := i == ag.SelectedIdx
		cursor := "  "
		name := pad(truncate(valueOr(agent.ID, "—"), agentW), agentW)
		if selected {
			cursor = StyleFocusedArrow.Render("▶") + " "
			name = StyleSelectedRow.Render(name)
		}
		status := pad(StatusColor(agent.Status)+" "+agentStatusText(agent.Status), statusW)
		completed := pad(fmt.Sprintf("%d", agent.CompletedCount), completedW)
		avg := pad(fmt.Sprintf("%.1fs", float64(agent.AvgDurationMs)/1000), avgW)
		success := successRateStyled(agent.SuccessRate)
		b.WriteString(cursor + " " + name + " " + status + " " + completed + " " + avg + " " + success + "\n")
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
	row("Status", StatusColor(d.Status)+" "+agentStatusText(d.Status))
	row("Current task", valueOr(d.CurrentTask, "—"))
	b.WriteString("\n")
	row("Completed", StyleSuccess.Render(fmt.Sprintf("%d", completed)))
	row("Succeeded", StyleSuccess.Render(fmt.Sprintf("%d ✓", succeeded)))
	row("Failed", StyleError.Render(fmt.Sprintf("%d ✗", failed)))
	row("Success rate", successRateStyled(d.SuccessRate))
	row("Queued", fmt.Sprintf("%d", d.QueuedCount))
	b.WriteString("\n")
	row("Avg duration", fmt.Sprintf("%.1fs", float64(d.AvgDurationMs)/1000))
	row("Last task", lastEnded)
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
		statusW = 8
		durW    = 8
		whenW   = 11
	)
	inner := a.model.width - 2
	titleW := inner - cursorW - statusW - durW - whenW - 4
	if titleW < 10 {
		titleW = 10
	}

	var b strings.Builder
	header := pad("", cursorW) + " " + pad("TASK", titleW) + " " + pad("STATUS", statusW) + " " + pad("DURATION", durW) + " " + "WHEN"
	b.WriteString(StyleTableHeader.Render(header) + "\n")

	rows := height - 3 // borders + header
	if rows < 1 {
		rows = 1
	}
	start := ag.ActivityScroll
	if start > len(ag.Activity)-1 {
		start = len(ag.Activity) - 1
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
		title := pad(truncate(valueOr(it.Title, it.TaskID), titleW), titleW)
		status := taskStatusBadge(it.Status)
		dur := "—"
		if it.Duration > 0 {
			dur = it.Duration.Round(time.Second).String()
		}
		b.WriteString("   " + title + " " + status + " " + pad(dur, durW) + " " + StyleMuted.Render(taskWhen(it)) + "\n")
	}
	return a.box("AGENT ACTIVITY — "+name, b.String(), height)
}

// agentStatusText returns a readable label for an agent status.
func agentStatusText(s string) string {
	switch s {
	case "active":
		return StyleSuccess.Render("active")
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
				return []fkey{{"esc", "back"}, {"l", "logs"}, {"r", "reload"}, {"q", "quit"}}
			case TaskViewLogs:
				return []fkey{{"esc", "back"}, {"d", "details"}, {"↑/↓", "scroll"}, {"pgup/dn", "page"}, {"home/end", "ends"}, {"q", "quit"}}
			}
		}
		return []fkey{{"↑/↓", "select"}, {"enter/l", "logs"}, {"d", "details"}, {"tab", "switch"}, {"r", "refresh"}, {"q", "quit"}}
	case "Agents":
		if ag := a.model.agentsTab; ag != nil {
			switch ag.View {
			case AgentViewDetail:
				return []fkey{{"esc", "back"}, {"l", "activity"}, {"q", "quit"}}
			case AgentViewActivity:
				return []fkey{{"esc", "back"}, {"d", "details"}, {"↑/↓", "scroll"}, {"r", "reload"}, {"q", "quit"}}
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
