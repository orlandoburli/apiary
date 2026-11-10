package dashboard

import (
	"testing"
	"time"
)

// TestFocusedTaskURLBoardUsesTheCardNotTheListCursor covers #527: the board
// addresses a task by (column, row), so (o) must open the card under the board
// cursor rather than the row at the list's SelectedIdx.
func TestFocusedTaskURLBoardUsesTheCardNotTheListCursor(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.activeTab = 1 // Tasks
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "list-row", Title: "row under the list cursor", Status: "done",
			URL: "https://example.test/done"},
		{TaskID: "board-card", Title: "card under the board cursor", Status: "running",
			URL: "https://example.test/running"},
	}
	a.model.tasksTab.View = TaskViewBoard
	a.model.tasksTab.SelectedIdx = 0 // the list cursor sits on list-row
	a.model.tasksTab.BoardCol = 1    // RUNNING
	a.model.tasksTab.BoardRow = 0    // board-card

	got, ok := a.focusedTaskURL()
	if !ok || got != "https://example.test/running" {
		t.Fatalf("focusedTaskURL on the board = %q (%v), want the board card's URL", got, ok)
	}
}

// TestFocusedTaskIDBoardUsesTheCard guards the board gap the shared resolver
// closes: focusedTaskID used to fall through to the list cursor on the board,
// so id-consuming actions (transcript, picker, restart wiring) targeted a
// different task than the one (o)/(p) opened.
func TestFocusedTaskIDBoardUsesTheCard(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.activeTab = 1 // Tasks
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "list-row", Status: "done"},
		{TaskID: "board-card", Status: "running"},
	}
	a.model.tasksTab.View = TaskViewBoard
	a.model.tasksTab.SelectedIdx = 0 // list cursor: list-row
	a.model.tasksTab.BoardCol = 1    // RUNNING
	a.model.tasksTab.BoardRow = 0    // board-card

	if id, ok := a.focusedTaskID(); !ok || id != "board-card" {
		t.Fatalf("focusedTaskID on the board = %q (%v), want the board card's id", id, ok)
	}
}

// TestFocusedTaskOnNarrowBoardFollowsTheRenderedList: below the board's
// legibility floor renderTasksTab draws the list while View stays
// TaskViewBoard. The resolver must then act on the row the user can actually
// see, not on a board cursor that is no longer drawn.
func TestFocusedTaskOnNarrowBoardFollowsTheRenderedList(t *testing.T) {
	a := newTestApp(30, 20) // inner width 28 < boardMinWidth (40)
	a.model.activeTab = 1   // Tasks
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "list-row", Status: "done", URL: "https://example.test/list-row"},
		{TaskID: "board-card", Status: "running", URL: "https://example.test/board-card"},
	}
	a.model.tasksTab.View = TaskViewBoard
	a.model.tasksTab.SelectedIdx = 0 // the visible list row
	a.model.tasksTab.BoardCol = 1    // RUNNING
	a.model.tasksTab.BoardRow = 0    // board-card — not what is drawn

	if out := a.renderBoard(a.model.tasksTab, 14); out != "" {
		t.Fatal("precondition: the board should decline to render below its floor")
	}
	if u, ok := a.focusedTaskURL(); !ok || u != "https://example.test/list-row" {
		t.Fatalf("focusedTaskURL on a narrow board = %q (%v), want the rendered list row's URL", u, ok)
	}
	if id, ok := a.focusedTaskID(); !ok || id != "list-row" {
		t.Fatalf("focusedTaskID on a narrow board = %q (%v), want the rendered list row's id", id, ok)
	}
}

// TestFocusedTaskURLWorkflowMonitorIgnoresListCursor covers the monitor half of
// #527: the monitor carries its own task id, so (o) must not follow the list
// cursor — including after the list is re-sorted underneath it.
func TestFocusedTaskURLWorkflowMonitorIgnoresListCursor(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.activeTab = 1 // Tasks
	newer := time.Unix(200, 0)
	older := time.Unix(100, 0)
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "cursor-task", Title: "cursor", Status: "running",
			URL: "https://example.test/cursor", StartedAt: &newer},
		{TaskID: "wf-task", Title: "monitored", Status: "done",
			URL: "https://example.test/monitored", StartedAt: &older},
	}
	a.model.tasksTab.View = TaskViewWorkflow
	a.model.tasksTab.WorkflowTaskID = "wf-task"
	a.model.tasksTab.SelectedIdx = 0 // the list cursor sits on cursor-task

	if got, ok := a.focusedTaskURL(); !ok || got != "https://example.test/monitored" {
		t.Fatalf("focusedTaskURL in the monitor = %q (%v), want the monitor's task URL", got, ok)
	}

	// Re-sort the list underneath (oldest first now puts the monitored task at
	// the top) and move the cursor. The monitor still targets its own task.
	a.model.tasksTab.SortAsc = true
	a.model.tasksTab.SelectedIdx = 1 // now cursor-task
	if got, ok := a.focusedTaskURL(); !ok || got != "https://example.test/monitored" {
		t.Fatalf("focusedTaskURL after re-sort = %q (%v), want the monitor's task URL", got, ok)
	}
}

