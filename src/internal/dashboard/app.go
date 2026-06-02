package dashboard

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/orlandoburli/apiary/internal/db"
)

// App is the main dashboard application.
type App struct {
	model  *Model
	dbConn *db.Client
	ticker *time.Ticker
}

// New creates a new dashboard app.
func New(dbConn *db.Client) *App {
	return &App{
		model:  NewModel(),
		dbConn: dbConn,
		ticker: time.NewTicker(2 * time.Second),
	}
}

// Init initializes the app.
func (a *App) Init() tea.Cmd {
	return tea.Batch(
		tea.EnterAltScreen,
		a.refreshData(),
		a.tickCmd(),
	)
}

// Update handles messages.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return a.handleKeyMsg(msg)
	case tea.WindowSizeMsg:
		a.model.width = msg.Width
		a.model.height = msg.Height
	case refreshMsg:
		cmd := a.refreshData()
		return a, tea.Batch(cmd, a.tickCmd())
	}
	return a, nil
}

// View renders the dashboard.
func (a *App) View() string {
	if a.model.width == 0 || a.model.height == 0 {
		return "Loading..."
	}

	// Header
	header := a.renderHeader()

	// Tabs
	tabs := a.renderTabs()

	// Content
	var content string
	switch a.model.ActiveTab() {
	case "Overview":
		content = a.renderOverviewTab()
	case "Tasks":
		content = a.renderTasksTab()
	case "Agents":
		content = a.renderAgentsTab()
	case "Logs":
		content = a.renderLogsTab()
	default:
		content = "Unknown tab"
	}

	// Footer
	footer := a.renderFooter()

	// Combine
	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		tabs,
		content,
		footer,
	)
}

func (a *App) renderHeader() string {
	title := " APIARY DISPATCHER DASHBOARD "
	return StyleHeader.Render(title)
}

func (a *App) renderTabs() string {
	var tabs []string
	for i, tab := range a.model.tabs {
		style := StyleTab
		if i == a.model.activeTab {
			style = StyleActiveTab
		}
		tabs = append(tabs, style.Render(tab))
	}
	return lipgloss.JoinHorizontal(lipgloss.Center, tabs...) + "\n"
}

func (a *App) renderOverviewTab() string {
	return StyleBorder.Render(fmt.Sprintf(`
Status:     %s %s
Uptime:     %s
Concurrency: %d
Agents:     %d active
Tasks:      %d running, %d queued

Today:      %d completed, %d failed
Rate:       %s/min
Duration:   %s avg
Success:    %s

← → Tab | ↑ ↓ Select | q Quit
`,
		StatusColor("active"), a.model.overviewTab.Status,
		a.model.overviewTab.Uptime,
		a.model.overviewTab.Concurrency,
		a.model.overviewTab.ActiveAgents,
		a.model.overviewTab.ActiveRuns, a.model.overviewTab.QueuedTasks,
		a.model.overviewTab.CompletedToday, a.model.overviewTab.FailedToday,
		a.model.overviewTab.ThroughputRatio,
		a.model.overviewTab.AvgDuration,
		a.model.overviewTab.SuccessRate,
	))
}

func (a *App) renderTasksTab() string {
	var content string
	if len(a.model.tasksTab.ActiveRuns) == 0 {
		content = StyleMuted.Render("No active tasks")
	} else {
		for _, run := range a.model.tasksTab.ActiveRuns {
			duration := time.Since(run.StartedAt).Round(time.Second)
			progress := ProgressBar(run.Progress, 20)
			line := fmt.Sprintf("%s  %s  %s  %s\n",
				run.Agent, run.Title[:min(30, len(run.Title))], progress, duration)
			content += line
		}
	}
	return StyleBorder.Render(content)
}

func (a *App) renderAgentsTab() string {
	if len(a.model.agentsTab.Agents) == 0 {
		return StyleBorder.Render(StyleMuted.Render("No agents configured"))
	}

	content := "ID          Status  Working  Queue  Completed  Success\n"
	for i, agent := range a.model.agentsTab.Agents {
		selected := ""
		if i == a.model.agentsTab.SelectedIdx {
			selected = "→ "
		}
		statusIcon := StatusColor(agent.Status)
		line := fmt.Sprintf("%s%-11s  %s  %-7d  %-5d  %-9d  %.1f%%\n",
			selected,
			agent.ID,
			statusIcon,
			1, // Would be current task count
			agent.QueuedCount,
			agent.CompletedCount,
			agent.SuccessRate*100,
		)
		content += line
	}
	return StyleBorder.Render(content)
}

func (a *App) renderLogsTab() string {
	if len(a.model.logsTab.Logs) == 0 {
		return StyleBorder.Render(StyleMuted.Render("No logs yet"))
	}

	content := ""
	start := a.model.logsTab.Scrolled
	end := start + (a.model.height / 2)
	if end > len(a.model.logsTab.Logs) {
		end = len(a.model.logsTab.Logs)
	}

	for i := start; i < end && i < len(a.model.logsTab.Logs); i++ {
		log := a.model.logsTab.Logs[i]
		timeStr := log.Timestamp.Format("15:04:05")
		var style lipgloss.Style
		switch log.Level {
		case "ERROR":
			style = StyleError
		case "WARN":
			style = StyleWarning
		case "INFO":
			style = StyleInfo
		default:
			style = StyleMuted
		}
		line := fmt.Sprintf("[%s] [%-5s] %s\n", timeStr, log.Level, log.Message[:min(60, len(log.Message))])
		content += style.Render(line)
	}

	return StyleBorder.Render(content)
}

