package dashboard

import (
	"context"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/state"
)

// The board groups tasks by canonical state, one column per state. The grouping
// is a projection of a single column rather than a derivation, which is what the
// unified state model bought (#465): a card is in BLOCKED because its state says
// blocked, not because the renderer worked it out.
//
// FAILED is deliberately not a column. A board's columns are stages work passes
// through, and terminal failure is not a stage — it is an exception waiting for
// a human. It renders as a full-width lane below the board instead, and stays
// visible as a zero when nothing has failed, because "nothing failed" is
// information an operator wants.
var boardColumns = []struct {
	Title string
	State state.State
}{
	{"QUEUED", state.Queued},
	{"RUNNING", state.Running},
	{"BLOCKED", state.Blocked},
	{"DONE", state.Done},
}

const (
	// boardMinColWidth is the narrowest a column can be and still show a card.
	boardMinColWidth = 14
	// boardSepWidth is the gutter between columns. A single space is not enough:
	// titles are truncated to the full column width, so adjacent cards run into
	// each other and the eye cannot find the column boundary.
	boardSepWidth = 3
	// boardMinWidth is where the board stops being legible at all and the list
	// is shown instead. Refusing to render is more honest than an unreadable grid.
	boardMinWidth = 40
	// boardDoneCap bounds the DONE column. Without it a long-running hive's
	// completed work squeezes out the columns that need attention. The header
	// count is always the true total, so the cap hides cards, never the number.
	boardDoneCap = 12
)

// boardGroups buckets tasks into their board columns, plus the failed lane.
//
// Canceled tasks are omitted: they are terminal and operator-initiated, so the
// operator already knows. They remain reachable through the list and its filter.
func boardGroups(items []TaskItem) (map[state.State][]TaskItem, []TaskItem) {
	cols := map[state.State][]TaskItem{}
	var failed []TaskItem
	for _, it := range items {
		switch st := state.Normalize(it.Status); st {
		case state.Failed:
			failed = append(failed, it)
		case state.Canceled:
			// omitted, see above
		case state.Queued, state.Running, state.Blocked, state.Done:
			cols[st] = append(cols[st], it)
		default:
			// An unrecognised state is shown rather than dropped; silently
			// losing a task from the board would be worse than an odd column.
			cols[state.Queued] = append(cols[state.Queued], it)
		}
	}
	return cols, failed
}

// boardLayout decides how many columns fit and how wide they are.
//
// Columns are dropped from the right — DONE first, then QUEUED — because those
// are the two an operator is least likely to be acting on. The dropped columns'
// counts move into the header, so narrowing the terminal hides cards but never
// hides that the work exists.
func boardLayout(inner int) (cols int, width int) {
	if inner < boardMinWidth {
		return 0, 0
	}
	for n := len(boardColumns); n >= 1; n-- {
		w := (inner - (n-1)*boardSepWidth) / n
		if w >= boardMinColWidth {
			return n, w
		}
	}
	return 1, inner
}

// visibleBoardColumns returns the columns that fit, in display order. When
// columns are dropped, DONE goes first and QUEUED second.
func visibleBoardColumns(n int) []int {
	dropOrder := []int{3, 0} // DONE, then QUEUED
	keep := map[int]bool{0: true, 1: true, 2: true, 3: true}
	for i := 0; i < len(boardColumns)-n && i < len(dropOrder); i++ {
		keep[dropOrder[i]] = false
	}
	var out []int
	for i := range boardColumns {
		if keep[i] {
			out = append(out, i)
		}
	}
	return out
}

// boardCard renders one task as the lines of a card: number, title, progress,
// and age. The agent is deliberately absent — it is a property of a step, and a
// card is a task.
func boardCard(it TaskItem, width int, selected bool) []string {
	num := valueOr(it.Number, "—")
	title := valueOr(it.Title, it.TaskID)
	progress := progressLabel(taskProgressOf(it), width)
	age := taskWhen(it)

	// A blocked card says what it is blocked on; that is the whole reason an
	// operator is looking at this column.
	if state.Normalize(it.Status) == state.Blocked && it.BlockedReason != "" {
		age = it.BlockedReason + " " + age
	}

	lines := []string{
		truncate(num, width),
		truncate(title, width),
		truncate(progress, width),
		truncate(age, width),
	}
	for i, l := range lines {
		l = pad(l, width)
		switch {
		case selected && i == 0:
			l = StyleFocusedArrow.Render(l)
		case selected:
			l = StyleSelectedRow.Render(l)
		case i == 0:
			l = StyleAccent.Render(l)
		case i >= 2:
			l = StyleMuted.Render(l)
		}
		lines[i] = l
	}
	return lines
}

