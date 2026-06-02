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
		a.refreshActiveTab(),
		a.tickCmd(),
	)
}

// Update handles messages.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if a == nil || a.model == nil {
		return a, nil
	}

	defer func() {
		if r := recover(); r != nil {
			// Log panic but don't crash
		}
	}()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		_, cmd := a.handleKeyMsg(msg)
		// Immediately refresh active tab when tab is changed
		if cmd != nil {
			return a, tea.Batch(cmd, a.refreshActiveTab())
		}
		return a, a.refreshActiveTab()
	case tea.WindowSizeMsg:
		a.model.width = msg.Width
		a.model.height = msg.Height
	case refreshMsg:
		cmd := a.refreshActiveTab()
		return a, tea.Batch(cmd, a.tickCmd())
	}
	return a, nil
}

// View renders the dashboard.
func (a *App) View() string {
	if a == nil || a.model == nil {
		return "Error: Dashboard not initialized"
	}

	if a.model.width == 0 || a.model.height == 0 {
		return "Loading..."
	}

	// Header
	header := a.renderHeader()
	headerHeight := lipgloss.Height(header)

	// Tabs
	tabs := a.renderTabs()
	tabsHeight := lipgloss.Height(tabs)

	// Footer
	footer := a.renderFooter()
	footerHeight := lipgloss.Height(footer)

	// Calculate available space for content
	contentHeight := a.model.height - headerHeight - tabsHeight - footerHeight - 2 // 2 for padding
	if contentHeight < 1 {
		contentHeight = 1
	}

	// Content with full screen height
	var content string
	defer func() {
		if r := recover(); r != nil {
			content = "Error rendering tab: " + fmt.Sprintf("%v", r)
		}
	}()

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
		content = "Unknown tab"
	}

	// Content already formatted with line length

	// Combine all sections
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

func (a *App) renderOverviewTab(height int) string {
	if a.model == nil || a.model.overviewTab == nil {
		return "┌─ OVERVIEW ─────────────────────────────────────────┐\nError: model not initialized\n└─────────────────────────────────────────────────────┘\n"
	}

	status := "●"
	if a.model.loading {
		status = "⟳"
	}

	content := fmt.Sprintf("Status:     %s %s\nUptime:     %s\nConcurrency: %d workers\nAgents:     %d active\n\nTasks (24h):\n  Running:    %d\n  Queued:     %d\n  Completed:  %d ✓\n  Failed:     %d ✗\n\nMetrics:\n  Throughput:   %s tasks/min\n  Avg Duration: %s\n  Success Rate: %s\n",
		status, a.model.overviewTab.Status,
		a.model.overviewTab.Uptime,
		a.model.overviewTab.Concurrency,
		a.model.overviewTab.ActiveAgents,
		a.model.overviewTab.ActiveRuns,
		a.model.overviewTab.QueuedTasks,
		a.model.overviewTab.CompletedToday,
		a.model.overviewTab.FailedToday,
		a.model.overviewTab.ThroughputRatio,
		a.model.overviewTab.AvgDuration,
		a.model.overviewTab.SuccessRate,
	)

	// Pad to fill height
	lines := strings.Count(content, "\n") + 1
	padding := height - lines
	if padding > 0 {
		content += strings.Repeat("\n", padding)
	}

	return "┌─ OVERVIEW ─────────────────────────────────────────┐\n" + content + "└─────────────────────────────────────────────────────┘\n"
}

func (a *App) renderTasksTab(height int) string {
	if a.model == nil || a.model.tasksTab == nil {
		return "┌─ TASKS ─────────────────────────────────────────────┐\nError: model not initialized\n└─────────────────────────────────────────────────────┘\n"
	}

	var content string
	if len(a.model.tasksTab.ActiveRuns) == 0 {
		content = "No active tasks\n"
	} else {
		content = "RUNNING TASKS:\n"
		for _, run := range a.model.tasksTab.ActiveRuns {
			duration := time.Since(run.StartedAt).Round(time.Second)
			progress := ProgressBar(run.Progress, 15)
			title := run.Title
			if len(title) > 30 {
				title = title[:30] + "…"
			}
			line := fmt.Sprintf("\n%s (%s)\n%s  %v\n",
				title, run.Agent, progress, duration)
			content += line
		}
	}

	// Pad to fill height
	lines := strings.Count(content, "\n") + 1
	padding := height - lines
	if padding > 0 {
		content += strings.Repeat("\n", padding)
	}

	return "┌─ TASKS ─────────────────────────────────────────────┐\n" + content + "└─────────────────────────────────────────────────────┘\n"
}

