package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/orlandoburli/apiary/internal/tui/styles"
	"github.com/orlandoburli/apiary/internal/version"
)

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		m.renderTabs(),
		m.renderBody(),
		m.renderFooter(),
	)
}

func (m Model) renderHeader() string {
	logo := styles.Header.Render("⬡ apiary")
	ver := styles.StatusBar.Render("v" + version.Version)
	spacer := strings.Repeat(" ", max(0, m.width-lipgloss.Width(logo)-lipgloss.Width(ver)-2))
	return lipgloss.JoinHorizontal(lipgloss.Top, logo, spacer, ver)
}

func (m Model) renderTabs() string {
	tabs := []string{}
	for i := viewDashboard; i <= viewLogs; i++ {
		label := fmt.Sprintf("%d %s", i+1, viewNames[i])
		if i == m.activeView {
			tabs = append(tabs, styles.TabActive.Render(label))
		} else {
			tabs = append(tabs, styles.Tab.Render(label))
		}
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	line := strings.Repeat("─", max(0, m.width-lipgloss.Width(bar)))
	return lipgloss.JoinHorizontal(lipgloss.Bottom, bar, styles.StatusBar.Render(line))
}

func (m Model) renderBody() string {
	bodyHeight := m.height - 5
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	switch m.activeView {
	case viewDashboard:
		return m.renderDashboard(bodyHeight)
	case viewCells:
		return m.renderTable(bodyHeight)
	case viewWorkers:
		return m.renderWorkers(bodyHeight)
	default:
		return m.renderPlaceholder(viewNames[m.activeView], bodyHeight)
	}
}

func (m Model) renderDashboard(height int) string {
	lines := []string{
		styles.StatusBar.Render("Sources   " + styles.StatusBarKey.Render("0") + " connected"),
		styles.StatusBar.Render("Workers   " + styles.StatusBarKey.Render("0") + " configured"),
		styles.StatusBar.Render("Active    " + styles.StatusBarKey.Render("0") + " runs"),
		"",
		styles.StatusBar.Render("No active runs."),
	}
	content := strings.Join(lines, "\n")
	padded := content + strings.Repeat("\n", max(0, height-strings.Count(content, "\n")-1))
	return styles.Panel.Width(m.width - 2).Height(height).Render(padded)
}

func (m Model) renderTable(height int) string {
	m.table.SetHeight(height - 2)
	return styles.Panel.Width(m.width - 2).Height(height).Render(m.table.View())
}

func (m Model) renderWorkers(height int) string {
	return m.renderPlaceholder("Workers", height)
}

func (m Model) renderPlaceholder(name string, height int) string {
	msg := styles.StatusBar.Render("No " + strings.ToLower(name) + " yet.")
	padding := strings.Repeat("\n", max(0, height/2-1))
	return styles.Panel.Width(m.width - 2).Height(height).Render(padding + msg)
}

func (m Model) renderFooter() string {
	keys := []struct{ key, desc string }{
		{"1-4", "switch view"},
		{"↑/↓", "navigate"},
		{"enter", "select"},
		{"q", "quit"},
	}
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, styles.StatusBarKey.Render(k.key)+" "+styles.StatusBar.Render(k.desc))
	}
	return styles.StatusBar.Padding(0, 1).Render(strings.Join(parts, "  "))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
