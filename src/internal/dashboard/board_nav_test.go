package dashboard

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newBoardNavApp returns an app sitting on the Tasks tab with the board open
// and a fixture that puts one card in every column.
func newBoardNavApp() *App {
	a := newTestApp(120, 30) // wide enough for all four columns
	a.model.activeTab = 1    // Tasks
	a.model.tasksTab.History = boardItems()
	a.model.tasksTab.View = TaskViewBoard
	return a
}

func arrowKey(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// right moves to the next column and puts the cursor back at the top of it.
func TestBoardNav_RightMovesColumnAndResetsRow(t *testing.T) {
	a := newBoardNavApp()
	tb := a.model.tasksTab
	tb.BoardRow = 2

	a.Update(arrowKey(tea.KeyRight))

	if tb.BoardCol != 1 {
		t.Errorf("BoardCol = %d after →, want 1", tb.BoardCol)
	}
	if tb.BoardRow != 0 {
		t.Errorf("BoardRow = %d after →, want it reset to 0", tb.BoardRow)
	}
	if tb.View != TaskViewBoard {
		t.Errorf("→ left the board: View = %v", tb.View)
	}
}

// left moves to the previous column, and at the first column it leaves the
// board for the list rather than going negative (or switching tabs).
func TestBoardNav_LeftMovesColumnAndLeavesAtFirstColumn(t *testing.T) {
	a := newBoardNavApp()
	tb := a.model.tasksTab
	tb.BoardCol = 2

	a.Update(arrowKey(tea.KeyLeft))
	if tb.BoardCol != 1 || tb.View != TaskViewBoard {
		t.Fatalf("← from column 2: col=%d view=%v, want 1/board", tb.BoardCol, tb.View)
	}

	a.Update(arrowKey(tea.KeyLeft))
	if tb.BoardCol != 0 || tb.View != TaskViewBoard {
		t.Fatalf("← from column 1: col=%d view=%v, want 0/board", tb.BoardCol, tb.View)
	}

	a.Update(arrowKey(tea.KeyLeft))
	if tb.BoardCol != 0 {
		t.Errorf("← at the first column made BoardCol %d, want it to stay 0", tb.BoardCol)
	}
	if tb.View != TaskViewList {
		t.Errorf("← at the first column: View = %v, want the list (never trap the cursor)", tb.View)
	}
}

// up/down move the row within the column, and down past the last card is left
// for render to clamp.
func TestBoardNav_UpDownMovesRow(t *testing.T) {
	a := newBoardNavApp()
	tb := a.model.tasksTab

	a.Update(arrowKey(tea.KeyDown))
	a.Update(arrowKey(tea.KeyDown))
	if tb.BoardRow != 2 {
		t.Fatalf("BoardRow = %d after two ↓, want 2", tb.BoardRow)
	}
	if tb.View != TaskViewBoard {
		t.Fatalf("↓ left the board: View = %v", tb.View)
	}

	a.Update(arrowKey(tea.KeyUp))
	if tb.BoardRow != 1 {
		t.Errorf("BoardRow = %d after ↑, want 1", tb.BoardRow)
	}

	tb.BoardRow = 0
	a.Update(arrowKey(tea.KeyUp))
	if tb.BoardRow != 0 {
		t.Errorf("BoardRow = %d after ↑ at the top, want it to stay 0", tb.BoardRow)
	}
}

// The board's row cursor is not the list cursor: moving on the board must not
// silently move the selection the user sees when they go back to the list.
func TestBoardNav_RowMoveDoesNotMoveListCursor(t *testing.T) {
	a := newBoardNavApp()
	tb := a.model.tasksTab
	tb.SelectedIdx = 2

	a.Update(arrowKey(tea.KeyDown))
	a.Update(arrowKey(tea.KeyDown))
	a.Update(arrowKey(tea.KeyUp))

	if tb.BoardRow != 1 {
		t.Fatalf("BoardRow = %d, want 1", tb.BoardRow)
	}
	if tb.SelectedIdx != 2 {
		t.Errorf("SelectedIdx = %d after board row moves, want it untouched at 2", tb.SelectedIdx)
	}
}

// At the last column → has nowhere to move, so it keeps its global meaning and
// switches tabs — the board never traps the cursor.
func TestBoardNav_RightAtLastColumnSwitchesTab(t *testing.T) {
	a := newBoardNavApp()
	tb := a.model.tasksTab
	tb.BoardCol = a.boardColumnCount() - 1

	a.Update(arrowKey(tea.KeyRight))

	if got := a.model.ActiveTab(); got == "Tasks" {
		t.Errorf("→ at the last column stayed on Tasks, want the tab to switch")
	}
}

// The board's approval keys must still reach boardApprovalCmd, which is the
// whole reason approving from the board works.
func TestBoardNav_ApprovalKeysStillReachBoardApproval(t *testing.T) {
	for _, key := range []string{"a", "y", "n"} {
		a := newBoardNavApp()
		tb := a.model.tasksTab
		tb.BoardCol = 2 // BLOCKED
		tb.BoardRow = 0

		_, cmd := a.Update(keyPress(key))
		if cmd == nil {
			t.Errorf("%q on the board produced no command; boardApprovalCmd was not reached", key)
		}
		if tb.View != TaskViewBoard {
			t.Errorf("%q on the board left the view: %v", key, tb.View)
		}
	}
}

// b toggles the board back to the list.
func TestBoardNav_BReturnsToList(t *testing.T) {
	a := newBoardNavApp()
	tb := a.model.tasksTab

	a.Update(keyPress("b"))

	if tb.View != TaskViewList {
		t.Errorf("b on the board: View = %v, want the list", tb.View)
	}
}

// Regression guards: the other Tasks sub-views still own their keys.
func TestSubViewKeys_Unchanged(t *testing.T) {
	t.Run("logs up scrolls the log", func(t *testing.T) {
		a := newTestApp(120, 30)
		a.model.activeTab = 1
		tb := a.model.tasksTab
		tb.View = TaskViewLogs
		tb.LogScroll = 2

		a.Update(arrowKey(tea.KeyUp))

		if tb.LogScroll != 1 {
			t.Errorf("LogScroll = %d after ↑ in the logs view, want 1", tb.LogScroll)
		}
	})

	t.Run("workflow monitor up moves the step selection", func(t *testing.T) {
		a := newTestApp(120, 30)
		a.model.activeTab = 1
		tb := a.model.tasksTab
		tb.View = TaskViewWorkflow
		tb.WorkflowInstance = &WorkflowInstanceItem{}
		tb.WorkflowStepIdx = 2

		a.Update(arrowKey(tea.KeyUp))

		if tb.WorkflowStepIdx != 1 {
			t.Errorf("WorkflowStepIdx = %d after ↑ in the monitor, want 1", tb.WorkflowStepIdx)
		}
	})

	t.Run("detail up scrolls the detail", func(t *testing.T) {
		a := newTestApp(120, 30)
		a.model.activeTab = 1
		tb := a.model.tasksTab
		tb.View = TaskViewDetail
		tb.DetailScroll = 3

		a.Update(arrowKey(tea.KeyUp))

		if tb.DetailScroll != 2 {
			t.Errorf("DetailScroll = %d after ↑ in the detail view, want 2", tb.DetailScroll)
		}
	})
}