func (a *App) renderFooter() string {
	lastUpdate := time.Since(a.model.lastRefresh).Round(time.Second)
	footer := fmt.Sprintf("Last update: %s ago  | ← → Tab  | ↑ ↓ Navigate  | q Quit",
		lastUpdate)
	return StyleMuted.Render(footer)
}

func (a *App) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return a, tea.Quit
	case "tab", "right":
		a.model.NextTab()
	case "shift+tab", "left":
		a.model.PrevTab()
	case "up":
		switch a.model.ActiveTab() {
		case "Tasks":
			if a.model.tasksTab.SelectedIdx > 0 {
				a.model.tasksTab.SelectedIdx--
			}
		case "Agents":
			if a.model.agentsTab.SelectedIdx > 0 {
				a.model.agentsTab.SelectedIdx--
			}
		case "Logs":
			if a.model.logsTab.Scrolled > 0 {
				a.model.logsTab.Scrolled--
			}
		}
	case "down":
		switch a.model.ActiveTab() {
		case "Tasks":
			if a.model.tasksTab.SelectedIdx < len(a.model.tasksTab.ActiveRuns)-1 {
				a.model.tasksTab.SelectedIdx++
			}
		case "Agents":
			if a.model.agentsTab.SelectedIdx < len(a.model.agentsTab.Agents)-1 {
				a.model.agentsTab.SelectedIdx++
			}
		case "Logs":
			if a.model.logsTab.Scrolled < len(a.model.logsTab.Logs)-1 {
				a.model.logsTab.Scrolled++
			}
		}
	}
	return a, nil
}

func (a *App) refreshData() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Fetch overview stats
		stats, err := a.dbConn.GetDashboardStats(ctx, time.Now().AddDate(0, 0, -1))
		if err == nil && stats != nil {
			a.model.overviewTab.Status = stats.DispatcherStatus
			a.model.overviewTab.ActiveAgents = stats.ActiveAgents
			a.model.overviewTab.ActiveRuns = stats.ActiveRuns
			a.model.overviewTab.QueuedTasks = stats.QueuedTasks
			a.model.overviewTab.CompletedToday = stats.CompletedToday
			a.model.overviewTab.FailedToday = stats.FailedToday
			a.model.overviewTab.AvgDuration = fmt.Sprintf("%.1fs", float64(stats.AvgDurationMs)/1000)
			a.model.overviewTab.SuccessRate = fmt.Sprintf("%.1f%%", stats.SuccessRate*100)
			if stats.CompletedToday > 0 {
				a.model.overviewTab.ThroughputRatio = fmt.Sprintf("%.1f", float64(stats.CompletedToday)/24)
			}
		}

		// Fetch recent tasks
		tasks, err := a.dbConn.GetRecentTasks(ctx, 10)
		if err == nil {
			a.model.tasksTab.RecentTasks = make([]TaskSummary, 0)
			for _, t := range tasks {
				a.model.tasksTab.RecentTasks = append(a.model.tasksTab.RecentTasks, TaskSummary{
					ID:       t.ID,
					Title:    t.Title,
					Agent:    t.AgentID,
					Status:   t.Status,
					Duration: time.Duration(t.Duration) * time.Millisecond,
					Success:  t.Success,
				})
			}
		}

		// Fetch agent stats
		agents, err := a.dbConn.GetAgentStats(ctx)
		if err == nil {
			a.model.agentsTab.Agents = make([]AgentStatus, 0)
			for _, agent := range agents {
				a.model.agentsTab.Agents = append(a.model.agentsTab.Agents, AgentStatus{
					ID:            agent.ID,
					Status:        agent.Status,
					QueuedCount:   agent.QueuedCount,
					CompletedCount: agent.CompletedCount,
					AvgDurationMs: agent.AvgDurationMs,
					SuccessRate:   agent.SuccessRate,
					LastTaskEndedAt: agent.LastTaskEndedAt,
				})
			}
		}

		// Fetch logs
		logs, err := a.dbConn.GetRecentLogs(ctx, 50)
		if err == nil {
			a.model.logsTab.Logs = make([]LogEntry, 0)
			for _, log := range logs {
				a.model.logsTab.Logs = append(a.model.logsTab.Logs, LogEntry{
					Timestamp: log.Timestamp,
					Level:     log.Level,
					Component: log.Component,
					Message:   log.Message,
				})
			}
		}

		// Fetch active runs
		runs, err := a.dbConn.GetActiveRuns(ctx)
		if err == nil {
			a.model.tasksTab.ActiveRuns = make([]ActiveRun, 0)
			for _, run := range runs {
				a.model.tasksTab.ActiveRuns = append(a.model.tasksTab.ActiveRuns, ActiveRun{
					ID:        run.CellID,
					CellID:    run.CellID,
					Title:     run.Title,
					Agent:     run.AgentID,
					StartedAt: time.Now().Add(-time.Duration(run.Duration) * time.Millisecond),
					Duration:  time.Duration(run.Duration) * time.Millisecond,
					Progress:  int((run.Duration % 10000) / 100), // Simple progress simulation
				})
			}
		}

		a.model.lastRefresh = time.Now()
		return refreshMsg{}
	}
}

func (a *App) tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return refreshMsg{}
	})
}

type refreshMsg struct{}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Run starts the dashboard.
func (a *App) Run() error {
	p := tea.NewProgram(a, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
