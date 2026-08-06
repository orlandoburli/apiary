package dashboard

import (
	"strings"
	"testing"
)

// monitorApp builds an App sitting on the workflow monitor with a few steps.
func monitorApp(t *testing.T, stepIdx int) *App {
	t.Helper()
	a := newTestApp(120, 30)
	a.model.tasksTab = &TasksTab{
		View: TaskViewWorkflow,
		WorkflowInstance: &WorkflowInstanceItem{
			ID:       "wf_layout",
			Workflow: "implementation",
			State:    "running",
			Steps: []WorkflowStepItem{
				{StepID: "plan", Agent: "architect", State: "passed", Duration: "12s"},
				{StepID: "implement", Agent: "backend-dev", State: "running", Duration: "3s"},
				{StepID: "review", Agent: "reviewer", State: "pending", Duration: "—"},
			},
		},
		WorkflowStepIdx: stepIdx,
	}
	a.model.tasksTab.WorkflowInstances = []*WorkflowInstanceItem{a.model.tasksTab.WorkflowInstance}
	return a
}

// stepRows returns the left panel's step rows only. The panels are joined
// horizontally and the right one repeats the step name, so a whole-line match
// would also pick up detail-panel text (and silently compare the wrong lines).
func stepRows(t *testing.T, a *App) []string {
	t.Helper()
	out := stripANSI(a.renderWorkflowMonitor(a.model.tasksTab, 20))
	var rows []string
	for _, line := range strings.Split(out, "\n") {
		// │ left panel │ right panel │ — take the left cell only.
		cells := strings.Split(line, "│")
		if len(cells) < 3 {
			continue
		}
		left := cells[1]
		for _, name := range []string{"plan", "implement", "review"} {
			// Anchor on the step-name column so the header row is excluded.
			if strings.Contains(left, name) {
				rows = append(rows, left)
				break
			}
		}
	}
	return rows
}

// columnStarts records where each of the step row's columns begins, so rows can
// be compared to each other regardless of which one is selected.
func columnStarts(row string) []int {
	var starts []int
	inField := false
	for i, r := range row {
		switch {
		case r != ' ' && !inField:
			starts = append(starts, i)
			inField = true
		case r == ' ' && inField:
			inField = false
		}
	}
	return starts
}

// Regression: the selected row used to be built from unpadded fields, so
// selecting a step collapsed its columns and the row jumped out of alignment
// with its neighbours.
func TestWorkflowMonitorSelectedRowKeepsColumnAlignment(t *testing.T) {
	// The agent column is what visibly shifts, and it differs per row, so
	// compare the same row selected vs unselected.
	unselected := stepRows(t, monitorApp(t, 0))
	selected := stepRows(t, monitorApp(t, 1))

	if len(unselected) < 3 || len(selected) < 3 {
		t.Fatalf("expected 3 step rows, got %d and %d", len(unselected), len(selected))
	}

	// Row 1 ("implement") is unselected in the first render and selected in the
	// second; its columns must land in the same places either way.
	wantStarts := columnStarts(unselected[1])
	gotStarts := columnStarts(selected[1])
	if len(wantStarts) != len(gotStarts) {
		t.Fatalf("selected row has %d columns, unselected has %d:\n unselected: %q\n selected:   %q",
			len(gotStarts), len(wantStarts), unselected[1], selected[1])
	}
	for i := range wantStarts {
		if wantStarts[i] != gotStarts[i] {
			t.Errorf("column %d starts at %d when selected, %d when not:\n unselected: %q\n selected:   %q",
				i, gotStarts[i], wantStarts[i], unselected[1], selected[1])
		}
	}
}