// boardColumnTitle is the column header: name, count, and — for a column that
// was capped — how many cards are not shown.
func boardColumnTitle(title string, total, shown, width int) string {
	label := title + " (" + strconv.Itoa(total) + ")"
	if shown < total {
		label += " +" + strconv.Itoa(total-shown)
	}
	return StyleValueStrong.Render(pad(truncate(label, width), width))
}

// renderFailedLane renders the attention lane below the board.
//
// It is always present. An empty lane collapses to one line that says zero,
// because a board where the failure lane simply vanishes teaches an operator to
// stop looking at that part of the screen.
func renderFailedLane(failed []TaskItem, inner int) string {
	if len(failed) == 0 {
		return StyleMuted.Render("FAILED (0) — nothing needs attention")
	}

	var b strings.Builder
	b.WriteString(StyleError.Render("FAILED ("+strconv.Itoa(len(failed))+")") +
		StyleMuted.Render(" — needs attention"))
	b.WriteString("\n")

	for i, it := range failed {
		if i >= 3 {
			b.WriteString(StyleMuted.Render("  +" + strconv.Itoa(len(failed)-3) + " more"))
			b.WriteString("\n")
			break
		}
		num := pad(truncate(valueOr(it.Number, "—"), 8), 8)
		progress := pad(truncate(progressLabel(taskProgressOf(it), 14), 14), 14)
		titleW := maxInt(10, inner-8-14-12-6)
		title := pad(truncate(valueOr(it.Title, it.TaskID), titleW), titleW)
		b.WriteString("  " + StyleAccent.Render(num) + " " + title + " " +
			StyleMuted.Render(progress) + " " + StyleMuted.Render(taskWhen(it)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderBoard draws the board: the columns that fit, then the failed lane.
//
// Below boardMinWidth it returns "" and the caller falls back to the list. A
// grid squeezed into 30 columns is not a degraded board, it is noise.
func (a *App) renderBoard(t *TasksTab, height int) string {
	items := a.filteredTasks(t)
	cols, failed := boardGroups(items)

	width := a.model.width
	if width < 24 {
		width = 24
	}
	inner := width - 2

	nCols, colW := boardLayout(inner)
	if nCols == 0 {
		return ""
	}
	visible := visibleBoardColumns(nCols)

	// Cards are four lines plus a blank separator; reserve the failed lane and
	// the header row.
	laneHeight := 1
	if len(failed) > 0 {
		laneHeight = minInt(len(failed), 3) + 1
	}
	bodyRows := height - 4 - laneHeight
	if bodyRows < 5 {
		bodyRows = 5
	}
	perColumn := maxInt(1, bodyRows/5)

	clampBoardSelection(t, cols, visible, perColumn)

	var header, body strings.Builder
	shownPerCol := make([][]TaskItem, len(visible))
	for i, ci := range visible {
		c := boardColumns[ci]
		list := cols[c.State]
		if c.State == state.Done && len(list) > boardDoneCap {
			list = list[:boardDoneCap]
		}
		if len(list) > perColumn {
			list = list[:perColumn]
		}
		shownPerCol[i] = list
		if i > 0 {
			header.WriteString(boardSeparator(false))
		}
		header.WriteString(boardColumnTitle(c.Title, len(cols[c.State]), len(list), colW))
	}

	// Columns that did not fit still report their totals.
	var dropped []string
	for ci := range boardColumns {
		shown := false
		for _, v := range visible {
			if v == ci {
				shown = true
			}
		}
		if !shown {
			if n := len(cols[boardColumns[ci].State]); n > 0 {
				dropped = append(dropped, "+"+strconv.Itoa(n)+" "+strings.ToLower(boardColumns[ci].Title))
			}
		}
	}
	if len(dropped) > 0 {
		header.WriteString("  " + StyleMuted.Render(strings.Join(dropped, "  ")))
	}

	for row := 0; row < perColumn; row++ {
		cardLines := make([][]string, len(visible))
		any := false
		for i := range visible {
			if row < len(shownPerCol[i]) {
				selected := t.BoardCol == i && t.BoardRow == row
				cardLines[i] = boardCard(shownPerCol[i][row], colW, selected)
				any = true
			}
		}
		if !any {
			break
		}
		for line := 0; line < 4; line++ {
			for i := range visible {
				if i > 0 {
					body.WriteString(boardSeparator(true))
				}
				if cardLines[i] != nil {
					body.WriteString(cardLines[i][line])
				} else {
					body.WriteString(pad("", colW))
				}
			}
			body.WriteString("\n")
		}
		body.WriteString("\n")
	}

	content := header.String() + "\n\n" + body.String() + renderFailedLane(failed, inner) + "\n"
	return a.box("TASKS — BOARD  (b: list  enter: detail  l: logs  a/y/n: approve  R: restart  X: stop)", content, height)
}

// clampBoardSelection keeps the cursor on a card that exists. Columns change
// size on every refresh as work moves, so a selection that was valid a tick ago
// may not be now.
func clampBoardSelection(t *TasksTab, cols map[state.State][]TaskItem, visible []int, perColumn int) {
	if len(visible) == 0 {
		return
	}
	if t.BoardCol < 0 {
		t.BoardCol = 0
	}
	if t.BoardCol >= len(visible) {
		t.BoardCol = len(visible) - 1
	}
	n := len(cols[boardColumns[visible[t.BoardCol]].State])
	if n > perColumn {
		n = perColumn
	}
	if t.BoardRow < 0 {
		t.BoardRow = 0
	}
	if t.BoardRow >= n {
		t.BoardRow = maxInt(0, n-1)
	}
}

// selectedBoardTask returns the task under the board cursor, or nil.
//
// The board's own actions (logs, approve, restart, stop) all route through the
// same handlers the list uses, so this is the one place the board's coordinate
// pair is translated back into a task.
func (a *App) selectedBoardTask(t *TasksTab) *TaskItem {
	cols, _ := boardGroups(a.filteredTasks(t))
	nCols, _ := boardLayout(maxInt(24, a.model.width) - 2)
	if nCols == 0 {
		return nil
	}
	visible := visibleBoardColumns(nCols)
	if t.BoardCol < 0 || t.BoardCol >= len(visible) {
		return nil
	}
	list := cols[boardColumns[visible[t.BoardCol]].State]
	if t.BoardRow < 0 || t.BoardRow >= len(list) {
		return nil
	}
	return &list[t.BoardRow]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// boardSeparator is the gutter drawn between columns: a dim rule beside card
// rows, blank under the headers so the titles stay uncluttered.
func boardSeparator(rule bool) string {
	if rule {
		return " " + StyleMuted.Render("│") + " "
	}
	return strings.Repeat(" ", boardSepWidth)
}

// boardColumnCount is how many columns the board is currently showing, which
// depends on the terminal width. Key handling needs it to know when the cursor
// is at the last column and the key should fall through to the tab switch.
func (a *App) boardColumnCount() int {
	n, _ := boardLayout(maxInt(24, a.model.width) - 2)
	if n == 0 {
		return 0
	}
	return len(visibleBoardColumns(n))
}

// boardApprovalMsg carries an approval request loaded for a board card, plus
// the key that asked for it, back into Update.
type boardApprovalMsg struct {
	req *db.ApprovalRequest
	key string
	err string
}

// boardApprovalCmd loads the approval request a card is parked on.
//
// Answering from the board is the interaction that makes it worth having: an
// operator opens it, sees BLOCKED (4), and clears four gates without drilling
// into four tasks. The request has to be fetched first because a card carries
// only the task, so this runs as a command and hands the result to
// answerApproval — the same function the detail view uses, so declared fields,
// quorum, and the form all behave identically.
func (a *App) boardApprovalCmd(it TaskItem, key string) tea.Cmd {
	dbConn := a.dbConn
	taskID := it.InternalTaskID
	return func() tea.Msg {
		if dbConn == nil || taskID == "" {
			return boardApprovalMsg{key: key, err: "no database"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()

		instances, err := dbConn.ListWorkflowInstancesByTask(ctx, taskID)
		if err != nil {
			return boardApprovalMsg{key: key, err: err.Error()}
		}
		for _, in := range instances {
			if !blockedOnApproval(in.State, in.BlockedReason) {
				continue
			}
			if req, err := dbConn.GetApprovalByInstance(ctx, in.ID); err == nil && req != nil {
				return boardApprovalMsg{req: req, key: key}
			}
		}
		return boardApprovalMsg{key: key, err: "no approval is waiting on this task"}
	}
}
