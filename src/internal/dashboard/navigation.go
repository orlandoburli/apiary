package dashboard

import tea "github.com/charmbracelet/bubbletea"

// navigationLoadedMsg identifies a user-initiated transition independently of
// background refreshes, which must never dismiss its loading screen.
type navigationLoadedMsg struct {
	id     uint64
	result tea.Msg
}

// beginNavigation runs one view load while the event loop keeps the spinner and
// cancel/quit keys responsive. Cancelling invalidates the result; the database
// command still finishes within its existing query timeout.
func (a *App) beginNavigation(label string, cmd tea.Cmd) tea.Cmd {
	if cmd == nil || a.navigationLabel != "" {
		return nil
	}
	a.navigationID++
	id := a.navigationID
	a.navigationLabel = label
	a.model.loading = true
	return func() tea.Msg { return navigationLoadedMsg{id: id, result: cmd()} }
}
