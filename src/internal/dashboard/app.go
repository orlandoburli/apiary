package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

	switch key {
	case "tab", "right":
		a.model.NextTab()
		a.model.loading = true
		return a, a.fetchActiveTab()
	case "shift+tab", "left":
		a.model.PrevTab()
		a.model.loading = true
		return a, a.fetchActiveTab()
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
			if a.model.logsTab != nil && a.model.logsTab.Scrolled < len(a.model.logsTab.Logs)-1 {
				a.model.logsTab.Scrolled++
			}
		}
	case "enter", "l":
		// Open the logs view for the selected task.
		if a.model.ActiveTab() == "Tasks" {
			if id, ok := a.selectedTaskID(); ok {
				a.model.loading = true
				return a, a.fetchTaskLogs(id)
			}
		}
	case "d":
		// Open the detail view for the selected task.
		if a.model.ActiveTab() == "Tasks" {
			if id, ok := a.selectedTaskID(); ok {
				a.model.loading = true
				return a, a.fetchTaskDetail(id)
			}
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
		if t.View == TaskViewLogs && t.LogScroll < len(t.Logs)-1 {
			t.LogScroll++
		}
	}
	return a, nil
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
			if rows, err := dbConn.GetTaskLogs(ctx, taskID, 500); err == nil {
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
			if rows, err := dbConn.GetRecentLogs(ctx, 100); err == nil {
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

// box wraps inner content in a titled border that spans the terminal width,
// padding the content so the whole block is exactly `height` lines tall.
func (a *App) box(label, content string, height int) string {
	width := a.model.width
	if width < 24 {
		width = 24
	}

	// Pad inner content to fill (height - 2) lines (top + bottom borders).
	inner := height - 2
	if inner < 1 {
		inner = 1
	}
	lines := strings.Count(content, "\n")
	if pad := inner - lines; pad > 0 {
		content += strings.Repeat("\n", pad)
	}

	prefix := "┌─ " + label + " "
	dashes := width - lipgloss.Width(prefix) - 1
	if dashes < 0 {
		dashes = 0
	}
	top := prefix + strings.Repeat("─", dashes) + "┐"
	bottom := "└" + strings.Repeat("─", width-2) + "┘"
	return top + "\n" + content + bottom + "\n"
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

	var b strings.Builder
	b.WriteString(StyleMuted.Render(fmt.Sprintf("  %-3s %-30s %-16s %-9s %s", "", "TASK", "AGENT", "STATUS", "WHEN")) + "\n")
	for i, it := range t.History {
		cursor := "  "
		if i == t.SelectedIdx {
			cursor = StyleInfo.Render("▶ ")
		}
		title := pad(truncate(valueOr(it.Title, it.TaskID), 30), 30)
		agent := pad(truncate(valueOr(it.Agent, "—"), 16), 16)
		status := taskStatusBadge(it.Status)
		when := taskWhen(it)
		b.WriteString(fmt.Sprintf("%s%s %s %s %s\n", cursor, title, agent, status, StyleMuted.Render(when)))
	}
	b.WriteString("\n")
	b.WriteString(StyleMuted.Render("enter/l logs   d details   ↑/↓ select"))
	b.WriteString("\n")
	return a.box("TASKS", b.String(), height)
}

func (a *App) renderTaskDetail(t *TasksTab, height int) string {
	d := t.Detail
	if d == nil {
		return a.box("TASK DETAILS", StyleMuted.Render("No details")+"\n"+a.backHint(), height)
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
		b.WriteString(fmt.Sprintf("  %-14s %s\n", k+":", v))
	}
	row("Task ID", d.TaskID)
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
		b.WriteString("  " + truncate(d.Error, a.model.width-4) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(a.backHint())
	b.WriteString("\n")
	return a.box("TASK DETAILS", b.String(), height)
}

func (a *App) renderTaskLogs(t *TasksTab, height int) string {
	if len(t.Logs) == 0 {
		body := StyleMuted.Render("No logs recorded for this task.") + "\n\n" + a.backHint() + "\n"
		return a.box("TASK LOGS", body, height)
	}

	rows := height - 4 // borders + back hint
	if rows < 1 {
		rows = 1
	}
	start := t.LogScroll
	if start > len(t.Logs)-1 {
		start = 0
	}
	end := start + rows
	if end > len(t.Logs) {
		end = len(t.Logs)
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		entry := t.Logs[i]
		ts := entry.Timestamp.Format("15:04:05")
		level := levelStyle(entry.Level).Render(fmt.Sprintf("%-5s", entry.Level))
		msg := truncate(entry.Message, a.model.width-22)
		b.WriteString(fmt.Sprintf("%s %s %s\n", StyleMuted.Render(ts), level, msg))
	}
	b.WriteString("\n")
	b.WriteString(a.backHint())
	b.WriteString("\n")
	return a.box("TASK LOGS", b.String(), height)
}

func (a *App) backHint() string {
	return StyleMuted.Render("esc back   d details   l logs   ↑/↓ scroll")
}

func (a *App) renderAgentsTab(height int) string {
	ag := a.model.agentsTab
	if ag == nil || len(ag.Agents) == 0 {
		return a.box("AGENTS", StyleMuted.Render("No agents yet")+"\n", height)
	}

	var b strings.Builder
	b.WriteString(StyleMuted.Render(fmt.Sprintf("  %-16s %-8s %-10s %-10s %s", "AGENT", "STATUS", "COMPLETED", "AVG", "SUCCESS")) + "\n")
	for i, agent := range ag.Agents {
		cursor := "  "
		if i == ag.SelectedIdx {
			cursor = StyleInfo.Render("▶ ")
		}
		avg := fmt.Sprintf("%.1fs", float64(agent.AvgDurationMs)/1000)
		b.WriteString(fmt.Sprintf("%s%-16s %s %-10d %-10s %.0f%%\n",
			cursor,
			truncate(agent.ID, 16),
			StatusColor(agent.Status),
			agent.CompletedCount,
			avg,
			agent.SuccessRate*100,
		))
	}
	return a.box("AGENTS", b.String(), height)
}

func (a *App) renderLogsTab(height int) string {
	l := a.model.logsTab
	if l == nil || len(l.Logs) == 0 {
		return a.box("LOGS", StyleMuted.Render("No logs yet")+"\n", height)
	}

	// Show the most recent logs that fit in the available rows.
	rows := height - 2
	if rows < 1 {
		rows = 1
	}
	start := l.Scrolled
	if start > len(l.Logs)-1 {
		start = 0
	}
	end := start + rows
	if end > len(l.Logs) {
		end = len(l.Logs)
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		entry := l.Logs[i]
		ts := entry.Timestamp.Format("15:04:05")
		level := levelStyle(entry.Level).Render(fmt.Sprintf("%-5s", entry.Level))
		msg := truncate(entry.Message, a.model.width-22)
		b.WriteString(fmt.Sprintf("%s %s %s\n", StyleMuted.Render(ts), level, msg))
	}
	return a.box("LOGS", b.String(), height)
}

func (a *App) renderFooter() string {
	if a == nil || a.model == nil {
		return ""
	}
	updated := "—"
	if !a.model.lastRefresh.IsZero() {
		updated = time.Since(a.model.lastRefresh).Round(time.Second).String() + " ago"
	}
	footer := fmt.Sprintf("updated %s   │   ←/→ tab   ↑/↓ nav   r refresh   q quit", updated)
	return StyleMuted.Render(footer)
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
