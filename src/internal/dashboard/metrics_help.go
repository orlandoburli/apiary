package dashboard

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The metrics help overlay (`H`). The Overview and Workflows tabs print wall,
// active and percentile figures, and none of those words explains itself: wall
// includes waiting, active excludes it, and a percentile is not an average. The
// copy below is the explanation, kept next to the code that prints the figures
// so a rename on one side is a rename on both.
//
// It is a reading overlay, not a form: it owns the keyboard while open, and on
// a terminal too short for the whole explanation it scrolls rather than slicing
// off the last paragraphs. The header and the close hint stay put; only the
// definitions between them move.

// metricsHelpLabelWidth is the column the copy starts its definitions at, and
// the column continuation lines line up under. It is also what the footer's
// close hint uses, so "close" sits where the definitions do.
const metricsHelpLabelWidth = 14

type metricsHelpBlock struct {
	label string
	text  string // \n-separated lines, worded exactly as the copy prints them
}

// metricsHelpBlocks is the explanation, in reading order. The text reflows when
// the terminal is narrower than the widest line, so no paragraph is clipped.
var metricsHelpBlocks = []metricsHelpBlock{
	{
		label: "Window (w)",
		text: "last 7 days · 30 days · 90 days · all time. Every figure covers\n" +
			"runs that finished inside it.",
	},
	{
		label: "Wall",
		text: "The run's whole life, start to finish. Includes agent work, CI\n" +
			"polling, human approvals, blockers and retry backoff. Answers\n" +
			"\"when will this be done?\"",
	},
	{
		label: "Active",
		text: "The union of the run's step intervals: time something was\n" +
			"actually executing. Waits are excluded and overlapping steps\n" +
			"count once. Answers \"how much work is this?\"",
	},
	{
		label: "Agents",
		text:  "One agent process, its own duration. No waiting.",
	},
	{
		label: "p50 / p90",
		text: "Nearest-rank percentiles. p50: half the runs took at most this\n" +
			"long. p90: 90% did. A p90 is a duration a run actually took.",
	},
	{
		label: "max",
		text:  "The slowest run in the window.",
	},
	{
		label: "N",
		text: "The sample size (e.g. 114 runs). A percentile without N is not\n" +
			"interpretable.",
	},
}

// metricsHelpTab reports whether the tab prints the duration figures. `H` is
// scoped to them: on the other tabs there is nothing here to explain.
func metricsHelpTab(tab string) bool {
	return tab == "Overview" || tab == "Workflows"
}

// openMetricsHelp raises the overlay at its first line. Starting at the top
// matters: the window definition is the one every other figure depends on.
func (a *App) openMetricsHelp() {
	a.model.metricsHelpActive = true
	a.model.metricsHelpScroll = 0
}

func (a *App) closeMetricsHelp() {
	a.model.metricsHelpActive = false
	a.model.metricsHelpScroll = 0
}

// handleMetricsHelpKey consumes every key while the overlay is open: it is a
// modal reading surface, so the tab underneath must not scroll, switch or
// change window. `H` toggles and `esc` closes; the rest scroll.
func (a *App) handleMetricsHelpKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "H", "esc":
		a.closeMetricsHelp()
		return a, nil
	}

	body, visible, _ := a.metricsHelpLayout()
	maxScroll := len(body) - visible
	if maxScroll < 0 {
		maxScroll = 0
	}
	step := visible - 1
	if step < 1 {
		step = 1
	}

	switch key {
	case "up", "k":
		a.model.metricsHelpScroll--
	case "down", "j":
		a.model.metricsHelpScroll++
	case "pgup", "ctrl+u":
		a.model.metricsHelpScroll -= step
	case "pgdown", "ctrl+d", " ":
		a.model.metricsHelpScroll += step
	case "home", "g":
		a.model.metricsHelpScroll = 0
	case "end", "G":
		a.model.metricsHelpScroll = maxScroll
	}

	if a.model.metricsHelpScroll < 0 {
		a.model.metricsHelpScroll = 0
	}
	if a.model.metricsHelpScroll > maxScroll {
		a.model.metricsHelpScroll = maxScroll
	}
	return a, nil
}

// helpFrame is which fixed rows surround the scrolling body at the current
// terminal height. A short terminal gives up the title and the close hint before
// it gives up a line of the explanation, and a very short one gives up the
// padding too, so the box always fits on screen.
type helpFrame struct {
	padV   int
	title  bool
	footer bool
	blanks bool // blank rows between the title, body and close hint
}

func (a *App) metricsHelpFrame() helpFrame {
	h := a.model.height
	f := helpFrame{padV: 1}
	if h < 8 {
		f.padV = 0
	}
	f.title = h >= 7
	f.footer = h >= 7
	f.blanks = h >= 14
	return f
}

// rows is the number of terminal lines the frame takes, borders included, so the
// body can be given whatever is left.
func (f helpFrame) rows() int {
	n := 2 + 2*f.padV
	if f.title {
		n++
	}
	if f.footer {
		n++
	}
	if f.blanks {
		n += 2
	}
	return n
}

