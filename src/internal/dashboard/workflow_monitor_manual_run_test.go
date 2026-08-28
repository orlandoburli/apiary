package dashboard

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// monitorTab builds a Tasks tab sitting in the workflow monitor on ISSUE-9.
func monitorTab() *TasksTab {
	insts := []*WorkflowInstanceItem{
		{ID: "i1", Workflow: "triage", State: "done", CellID: "ISSUE-9", Steps: []WorkflowStepItem{{StepID: "x"}}},
	}
	return &TasksTab{
		View:              TaskViewWorkflow,
		WorkflowTaskID:    "ISSUE-9",
		WorkflowTaskLabel: "ERP-9 — Ship the thing",
		WorkflowInstances: insts,
		WorkflowInstance:  insts[0],
		WorkflowStepIdx:   0,
	}
}

// TestManualRunArmsMonitorRefresh verifies that starting a workflow with the
// picker while the monitor is open arms the instance-list refresh, so the new
// run appears without leaving the screen and coming back.
func TestManualRunArmsMonitorRefresh(t *testing.T) {
	a := &App{model: NewModel()}
	a.model.tasksTab = monitorTab()

	a.awaitManualRunInMonitor("ISSUE-9")
	if a.model.tasksTab.WorkflowAwaitTicks != wfMonitorAwaitTicks {
		t.Fatalf("monitor should await the new instance, got %d ticks", a.model.tasksTab.WorkflowAwaitTicks)
	}

	// A run started against a different task must not arm this monitor.
	a.model.tasksTab = monitorTab()
	a.awaitManualRunInMonitor("ISSUE-42")
	if a.model.tasksTab.WorkflowAwaitTicks != 0 {
		t.Errorf("a run on another task should not arm the monitor, got %d ticks", a.model.tasksTab.WorkflowAwaitTicks)
	}
}

// TestWorkflowInstancesRefreshSwitchesToNewInstance verifies the monitor jumps to
// a newly created instance when one appears, and otherwise keeps the instance the
// user is looking at (with its step cursor).
func TestWorkflowInstancesRefreshSwitchesToNewInstance(t *testing.T) {
	a := &App{model: NewModel()}
	tab := monitorTab()
	tab.WorkflowAwaitTicks = wfMonitorAwaitTicks
	a.model.tasksTab = tab

	newer := &WorkflowInstanceItem{ID: "i2", Workflow: "implementation", State: "running", CellID: "ISSUE-9"}
	a.Update(workflowInstancesRefreshMsg{
		taskID:    "ISSUE-9",
		instances: []*WorkflowInstanceItem{newer, tab.WorkflowInstances[0]},
	})

	if tab.WorkflowInstance == nil || tab.WorkflowInstance.ID != "i2" || tab.WorkflowInstanceIdx != 0 {
		t.Fatalf("monitor should show the new instance i2, got %+v idx=%d", tab.WorkflowInstance, tab.WorkflowInstanceIdx)
	}
	if tab.WorkflowAwaitTicks != 0 {
		t.Errorf("await should be cleared once the instance appeared, got %d", tab.WorkflowAwaitTicks)
	}

	// A refresh with the same set keeps the shown instance and its step cursor.
	tab.WorkflowStepIdx = 3
	a.Update(workflowInstancesRefreshMsg{
		taskID:    "ISSUE-9",
		instances: []*WorkflowInstanceItem{newer, tab.WorkflowInstances[1]},
	})
	if tab.WorkflowInstance.ID != "i2" || tab.WorkflowStepIdx != 3 {
		t.Errorf("unchanged set should keep instance and cursor, got %s idx=%d", tab.WorkflowInstance.ID, tab.WorkflowStepIdx)
	}

	// A refresh for another task is dropped.
	a.Update(workflowInstancesRefreshMsg{taskID: "ISSUE-42", instances: []*WorkflowInstanceItem{{ID: "zz"}}})
	if tab.WorkflowInstance.ID != "i2" {
		t.Errorf("refresh for another task should be ignored, got %s", tab.WorkflowInstance.ID)
	}
}

// TestFetchActiveTabRefreshesInstancesWhileAwaiting verifies the refresh tick
// re-lists instances only while a manual run is pending, and gives up eventually.
func TestFetchActiveTabRefreshesInstancesWhileAwaiting(t *testing.T) {
	a := &App{model: NewModel()}
	tab := monitorTab()
	a.model.tasksTab = tab
	a.model.activeTab = tabIndex(a.model, "Tasks")

	tab.WorkflowAwaitTicks = 2
	for i := 0; i < 2; i++ {
		if cmd := a.fetchActiveTab(); cmd == nil {
			t.Fatalf("tick %d should have produced refresh commands", i)
		}
	}
	if tab.WorkflowAwaitTicks != 0 {
		t.Fatalf("await budget should be spent, got %d", tab.WorkflowAwaitTicks)
	}
}

// TestWorkflowMonitorShowsTaskLabel verifies the monitor's title names the task
// being watched — without it the screen shows a workflow id and nothing else.
func TestWorkflowMonitorShowsTaskLabel(t *testing.T) {
	a := &App{model: NewModel()}
	a.model.width = 160
	a.model.height = 40
	tab := monitorTab()
	a.model.tasksTab = tab

	out := ansi.Strip(a.renderWorkflowMonitor(tab, 20))
	if !strings.Contains(out, "ERP-9 — Ship the thing") {
		t.Errorf("monitor title should name the task, got:\n%s", strings.SplitN(out, "\n", 2)[0])
	}
}

// TestTaskLabelFromHistory verifies the label used by the monitor header is
// resolved from the loaded task rows, by task id or legacy drill key.
func TestTaskLabelFromHistory(t *testing.T) {
	a := &App{model: NewModel()}
	a.model.tasksTab = &TasksTab{History: []TaskItem{
		{TaskID: "t-1", DrillKey: "ISSUE-9", Number: "ERP-9", Title: "Ship the thing"},
		{TaskID: "t-2", Title: "Untitled number-less"},
	}}

	if got := a.taskLabel("ISSUE-9"); got != "ERP-9 — Ship the thing" {
		t.Errorf("drill-key lookup: got %q", got)
	}
	if got := a.taskLabel("t-2"); got != "Untitled number-less" {
		t.Errorf("title-only lookup: got %q", got)
	}
	if got := a.taskLabel("nope"); got != "nope" {
		t.Errorf("unknown task should fall back to its id, got %q", got)
	}
}

// tabIndex finds the index of a tab by name so the test can activate it.
func tabIndex(m *Model, name string) int {
	for i, tb := range m.tabs {
		if tb == name {
			return i
		}
	}
	return 0
}