// Every row must start its columns at the same offsets, whichever is selected.
func TestWorkflowMonitorAllRowsShareColumnOffsets(t *testing.T) {
	for sel := 0; sel < 3; sel++ {
		rows := stepRows(t, monitorApp(t, sel))
		if len(rows) < 3 {
			t.Fatalf("selection %d: expected 3 step rows, got %d", sel, len(rows))
		}
		// Every column is padded to a fixed width, so all rows — selected or not
		// — must place their columns at identical offsets.
		want := columnStarts(rows[0])
		for i, row := range rows[1:] {
			got := columnStarts(row)
			if len(got) != len(want) {
				t.Errorf("selection %d: row %d has %d columns, row 0 has %d:\n %q\n %q",
					sel, i+1, len(got), len(want), rows[0], row)
				continue
			}
			for c := range want {
				if got[c] != want[c] {
					t.Errorf("selection %d: row %d column %d starts at %d, row 0 at %d:\n %q\n %q",
						sel, i+1, c, got[c], want[c], rows[0], row)
				}
			}
		}
	}
}

func TestWorkflowMonitorSplitDefaultsAndResizes(t *testing.T) {
	a := monitorApp(t, 0)
	tab := a.model.tasksTab

	baseLeft, baseRight := a.wfMonitorPanelWidths()

	// Narrowing the step list must give the detail panel the room back.
	adjustWorkflowSplit(tab, -wfMonitorSplitStepPct)
	narrowLeft, narrowRight := a.wfMonitorPanelWidths()
	if narrowLeft >= baseLeft {
		t.Errorf("step list did not narrow: %d → %d", baseLeft, narrowLeft)
	}
	if narrowRight <= baseRight {
		t.Errorf("detail panel did not widen: %d → %d", baseRight, narrowRight)
	}

	// And widening it takes the room back.
	adjustWorkflowSplit(tab, wfMonitorSplitStepPct)
	if gotLeft, _ := a.wfMonitorPanelWidths(); gotLeft != baseLeft {
		t.Errorf("split did not return to the default width: got %d, want %d", gotLeft, baseLeft)
	}
}

func TestWorkflowMonitorSplitClampsToReadableRange(t *testing.T) {
	tab := &TasksTab{}
	for i := 0; i < 40; i++ {
		adjustWorkflowSplit(tab, -wfMonitorSplitStepPct)
	}
	if tab.WorkflowSplitPct != wfMonitorMinSplitPct {
		t.Errorf("split floor = %d, want %d", tab.WorkflowSplitPct, wfMonitorMinSplitPct)
	}
	for i := 0; i < 40; i++ {
		adjustWorkflowSplit(tab, wfMonitorSplitStepPct)
	}
	if tab.WorkflowSplitPct != wfMonitorMaxSplitPct {
		t.Errorf("split ceiling = %d, want %d", tab.WorkflowSplitPct, wfMonitorMaxSplitPct)
	}
}

// The split keys must work while the log panel is open — that is the view you
// are in when you want more room for output.
func TestWorkflowMonitorSplitKeysWorkInLogView(t *testing.T) {
	a := monitorApp(t, 0)
	a.model.tasksTab.WorkflowShowLogs = true

	a.handleWorkflowMonitorKey("-")
	if got := a.model.tasksTab.WorkflowSplitPct; got != wfMonitorDefaultSplitPct-wfMonitorSplitStepPct {
		t.Errorf("'-' in log view: split = %d, want %d", got, wfMonitorDefaultSplitPct-wfMonitorSplitStepPct)
	}
	a.handleWorkflowMonitorKey("+")
	if got := a.model.tasksTab.WorkflowSplitPct; got != wfMonitorDefaultSplitPct {
		t.Errorf("'+' in log view: split = %d, want %d", got, wfMonitorDefaultSplitPct)
	}
}

// Resizing must not disturb the step cursor or the log panel.
func TestWorkflowMonitorSplitKeepsSelectionAndLogPanel(t *testing.T) {
	a := monitorApp(t, 2)
	a.model.tasksTab.WorkflowShowLogs = true
	a.model.tasksTab.WorkflowLogStepID = "review"

	a.handleWorkflowMonitorKey("-")

	if got := a.model.tasksTab.WorkflowStepIdx; got != 2 {
		t.Errorf("step cursor moved on resize: got %d, want 2", got)
	}
	if !a.model.tasksTab.WorkflowShowLogs {
		t.Error("log panel closed on resize")
	}
	if got := a.model.tasksTab.WorkflowLogStepID; got != "review" {
		t.Errorf("log panel re-scoped on resize: got %q, want \"review\"", got)
	}
}