// metricsHelpDialogWidth is as wide as the copy needs, capped so the dialog
// (borders included) stays inside the terminal. Narrowing below the natural
// width makes the copy wrap instead of widening the box.
func (a *App) metricsHelpDialogWidth() int {
	natural := 0
	for _, b := range metricsHelpBlocks {
		for _, line := range strings.Split(b.text, "\n") {
			if w := metricsHelpLabelWidth + lipgloss.Width(line); w > natural {
				natural = w
			}
		}
	}
	// 3 columns of padding each side.
	width := natural + 6
	// Two border columns have to fit too.
	maxWidth := a.model.width - 2
	if maxWidth < 12 {
		maxWidth = 12
	}
	if width > maxWidth {
		width = maxWidth
	}
	if width < 12 {
		width = 12
	}
	return width
}

// metricsHelpLayout returns the body lines, how many fit at once, and the text
// column width. Rows are body lines, not rendered rows: every line is wrapped
// to fit the column, so one body line is always one terminal line.
func (a *App) metricsHelpLayout() (body []string, visible, inner int) {
	inner = a.metricsHelpDialogWidth() - 6
	if inner < 1 {
		inner = 1
	}
	body = a.metricsHelpBodyLines(inner)
	visible = a.model.height - a.metricsHelpFrame().rows()
	if visible < 1 {
		visible = 1
	}
	return body, visible, inner
}

// metricsHelpBodyLines renders the blocks into the dialog's text column. Labels
// stay in their own column while there is room for them; below that the label
// gets a line of its own and the text runs the full width, which is the only
// way to keep the copy readable on a very narrow terminal.
func (a *App) metricsHelpBodyLines(inner int) []string {
	stacked := inner < metricsHelpLabelWidth+16
	out := make([]string, 0, len(metricsHelpBlocks)*3)
	indent := strings.Repeat(" ", metricsHelpLabelWidth)

	for i, b := range metricsHelpBlocks {
		if i > 0 {
			out = append(out, "")
		}
		if stacked {
			out = append(out, StyleLabel.Render(b.label))
			for _, line := range wrapHelpText(b.text, inner) {
				out = append(out, line)
			}
			continue
		}
		// If a single copy line is too wide, the block reflows as one paragraph.
		// Wrapping each source line on its own would leave one-word orphans
		// wherever the copy happened to break a sentence.
		source := strings.Split(b.text, "\n")
		colWidth := inner - metricsHelpLabelWidth
		for _, line := range source {
			if lipgloss.Width(line) > colWidth {
				source = []string{strings.Join(strings.Fields(b.text), " ")}
				break
			}
		}
		for j, textLine := range source {
			prefix := indent
			if j == 0 {
				prefix = StyleLabel.Render(b.label) +
					strings.Repeat(" ", metricsHelpLabelWidth-lipgloss.Width(b.label))
			}
			for k, wrapped := range wrapHelpText(textLine, colWidth) {
				if k == 0 {
					out = append(out, prefix+wrapped)
				} else {
					out = append(out, indent+wrapped)
				}
			}
		}
	}
	return out
}

// wrapHelpText word-wraps one line at width columns. Existing line breaks in the
// copy are respected (the caller splits on them first); this only breaks lines
// that are too long for the terminal. A word wider than the whole column is left
// on its own line rather than mangled — it is visible, just clipped.
func wrapHelpText(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	if lipgloss.Width(s) <= width {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	out := make([]string, 0, len(words)/4+1)
	line := ""
	for _, word := range words {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// renderMetricsHelp overlays the explanation on the current view, sharing the
// sheets' rounded bordered shell.
func (a *App) renderMetricsHelp(view string) string {
	body, visible, inner := a.metricsHelpLayout()
	frame := a.metricsHelpFrame()

	maxScroll := len(body) - visible
	if maxScroll < 0 {
		maxScroll = 0
	}
	scroll := a.model.metricsHelpScroll
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	end := scroll + visible
	if end > len(body) {
		end = len(body)
	}

	rows := make([]string, 0, len(body)+4)
	if frame.title {
		rows = append(rows, StyleBoxTitle.Render("Metrics help"))
		if frame.blanks {
			rows = append(rows, "")
		}
	}
	rows = append(rows, body[scroll:end]...)
	if frame.footer {
		if frame.blanks {
			rows = append(rows, "")
		}
		rows = append(rows, metricsHelpCloseLine(inner, maxScroll > 0))
	}

	// Width includes the horizontal padding; the border adds two columns.
	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorWarning).
		Padding(frame.padV, 3).
		Width(inner + 6).
		Render(strings.Join(rows, "\n"))

	topPad := (a.model.height - lipgloss.Height(dialog)) / 2
	if topPad < 0 {
		topPad = 0
	}
	return strings.Repeat("\n", topPad) +
		lipgloss.NewStyle().Width(a.model.width).Align(lipgloss.Center).Render(dialog)
}

// metricsHelpCloseLine is the copy's `H or esc      close`, plus a scroll hint
// while there is more explanation than fits. The hint is additive: it says the
// definitions continue, it does not replace the way out. It is dropped rather
// than wrapped when the column cannot hold it, so the fixed footer stays one row
// and the scroll budget stays true.
func metricsHelpCloseLine(inner int, scrollable bool) string {
	line := StyleLabel.Render("H or esc") +
		strings.Repeat(" ", metricsHelpLabelWidth-lipgloss.Width("H or esc")) +
		StyleFooterDim.Render("close")
	if !scrollable {
		return line
	}
	hint := "   ↑↓ pgup/pgdn scroll"
	if lipgloss.Width(line)+lipgloss.Width(hint) > inner {
		return line
	}
	return line + StyleFooterDim.Render(hint)
}