// TestFocusedTaskPullsBoardAndMonitor checks the (p) resolver against both
// views: the PRs must come from the focused card/monitored task, not the row
// under the list cursor.
func TestFocusedTaskPullsBoardAndMonitor(t *testing.T) {
	boardPRs := []PullRequestItem{
		{Number: 1, URL: "https://example.test/pr/1"},
		{Number: 2, URL: "https://example.test/pr/2"}, // oldest first: last = most recent
	}
	listPRs := []PullRequestItem{{Number: 9, URL: "https://example.test/pr/9"}}

	a := newTestApp(120, 30)
	a.model.activeTab = 1 // Tasks
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "list-row", Status: "done", PullRequests: listPRs},
		{TaskID: "board-card", Status: "running", PullRequests: boardPRs},
	}
	a.model.tasksTab.View = TaskViewBoard
	a.model.tasksTab.SelectedIdx = 0
	a.model.tasksTab.BoardCol = 1 // RUNNING
	a.model.tasksTab.BoardRow = 0 // board-card

	prs, ok := a.focusedTaskPulls()
	if !ok || len(prs) != 2 || prs[0].URL != boardPRs[0].URL {
		t.Fatalf("focusedTaskPulls on the board = %+v (%v), want the board card's PRs", prs, ok)
	}
	if u, ok := a.focusedTaskPRURL(); !ok || u != boardPRs[1].URL {
		t.Fatalf("focusedTaskPRURL on the board = %q (%v), want the most recent PR", u, ok)
	}

	a.model.tasksTab.View = TaskViewWorkflow
	a.model.tasksTab.WorkflowTaskID = "list-row"
	a.model.tasksTab.SelectedIdx = 1 // cursor on board-card
	prs, ok = a.focusedTaskPulls()
	if !ok || len(prs) != 1 || prs[0].URL != listPRs[0].URL {
		t.Fatalf("focusedTaskPulls in the monitor = %+v (%v), want the monitor's task PRs", prs, ok)
	}
	if u, ok := a.focusedTaskPRURL(); !ok || u != listPRs[0].URL {
		t.Fatalf("focusedTaskPRURL in the monitor = %q (%v), want the monitor's PR", u, ok)
	}
}

// TestFocusedTaskIDWorkflowMonitor is the regression guard for the one resolver
// that already handled the monitor: it must keep returning WorkflowTaskID, even
// when the task's row has scrolled out of the loaded list window.
func TestFocusedTaskIDWorkflowMonitor(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.activeTab = 1 // Tasks
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "cursor-task"},
		{TaskID: "wf-task"},
	}
	a.model.tasksTab.View = TaskViewWorkflow
	a.model.tasksTab.WorkflowTaskID = "wf-task"
	a.model.tasksTab.SelectedIdx = 0

	if id, ok := a.focusedTaskID(); !ok || id != "wf-task" {
		t.Fatalf("focusedTaskID in the monitor = %q (%v), want wf-task", id, ok)
	}

	// A row that is no longer loaded must not retarget the id...
	a.model.tasksTab.WorkflowTaskID = "scrolled-away"
	if id, ok := a.focusedTaskID(); !ok || id != "scrolled-away" {
		t.Fatalf("focusedTaskID for an unloaded row = %q (%v), want scrolled-away", id, ok)
	}
	// ...and the URL resolver degrades to nothing rather than opening the
	// wrong task.
	if u, ok := a.focusedTaskURL(); ok {
		t.Errorf("focusedTaskURL for an unloaded row = %q, want no URL", u)
	}
}

// TestWorkflowFooterIncludesOpenKeys covers the hint half of #527: the monitor
// footer must advertise the same (o)/(p) keys the handler already accepts.
func TestWorkflowFooterIncludesOpenKeys(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.activeTab = 1 // Tasks
	a.model.tasksTab.View = TaskViewWorkflow

	keys := func() map[string]bool {
		out := map[string]bool{}
		for _, f := range a.footerKeys() {
			out[f.k] = true
		}
		return out
	}

	got := keys()
	if !got["o"] || !got["p"] {
		t.Errorf("workflow monitor footer missing o/p hints; keys = %v", got)
	}

	// The expanded log panel keeps the same task focused, so the hints stay.
	a.model.tasksTab.WorkflowShowLogs = true
	got = keys()
	if !got["o"] || !got["p"] {
		t.Errorf("workflow log-panel footer missing o/p hints; keys = %v", got)
	}
}

// TestFocusedTaskURLAndPullsOnAgentsUnchanged guards the Agents path shared by
// the resolvers: the focused activity row still supplies the URL and PRs.
func TestFocusedTaskURLAndPullsOnAgentsUnchanged(t *testing.T) {
	a := newTestApp(90, 20)
	a.model.activeTab = 2 // Agents
	a.model.agentsTab.View = AgentViewActivity
	a.model.agentsTab.Activity = []TaskItem{
		{TaskID: "A-1", URL: "https://example.test/agent/1",
			PullRequests: []PullRequestItem{{Number: 2, URL: "https://example.test/agent/pr/2"}}},
		{TaskID: "A-2", URL: "https://example.test/agent/2"},
	}

	a.model.agentsTab.ActivityIdx = 1
	if u, ok := a.focusedTaskURL(); !ok || u != "https://example.test/agent/2" {
		t.Fatalf("focusedTaskURL on Agents = %q (%v), want the selected activity URL", u, ok)
	}
	prs, ok := a.focusedTaskPulls()
	if !ok || len(prs) != 0 {
		t.Fatalf("focusedTaskPulls on Agents = %+v (%v), want an empty list for a row without PRs", prs, ok)
	}

	a.model.agentsTab.ActivityIdx = 0
	prs, ok = a.focusedTaskPulls()
	if !ok || len(prs) != 1 || prs[0].URL != "https://example.test/agent/pr/2" {
		t.Fatalf("focusedTaskPulls on Agents = %+v (%v), want the selected activity's PRs", prs, ok)
	}
}
