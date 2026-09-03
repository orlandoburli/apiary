package dashboard

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/state"
)

// approvalsFixtureApp returns an App on the Overview tab with a Tasks history
// containing one task parked on an approval gate, one blocked on something
// else (a CI wait), and one running normally — the mix a real dashboard sees.
func approvalsFixtureApp() *App {
	a := &App{model: NewModel()}
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "t1", Title: "waiting on approval", Status: "approval_waiting"},
		{TaskID: "t2", Title: "blocked on ci", Status: string(state.Blocked), BlockedReason: "ci"},
		{TaskID: "t3", Title: "running fine", Status: "running"},
	}
	return a
}

// Pressing Shift+A from the Overview tab jumps straight to the Tasks list,
// filtered to only the instances parked on a human approval gate — the
// dashboard-affordance ask of #476.
func TestApprovalsShortcutJumpsToFilteredTaskList(t *testing.T) {
	a := approvalsFixtureApp()
	if got := a.model.ActiveTab(); got != "Overview" {
		t.Fatalf("fixture should start on Overview, got %q", got)
	}

	a.handleKeyMsg(keyPress("A"))

	if got := a.model.ActiveTab(); got != "Tasks" {
		t.Fatalf("Shift+A should switch to the Tasks tab, got %q", got)
	}
	tt := a.model.tasksTab
	if tt.View != TaskViewList {
		t.Fatalf("Shift+A should land on the task list, got view %v", tt.View)
	}
	if !tt.ApprovalsOnly {
		t.Fatal("Shift+A should turn on the approvals-only filter")
	}

	items := a.filteredTasks(tt)
	if len(items) != 1 || items[0].TaskID != "t1" {
		t.Fatalf("expected only the approval-waiting task, got %+v", items)
	}
}

// Pressing Shift+A again while already on the filtered list turns the filter
// back off, restoring the full history.
func TestApprovalsShortcutTogglesOffFromTaskList(t *testing.T) {
	a := approvalsFixtureApp()
	a.model.activeTab = 1 // Tasks
	a.model.tasksTab.ApprovalsOnly = true

	a.handleKeyMsg(keyPress("A"))

	if a.model.tasksTab.ApprovalsOnly {
		t.Fatal("a second Shift+A from the filtered list should turn the filter off")
	}
	if len(a.filteredTasks(a.model.tasksTab)) != 3 {
		t.Fatal("turning the filter off should restore the full task history")
	}
}

// The approvals-only filter recognizes both the modern approval_waiting state
// and the legacy blocked+reason:approval pair, via the same blockedOnApproval
// predicate already used per-instance elsewhere in the dashboard.
func TestApprovalsOnlyFilterRecognizesLegacyBlockedReason(t *testing.T) {
	a := &App{model: NewModel()}
	tt := a.model.tasksTab
	tt.ApprovalsOnly = true
	tt.History = []TaskItem{
		{TaskID: "legacy", Status: string(state.Blocked), BlockedReason: string(state.ReasonApproval)},
		{TaskID: "not-approval", Status: string(state.Blocked), BlockedReason: "dependency"},
	}

	items := a.filteredTasks(tt)
	if len(items) != 1 || items[0].TaskID != "legacy" {
		t.Fatalf("expected only the legacy approval-blocked task, got %+v", items)
	}
}
