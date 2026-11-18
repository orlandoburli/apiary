package dashboard

import (
	"strings"
	"testing"
)

// The dashboard can permanently delete the focused task.
//
// It calls the daemon endpoint the `apiary delete` command calls, so the two
// cannot drift: the dashboard is not a second implementation of "delete a
// task", it is a way to reach the one that exists. The daemon does the work —
// it cancels the run in flight and removes the instances, steps, bindings, logs
// and the task row — because the daemon is what knows what else is holding a
// reference to the task.
func TestDeleteTaskKeyOpensTheConfirmation(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.activeTab = tabIndex(a.model, "Tasks")
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "T-1", Title: "first"},
		{TaskID: "T-2", Title: "second"},
	}
	a.model.tasksTab.SelectedIdx = 1

	// Shift+D asks first: this cannot be undone.
	a.handleKeyMsg(keyPress("D"))
	if a.model.confirmAction != "delete" {
		t.Fatalf("confirmAction = %q, want delete", a.model.confirmAction)
	}
	if a.model.confirmTaskID != "T-2" {
		t.Errorf("confirmTaskID = %q, want T-2 (the focused task)", a.model.confirmTaskID)
	}
}

// The confirmation has to say what is about to happen and that it cannot be
// undone — a delete with no way back deserves a dialog that says so.
func TestDeleteConfirmationWarnsItCannotBeUndone(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.confirmAction = "delete"
	a.model.confirmTaskID = "T-1"

	out := stripANSI(a.renderConfirmModal("Tasks"))
	if !strings.Contains(out, "Delete task") {
		t.Errorf("the confirmation does not name what it deletes:\n%s", out)
	}
	if !strings.Contains(out, "cannot be undone") {
		t.Errorf("the confirmation does not warn that it cannot be undone:\n%s", out)
	}
}

// Declining must leave the task alone.
func TestDeleteConfirmationCanBeDeclined(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.activeTab = tabIndex(a.model, "Tasks")
	a.model.tasksTab.History = []TaskItem{{TaskID: "T-1"}}
	a.model.confirmAction = "delete"
	a.model.confirmTaskID = "T-1"

	a.handleKeyMsg(keyPress("n"))
	if a.model.confirmAction != "" {
		t.Errorf("confirmAction = %q, want it cleared when the prompt is declined", a.model.confirmAction)
	}
}

// The Running tab lists the processes working right now, and a task acted on
// from there is the one under the cursor.
func TestDeleteKeyWorksFromTheRunningTab(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.activeTab = tabIndex(a.model, "Running")
	a.model.runningTab.Agents = []RunningAgent{
		{TaskID: "T-1", Title: "first"},
		{TaskID: "T-2", Title: "second"},
	}
	a.model.runningTab.SelectedIdx = 1

	a.handleKeyMsg(keyPress("D"))
	if a.model.confirmAction != "delete" {
		t.Fatalf("confirmAction = %q, want delete", a.model.confirmAction)
	}
	if a.model.confirmTaskID != "T-2" {
		t.Errorf("confirmTaskID = %q, want T-2 (the row under the cursor)", a.model.confirmTaskID)
	}
}
