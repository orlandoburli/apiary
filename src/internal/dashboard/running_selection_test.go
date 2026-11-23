package dashboard

import (
	"fmt"
	"strings"
	"testing"
)

// runningTestApp builds an App parked on the Running tab with the given rows.
func runningTestApp(agents []RunningAgent, waiting []WaitingInstance, cursor int) *App {
	a := newTestApp(120, 40)
	a.model.activeTab = tabIndex(a.model, "Running")
	a.model.runningTab.Agents = agents
	a.model.runningTab.Waiting = waiting
	a.model.runningTab.SelectedIdx = cursor
	return a
}

// The cursor is one cursor over both sections: ↓ past the last running agent
// lands on the first waiting workflow, and enter resolves to its task. Before
// this, the cursor stopped dead at the section boundary and the waiting list
// could only be read, never acted on.
func TestRunningCursorCrossesIntoTheWaitingSection(t *testing.T) {
	a := runningTestApp(
		[]RunningAgent{{TaskID: "run-1"}},
		[]WaitingInstance{{TaskID: "wait-1", WaitReason: "approval"}},
		0,
	)

	a.handleKeyMsg(keyPress("down"))

	if got := a.model.runningTab.SelectedIdx; got != 1 {
		t.Fatalf("SelectedIdx after ↓ = %d, want 1 (the waiting row)", got)
	}
	if id, ok := a.selectedRunningTaskID(); !ok || id != "wait-1" {
		t.Errorf("selectedRunningTaskID = %q (%v), want wait-1", id, ok)
	}
}

// Enter on a waiting row must open that task's workflow monitor, the same
// place Enter opens for a running agent.
func TestEnterOnAWaitingRowOpensItsWorkflow(t *testing.T) {
	a := runningTestApp(
		nil,
		[]WaitingInstance{{TaskID: "wait-1", WaitReason: "ci"}},
		0,
	)

	a.handleKeyMsg(keyPress("enter"))

	// openWorkflowMonitorOrLogs sets loading while it fetches.
	if !a.model.loading {
		t.Error("enter on a waiting row did not start a navigation")
	}
}

// The cursor indexes the ranked order the section draws, not the raw fetch
// order: the row under the cursor must be the row highlighted. A dead run
// ranks last, so it must not sit under the cursor just because the query
// returned it first.
func TestWaitingCursorFollowsTheDrawnOrder(t *testing.T) {
	a := runningTestApp(
		nil,
		[]WaitingInstance{
			{TaskID: "dead", WaitReason: "interrupted"},
			{TaskID: "approval", WaitReason: "approval"},
		},
		0,
	)

	if id, ok := a.selectedRunningTaskID(); !ok || id != "approval" {
		t.Fatalf("cursor at 0 resolved to %q, want the approval row", id)
	}
	a.handleKeyMsg(keyPress("down"))
	if id, ok := a.selectedRunningTaskID(); !ok || id != "dead" {
		t.Errorf("cursor at 1 resolved to %q, want the interrupted row", id)
	}
}

// End reaches the last waiting row, and the cursor cannot run past it.
func TestRunningCursorStopsAtTheLastWaitingRow(t *testing.T) {
	a := runningTestApp(
		[]RunningAgent{{TaskID: "run-1"}},
		[]WaitingInstance{{TaskID: "wait-1"}, {TaskID: "wait-2"}},
		0,
	)

	a.handleKeyMsg(keyPress("end"))
	if got := a.model.runningTab.SelectedIdx; got != 2 {
		t.Fatalf("SelectedIdx after end = %d, want 2", got)
	}
	a.handleKeyMsg(keyPress("down"))
	if got := a.model.runningTab.SelectedIdx; got != 2 {
		t.Errorf("SelectedIdx after ↓ at the end = %d, want it to stay at 2", got)
	}
}

// A refresh that shrinks the list must clamp the cursor, not reset it: the
// list is rebuilt on every poll and resetting would yank the user back to the
// top while they are watching a row.
func TestRunningCursorClampsInsteadOfResetting(t *testing.T) {
	a := runningTestApp(
		[]RunningAgent{{TaskID: "run-1"}, {TaskID: "run-2"}},
		[]WaitingInstance{{TaskID: "wait-1"}},
		2,
	)

	a.model.runningTab.Agents = []RunningAgent{{TaskID: "run-1"}}
	a.model.runningTab.Waiting = nil
	a.model.runningTab.ClampCursor()

	if got := a.model.runningTab.SelectedIdx; got != 0 {
		t.Errorf("SelectedIdx = %d after the list shrank, want it clamped to 0", got)
	}
}

// The waiting window must always contain the row under the cursor. The box
// silently drops what does not fit, so a selected row outside the window is
// simply invisible.
func TestWaitingWindowKeepsTheSelectionVisible(t *testing.T) {
	ordered := make([]WaitingInstance, 10)
	for i := range ordered {
		ordered[i] = WaitingInstance{TaskID: fmt.Sprintf("t-%d", i)}
	}

	// The cursor at the end scrolls the window down to it.
	shown, above, below, base := waitingWindow(ordered, 4, 9)
	if base+len(shown)-1 != 9 {
		t.Fatalf("window %d..%d does not end on the cursor at 9", base, base+len(shown)-1)
	}
	if above != 6 || below != 0 {
		t.Errorf("above=%d below=%d, want 6 and 0", above, below)
	}

	// A cursor in the middle hides rows on both sides; the window gives a row
	// back so the two "… and more" lines fit, and still contains the cursor.
	shown, above, below, base = waitingWindow(ordered, 4, 5)
	if base > 5 || base+len(shown) <= 5 {
		t.Fatalf("window %d..%d does not contain the cursor at 5", base, base+len(shown))
	}
	if above == 0 || below == 0 {
		t.Errorf("above=%d below=%d, want rows hidden on both sides", above, below)
	}
	if len(shown) != 3 {
		t.Errorf("len(shown) = %d, want 3 (one row given back for the second line)", len(shown))
	}

	// A list that fits is drawn whole, with nothing counted as hidden.
	shown, above, below, base = waitingWindow(ordered[:2], 4, 1)
	if len(shown) != 2 || above != 0 || below != 0 || base != 0 {
		t.Errorf("short list window = %d rows, above=%d below=%d base=%d, want it all", len(shown), above, below, base)
	}
}

// The rendered waiting row carries the cursor marker only when selected, so the
// highlight and the key handling cannot disagree — both read the same index.
func TestRenderWaitingRowMarksTheSelectedRow(t *testing.T) {
	a := newTestApp(120, 40)

	selected := a.renderWaitingRow(WaitingInstance{WorkflowID: "wf", TaskID: "T-1"}, true)
	if !strings.Contains(selected, "▶") {
		t.Errorf("the selected waiting row has no cursor: %q", selected)
	}
	if plain := a.renderWaitingRow(WaitingInstance{WorkflowID: "wf", TaskID: "T-1"}, false); strings.Contains(plain, "▶") {
		t.Errorf("an unselected waiting row carries a cursor: %q", plain)
	}
}
