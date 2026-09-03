package dashboard

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/orlandoburli/apiary/internal/config"
)

// TestHasTicket checks the structural signal for issue #475: a task counts as
// ticket-bound only when at least one of its source bindings points at a
// source whose configured type is a ticket tracker (not "plugin"). A task
// with no bindings, or bindings only to plugin-bridged sources, is not.
func TestHasTicket(t *testing.T) {
	a := &App{cfg: &config.Config{Sources: []config.SourceConfig{
		{ID: "jira", Type: "jira"},
		{ID: "github", Type: "github"},
		{ID: "routines", Type: "plugin"},
	}}}

	cases := []struct {
		name string
		item TaskItem
		want bool
	}{
		{"no bindings", TaskItem{}, false},
		{"jira binding", TaskItem{Bindings: []SourceBindingItem{{SourceID: "jira"}}}, true},
		{"routine binding only", TaskItem{Bindings: []SourceBindingItem{{SourceID: "routines"}}}, false},
		{"mixed bindings", TaskItem{Bindings: []SourceBindingItem{{SourceID: "routines"}, {SourceID: "github"}}}, true},
		{"unknown source id defaults ticket-bound", TaskItem{Bindings: []SourceBindingItem{{SourceID: "removed-source"}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.hasTicket(tc.item); got != tc.want {
				t.Errorf("hasTicket(%+v) = %v, want %v", tc.item, got, tc.want)
			}
		})
	}
}

// TestTicketsOnlyToggle checks that Shift+T flips TasksTab.TicketsOnly only
// from the task list view, and that filteredTasks then hides non-ticket rows
// while the default (off) leaves the list unchanged (additive per #475).
func TestTicketsOnlyToggle(t *testing.T) {
	a := &App{
		model: NewModel(),
		cfg: &config.Config{Sources: []config.SourceConfig{
			{ID: "jira", Type: "jira"},
			{ID: "routines", Type: "plugin"},
		}},
	}
	a.model.activeTab = 1 // Tasks
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "t1", Bindings: []SourceBindingItem{{SourceID: "jira"}}},
		{TaskID: "t2", Bindings: []SourceBindingItem{{SourceID: "routines"}}},
	}

	if a.model.tasksTab.TicketsOnly {
		t.Fatal("TicketsOnly must default to off")
	}
	if got := a.filteredTasks(a.model.tasksTab); len(got) != 2 {
		t.Fatalf("default: filteredTasks() = %d items, want 2 (unfiltered)", len(got))
	}

	a.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T")})
	if !a.model.tasksTab.TicketsOnly {
		t.Fatal("Shift+T should turn TicketsOnly on")
	}
	got := a.filteredTasks(a.model.tasksTab)
	if len(got) != 1 || got[0].TaskID != "t1" {
		t.Fatalf("tickets-only: filteredTasks() = %+v, want just t1", got)
	}

	a.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T")})
	if a.model.tasksTab.TicketsOnly {
		t.Fatal("Shift+T should toggle back off")
	}
}
