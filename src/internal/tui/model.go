package tui

import (
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/orlandoburli/apiary/internal/model"
)

type view int

const (
	viewDashboard view = iota
	viewCells
	viewWorkers
	viewLogs
)

var viewNames = map[view]string{
	viewDashboard: "Dashboard",
	viewCells:     "Cells",
	viewWorkers:   "Workers",
	viewLogs:      "Logs",
}

type Model struct {
	width  int
	height int

	activeView view
	table      table.Model
	activeRuns []model.ActiveRun

	err error
}

func New() Model {
	cols := []table.Column{
		{Title: "ID", Width: 12},
		{Title: "Title", Width: 40},
		{Title: "Source", Width: 12},
		{Title: "Worker", Width: 16},
		{Title: "Model", Width: 20},
		{Title: "Status", Width: 10},
	}

	t := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(10),
	)

	return Model{
		activeView: viewDashboard,
		table:      t,
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "1":
			m.activeView = viewDashboard
		case "2":
			m.activeView = viewCells
		case "3":
			m.activeView = viewWorkers
		case "4":
			m.activeView = viewLogs
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}
