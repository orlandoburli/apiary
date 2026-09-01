package dashboard

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/state"
)

func boardItems() []TaskItem {
	return []TaskItem{
		{InternalTaskID: "1", Number: "#1", Title: "queued one", Status: "queued"},
		{InternalTaskID: "2", Number: "#2", Title: "running one", Status: "running"},
		{InternalTaskID: "3", Number: "#3", Title: "waiting on a human", Status: "blocked",
			BlockedReason: string(state.ReasonApproval)},
		{InternalTaskID: "4", Number: "#4", Title: "done one", Status: "done"},
		{InternalTaskID: "5", Number: "#5", Title: "broken one", Status: "failed"},
		{InternalTaskID: "6", Number: "#6", Title: "abandoned", Status: "canceled"},
	}
}

// TestBoardGroups_ColumnsMapOntoCanonicalStates pins the property the board is
// built on: a card is in a column because its state says so, with no derivation
// in between (#465/#466).
func TestBoardGroups_ColumnsMapOntoCanonicalStates(t *testing.T) {
	cols, failed := boardGroups(boardItems())

	for st, wantTitle := range map[state.State]string{
		state.Queued:  "queued one",
		state.Running: "running one",
		state.Blocked: "waiting on a human",
		state.Done:    "done one",
	} {
		if len(cols[st]) != 1 || cols[st][0].Title != wantTitle {
			t.Errorf("column %q = %+v, want the single task %q", st, cols[st], wantTitle)
		}
	}

	// FAILED is a lane, not a column — terminal failure is an exception, not a
	// stage work passes through.
	if len(failed) != 1 || failed[0].Title != "broken one" {
		t.Errorf("failed lane = %+v, want the one failed task", failed)
	}

	// Canceled is operator-initiated and terminal: the operator already knows.
	for _, list := range cols {
		for _, it := range list {
			if it.Status == "canceled" {
				t.Error("a canceled task must not appear on the board")
			}
		}
	}
}

// TestBoardGroups_LegacyStatesStillLand covers a database that has not run the
// state migration: those rows still have to reach a column.
func TestBoardGroups_LegacyStatesStillLand(t *testing.T) {
	cols, _ := boardGroups([]TaskItem{
		{Status: "registered"},
		{Status: "approval_waiting"},
	})
	if len(cols[state.Queued]) != 1 {
		t.Errorf("legacy 'registered' should land in QUEUED, got %+v", cols[state.Queued])
	}
	if len(cols[state.Blocked]) != 1 {
		t.Errorf("legacy 'approval_waiting' should land in BLOCKED, got %+v", cols[state.Blocked])
	}
}

// TestBoardGroups_UnknownStateIsNotDropped: silently losing a task from the
// board would be worse than showing it in an imperfect column.
func TestBoardGroups_UnknownStateIsNotDropped(t *testing.T) {
	cols, failed := boardGroups([]TaskItem{{Status: "some_future_state"}})
	total := len(failed)
	for _, l := range cols {
		total += len(l)
	}
	if total != 1 {
		t.Errorf("an unrecognised state was dropped from the board entirely")
	}
}

func TestBoardLayout_DropsColumnsWhenNarrow(t *testing.T) {
	cases := []struct {
		inner    string
		width    int
		wantCols int
	}{
		{"wide", 120, 4},
		{"medium", 70, 4},
		{"narrow", 60, 3},
		{"very narrow", 45, 2},
		{"below the floor", 30, 0},
	}
	for _, c := range cases {
		got, w := boardLayout(c.width)
		if got != c.wantCols {
			t.Errorf("%s (%d): columns = %d, want %d", c.inner, c.width, got, c.wantCols)
		}
		if got > 0 && w < boardMinColWidth {
			t.Errorf("%s: column width %d is below the minimum %d", c.inner, w, boardMinColWidth)
		}
	}
}

// TestVisibleBoardColumns_DropOrder pins which columns go first when space runs
// out: DONE, then QUEUED — the two an operator is least likely to be acting on.
// BLOCKED and RUNNING survive longest because they are what needs a human.
func TestVisibleBoardColumns_DropOrder(t *testing.T) {
	if got := visibleBoardColumns(3); len(got) != 3 || got[2] != 2 {
		t.Errorf("with 3 columns DONE should be dropped, got %v", got)
	}
	got := visibleBoardColumns(2)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("with 2 columns only RUNNING and BLOCKED should remain, got %v", got)
	}
}

// TestBoardCard_ShowsBlockedReason: the reason is the whole point of looking at
// the BLOCKED column, so it belongs on the card.
func TestBoardCard_ShowsBlockedReason(t *testing.T) {
	it := TaskItem{Number: "#3", Title: "gate", Status: "blocked", BlockedReason: "approval"}
	out := stripANSI(strings.Join(boardCard(it, 20, false), "\n"))
	if !strings.Contains(out, "approval") {
		t.Errorf("card did not show what it is blocked on:\n%s", out)
	}
}

