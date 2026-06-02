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
type tasksDataMsg struct{ runs []ActiveRun }
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
			a.model.tasksTab.ActiveRuns = msg.runs
			if a.model.tasksTab.SelectedIdx >= len(msg.runs) {
				a.model.tasksTab.SelectedIdx = 0
			}
		}
		a.model.loading = false
		a.model.lastRefresh = time.Now()

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
	switch msg.String() {
	case "q", "ctrl+c":
		return a, tea.Quit
	case "tab", "right":
		a.model.NextTab()
		a.model.loading = true
		return a, a.fetchActiveTab()
	case "shift+tab", "left":
		a.model.PrevTab()
		a.model.loading = true
		return a, a.fetchActiveTab()
	case "r":
		// Manual refresh.
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
			if a.model.tasksTab != nil && a.model.tasksTab.SelectedIdx < len(a.model.tasksTab.ActiveRuns)-1 {
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
	}
	return a, nil
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

		runs := make([]ActiveRun, 0)
		if dbConn != nil {
			if rows, err := dbConn.GetActiveRuns(ctx); err == nil {
				for _, run := range rows {
					runs = append(runs, ActiveRun{
						ID:        run.CellID,
						CellID:    run.CellID,
						Title:     run.Title,
						Agent:     run.AgentID,
						StartedAt: time.Now().Add(-time.Duration(run.Duration) * time.Millisecond),
						Duration:  time.Duration(run.Duration) * time.Millisecond,
						Progress:  int((run.Duration % 10000) / 100),
					})
				}
			}
		}
		return tasksDataMsg{runs: runs}
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
	if t == nil || len(t.ActiveRuns) == 0 {
		return a.box("TASKS", StyleMuted.Render("No active tasks")+"\n", height)
	}

	var b strings.Builder
	for i, run := range t.ActiveRuns {
		cursor := "  "
		if i == t.SelectedIdx {
			cursor = StyleInfo.Render("▶ ")
		}
		title := truncate(run.Title, a.model.width-24)
		dur := time.Since(run.StartedAt).Round(time.Second)
		bar := ProgressBar(run.Progress, 14)
		b.WriteString(fmt.Sprintf("%s%s %s\n", cursor, StyleInfo.Render(title), StyleMuted.Render("("+run.Agent+")")))
		b.WriteString(fmt.Sprintf("   %s  %v\n", bar, dur))
	}
	return a.box("TASKS", b.String(), height)
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
