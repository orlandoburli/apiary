package dashboard

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// App is the main dashboard application.
type App struct {
	model *Model
}

// New creates a new dashboard app.
func New() *App {
	return &App{
		model: NewModel(),
	}
}

// Init initializes the app.
func (a *App) Init() tea.Cmd {
	return tea.Batch(
		tea.EnterAltScreen,
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
		model, cmd := a.handleKeyMsg(msg)
		return model, cmd
	case tea.WindowSizeMsg:
		a.model.width = msg.Width
		a.model.height = msg.Height
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

	defer func() {
		if r := recover(); r != nil {
			// Silently recover
		}
	}()

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
	activeTab := a.model.ActiveTab()

	switch activeTab {
	case "Overview":
		content = a.renderOverviewTab(contentHeight)
	case "Tasks":
		content = a.renderTasksTab(contentHeight)
	case "Agents":
		content = a.renderAgentsTab(contentHeight)
	case "Logs":
		content = a.renderLogsTab(contentHeight)
	default:
		content = "┌─ ERROR ────────────────────────────────────────────┐\nUnknown tab\n└─────────────────────────────────────────────────────┘\n"
	}

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
	if a == nil {
		return ""
	}
	title := " APIARY DISPATCHER DASHBOARD "
	return StyleHeader.Render(title)
}

func (a *App) renderTabs() string {
	if a == nil || a.model == nil {
		return ""
	}
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
	status := "●"

	content := "Status:     " + status + " Healthy\nUptime:     running\nConcurrency: 4 workers\nAgents:     0 active\n\nTasks (24h):\n  Running:    0\n  Queued:     0\n  Completed:  0 ✓\n  Failed:     0 ✗\n\nMetrics:\n  Throughput:   0.0 tasks/min\n  Avg Duration: 0.0s\n  Success Rate: 0.0%\n"

	// Pad to fill height
	lines := strings.Count(content, "\n") + 1
	padding := height - lines
	if padding > 0 {
		content += strings.Repeat("\n", padding)
	}

	width := a.model.width
	if width < 20 {
		width = 20
	}
	topBorder := "┌─ OVERVIEW " + strings.Repeat("─", width-13) + "┐"
	botBorder := strings.Repeat("─", width)
	botBorder = "└" + botBorder[1:len(botBorder)-1] + "┘"
	return topBorder + "\n" + content + botBorder + "\n"
}

func (a *App) renderTasksTab(height int) string {
	content := "No active tasks\n"

	// Pad to fill height
	lines := strings.Count(content, "\n") + 1
	padding := height - lines
	if padding > 0 {
		content += strings.Repeat("\n", padding)
	}

	width := a.model.width
	if width < 20 {
		width = 20
	}
	topBorder := "┌─ TASKS " + strings.Repeat("─", width-10) + "┐"
	botBorder := strings.Repeat("─", width)
	botBorder = "└" + botBorder[1:len(botBorder)-1] + "┘"
	return topBorder + "\n" + content + botBorder + "\n"
}

func (a *App) renderAgentsTab(height int) string {
	content := "No agents configured\n"

	// Pad to fill height
	lines := strings.Count(content, "\n") + 1
	padding := height - lines
	if padding > 0 {
		content += strings.Repeat("\n", padding)
	}

	width := a.model.width
	if width < 20 {
		width = 20
	}
	topBorder := "┌─ AGENTS " + strings.Repeat("─", width-11) + "┐"
	botBorder := strings.Repeat("─", width)
	botBorder = "└" + botBorder[1:len(botBorder)-1] + "┘"
	return topBorder + "\n" + content + botBorder + "\n"
}

func (a *App) renderLogsTab(height int) string {
	content := "No logs yet\n"

	// Pad to fill height
	lines := strings.Count(content, "\n") + 1
	padding := height - lines
	if padding > 0 {
		content += strings.Repeat("\n", padding)
	}

	width := a.model.width
	if width < 20 {
		width = 20
	}
	topBorder := "┌─ LOGS " + strings.Repeat("─", width-9) + "┐"
	botBorder := strings.Repeat("─", width)
	botBorder = "└" + botBorder[1:len(botBorder)-1] + "┘"
	return topBorder + "\n" + content + botBorder + "\n"
}

func (a *App) renderFooter() string {
	if a == nil || a.model == nil {
		return ""
	}
	footer := "← → Tab  | ↑ ↓ Navigate  | q Quit"
	return StyleMuted.Render(footer)
}

func (a *App) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a == nil || a.model == nil {
		return a, nil
	}

	defer func() {
		if r := recover(); r != nil {
			// Silently recover from panics during key handling
		}
	}()

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
