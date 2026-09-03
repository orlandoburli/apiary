package dashboard

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// While the task filter is being typed ("/" pressed), every key belongs to the
// filter: printable chars append (even ones with global bindings like "q"),
// enter confirms the filter, and esc clears it. Nothing else may fire.
func TestTaskFilterTypingOwnsKeyboard(t *testing.T) {
	newApp := func() *App {
		a := &App{model: NewModel()}
		a.model.activeTab = 1 // Tasks
		return a
	}
	rune_ := func(s string) tea.KeyMsg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}

	a := newApp()
	a.handleKeyMsg(rune_("/"))
	if !a.model.tasksTab.FilterActive {
		t.Fatal("'/' should enter filter mode")
	}

	// "q" must append to the filter, not quit; "r"/"s"/"d" likewise inert.
	// The only command an edit may produce is the list re-query the filter
	// itself triggers (it runs in SQL), never the key's global binding.
	for _, k := range []string{"q", "r", "s", "d"} {
		_, cmd := a.handleKeyMsg(rune_(k))
		if cmd == nil {
			t.Fatalf("key %q should re-query the filtered list", k)
		}
		if _, isRefetch := cmd().(tasksDataMsg); !isRefetch {
			t.Fatalf("key %q produced a command other than the list re-query while filtering", k)
		}
	}
	if got := a.model.tasksTab.FilterText; got != "qrsd" {
		t.Fatalf("FilterText = %q, want %q", got, "qrsd")
	}
	if a.model.tasksTab.SortField != "" {
		t.Fatal("'s' must not change sort while filtering")
	}

	// Enter confirms: leaves typing mode but keeps the filter text.
	a.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if a.model.tasksTab.FilterActive {
		t.Fatal("enter should leave filter-typing mode")
	}
	if a.model.tasksTab.FilterText != "qrsd" {
		t.Fatal("enter should keep the filter text")
	}

	// Esc after confirming clears the applied filter.
	a.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	if a.model.tasksTab.FilterText != "" {
		t.Fatal("esc should clear a confirmed filter")
	}

	// Esc while typing cancels and clears.
	a = newApp()
	a.handleKeyMsg(rune_("/"))
	a.handleKeyMsg(rune_("x"))
	a.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEsc})
	if a.model.tasksTab.FilterActive || a.model.tasksTab.FilterText != "" {
		t.Fatal("esc while typing should cancel and clear the filter")
	}

	// Ctrl+C still quits even while typing.
	a = newApp()
	a.handleKeyMsg(rune_("/"))
	if _, cmd := a.handleKeyMsg(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Fatal("ctrl+c should still quit during filter typing")
	}
}