func (a *App) renderAgentsTab(height int) string {
	if a.model == nil || a.model.agentsTab == nil {
		return "┌─ AGENTS ────────────────────────────────────────────┐\nError: model not initialized\n└─────────────────────────────────────────────────────┘\n"
	}

	if len(a.model.agentsTab.Agents) == 0 {
		content := "No agents configured\n"
		lines := 1
		padding := height - lines
		if padding > 0 {
			content += strings.Repeat("\n", padding)
		}
		return "┌─ AGENTS ────────────────────────────────────────────┐\n" + content + "└─────────────────────────────────────────────────────┘\n"
	}

	content := "AGENT          STATUS  COMPLETED  AVG TIME  SUCCESS\n"
	for i, agent := range a.model.agentsTab.Agents {
		selected := " "
		if i == a.model.agentsTab.SelectedIdx {
			selected = "→"
		}
		statusIcon := StatusColor(agent.Status)
		avgTime := fmt.Sprintf("%.1fs", float64(agent.AvgDurationMs)/1000)
		line := fmt.Sprintf("%s %-14s %s  %-9d  %-9s  %.1f%%\n",
			selected,
			agent.ID,
			statusIcon,
			agent.CompletedCount,
			avgTime,
			agent.SuccessRate*100,
		)
		content += line
	}

	lines := strings.Count(content, "\n") + 1
	padding := height - lines
	if padding > 0 {
		content += strings.Repeat("\n", padding)
	}

	return "┌─ AGENTS ────────────────────────────────────────────┐\n" + content + "└─────────────────────────────────────────────────────┘\n"
}

func (a *App) renderLogsTab(height int) string {
	if a.model == nil || a.model.logsTab == nil {
		return "┌─ LOGS ──────────────────────────────────────────────┐\nError: model not initialized\n└─────────────────────────────────────────────────────┘\n"
	}

	if len(a.model.logsTab.Logs) == 0 {
		content := "No logs yet\n"
		lines := 1
		padding := height - lines
		if padding > 0 {
			content += strings.Repeat("\n", padding)
		}
		return "┌─ LOGS ──────────────────────────────────────────────┐\n" + content + "└─────────────────────────────────────────────────────┘\n"
	}

	content := ""
	start := a.model.logsTab.Scrolled
	end := start + height - 2
	if end > len(a.model.logsTab.Logs) {
		end = len(a.model.logsTab.Logs)
	}

	for i := start; i < end && i < len(a.model.logsTab.Logs); i++ {
		log := a.model.logsTab.Logs[i]
		timeStr := log.Timestamp.Format("15:04:05")
		msg := log.Message
		maxLen := a.model.width - 30
		if maxLen < 20 {
			maxLen = 20
		}
		if len(msg) > maxLen {
			msg = msg[:maxLen] + "…"
		}
		line := fmt.Sprintf("[%s] [%-5s] %s\n", timeStr, log.Level, msg)
		content += line
	}

	lines := strings.Count(content, "\n") + 1
	padding := height - lines
	if padding > 0 {
		content += strings.Repeat("\n", padding)
	}

	return "┌─ LOGS ──────────────────────────────────────────────┐\n" + content + "└─────────────────────────────────────────────────────┘\n"
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

// refreshActiveTab fetches data ONLY for the currently active tab (lazy loading)
func (a *App) refreshActiveTab() tea.Cmd {
	return func() tea.Msg {
		defer func() {
			if r := recover(); r != nil {
				// Silently recover from panics during data fetching
			}
		}()

		if a == nil || a.model == nil || a.dbConn == nil {
			return refreshMsg{}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		a.model.loading = true
		activeTab := a.model.ActiveTab()

		// Only fetch data for the active tab
		switch activeTab {
		case "Overview":
			if a.model.overviewTab != nil {
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
			}

		case "Tasks":
			if a.model.tasksTab != nil {
				runs, _ := a.dbConn.GetActiveRuns(ctx)
				a.model.tasksTab.ActiveRuns = make([]ActiveRun, 0)
				if runs != nil {
					for _, run := range runs {
						a.model.tasksTab.ActiveRuns = append(a.model.tasksTab.ActiveRuns, ActiveRun{
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

		case "Agents":
			if a.model.agentsTab != nil {
				agents, _ := a.dbConn.GetAgentStats(ctx)
				a.model.agentsTab.Agents = make([]AgentStatus, 0)
				if agents != nil {
					for _, agent := range agents {
						a.model.agentsTab.Agents = append(a.model.agentsTab.Agents, AgentStatus{
							ID:              agent.ID,
							Status:          agent.Status,
							QueuedCount:     agent.QueuedCount,
							CompletedCount:  agent.CompletedCount,
							AvgDurationMs:   agent.AvgDurationMs,
							SuccessRate:     agent.SuccessRate,
							LastTaskEndedAt: agent.LastTaskEndedAt,
						})
					}
				}
			}

		case "Logs":
			if a.model.logsTab != nil {
				logs, _ := a.dbConn.GetRecentLogs(ctx, 100)
				a.model.logsTab.Logs = make([]LogEntry, 0)
				if logs != nil {
					for _, log := range logs {
						a.model.logsTab.Logs = append(a.model.logsTab.Logs, LogEntry{
							Timestamp: log.Timestamp,
							Level:     log.Level,
							Component: log.Component,
							Message:   log.Message,
						})
					}
				}
			}
		}

		a.model.lastRefresh = time.Now()
		a.model.lastTabRefresh[a.model.activeTab] = time.Now()
		a.model.loading = false
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