func TestBoardCard_IsFixedWidth(t *testing.T) {
	it := TaskItem{Number: "#12345", Title: "a title far longer than the column", Status: "running"}
	for _, line := range boardCard(it, 18, false) {
		if w := len([]rune(stripANSI(line))); w != 18 {
			t.Errorf("card line %q width = %d, want 18", stripANSI(line), w)
		}
	}
}

// TestRenderFailedLane_EmptyIsAVisibleZero: a lane that vanishes when empty
// teaches an operator to stop looking at that part of the screen.
func TestRenderFailedLane_EmptyIsAVisibleZero(t *testing.T) {
	out := stripANSI(renderFailedLane(nil, 80))
	if !strings.Contains(out, "FAILED (0)") {
		t.Errorf("empty lane should still report zero, got %q", out)
	}
}

func TestRenderFailedLane_CapsAndCounts(t *testing.T) {
	var failed []TaskItem
	for i := range 5 {
		failed = append(failed, TaskItem{Number: "#" + string(rune('1'+i)), Title: "broken", Status: "failed"})
	}
	out := stripANSI(renderFailedLane(failed, 80))
	if !strings.Contains(out, "FAILED (5)") {
		t.Errorf("lane header should carry the true count, got:\n%s", out)
	}
	if !strings.Contains(out, "+2 more") {
		t.Errorf("lane should say how many it did not show, got:\n%s", out)
	}
}

// TestBoardColumnTitle_CapNeverHidesTheCount: the DONE column is capped, and the
// header has to keep reporting the real total or the cap becomes a lie.
func TestBoardColumnTitle_CapNeverHidesTheCount(t *testing.T) {
	out := stripANSI(boardColumnTitle("DONE", 40, 12, 24))
	if !strings.Contains(out, "(40)") {
		t.Errorf("column title should show the true total, got %q", out)
	}
	if !strings.Contains(out, "+28") {
		t.Errorf("column title should show how many are hidden, got %q", out)
	}
}

// TestRenderBoard_FramedAndComplete is the end-to-end check: the board draws
// inside the standard frame, shows every column header with its count, and
// carries the failed lane.
func TestRenderBoard_FramedAndComplete(t *testing.T) {
	a := newTestApp(120, 30)
	a.model.tasksTab.History = boardItems()
	a.model.tasksTab.View = TaskViewBoard

	out := a.renderBoard(a.model.tasksTab, 24)
	if out == "" {
		t.Fatal("board declined to render at 120 columns")
	}
	assertFramed(t, out, 120)

	plain := stripANSI(out)
	for _, want := range []string{"QUEUED (1)", "RUNNING (1)", "BLOCKED (1)", "DONE (1)", "FAILED (1)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("board is missing %q:\n%s", want, plain)
		}
	}
}

// TestRenderBoard_RefusesBelowTheFloor: a grid crushed into a narrow terminal is
// noise, so the board returns "" and the caller shows the list instead.
func TestRenderBoard_RefusesBelowTheFloor(t *testing.T) {
	a := newTestApp(30, 20)
	a.model.tasksTab.History = boardItems()
	a.model.tasksTab.View = TaskViewBoard

	if out := a.renderBoard(a.model.tasksTab, 16); out != "" {
		t.Errorf("board should decline to render at 30 columns, got:\n%s", out)
	}
	// And the tab as a whole still renders, by falling back to the list.
	if got := a.renderTasksTab(16); got == "" {
		t.Error("Tasks tab rendered nothing when the board declined")
	}
}

// TestClampBoardSelection keeps the cursor on a card that exists: columns
// resize on every refresh as work moves between them.
func TestClampBoardSelection(t *testing.T) {
	cols, _ := boardGroups(boardItems())
	visible := visibleBoardColumns(4)

	t2 := &TasksTab{BoardCol: 99, BoardRow: 99}
	clampBoardSelection(t2, cols, visible, 5)
	if t2.BoardCol != len(visible)-1 {
		t.Errorf("BoardCol = %d, want it clamped to %d", t2.BoardCol, len(visible)-1)
	}
	if t2.BoardRow != 0 {
		t.Errorf("BoardRow = %d, want it clamped to the single card in that column", t2.BoardRow)
	}

	t3 := &TasksTab{BoardCol: -5, BoardRow: -5}
	clampBoardSelection(t3, cols, visible, 5)
	if t3.BoardCol != 0 || t3.BoardRow != 0 {
		t.Errorf("negative selection = (%d,%d), want (0,0)", t3.BoardCol, t3.BoardRow)
	}
}
