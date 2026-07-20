package dashboard

import (
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/orlandoburli/apiary/internal/logging"
)

// taskTranscriptMsg delivers a (re)read of the open transcript file. Produced
// by commands off the UI thread; applied only while the transcript view is
// still open on the same path, so a late reload never clobbers another view.
type taskTranscriptMsg struct {
	path    string
	content string
	err     string
}

// transcriptPathForFocus resolves which transcript file "t" should open. In
// the workflow monitor the selected step's own file is preferred (when it
// exists); everywhere else the task's most recent transcript is used.
func (a *App) transcriptPathForFocus() string {
	t := a.model.tasksTab
	if a.model.ActiveTab() == "Tasks" && t != nil && t.View == TaskViewWorkflow {
		if inst := t.WorkflowInstance; inst != nil && t.WorkflowStepIdx < len(inst.Steps) {
			step := inst.Steps[t.WorkflowStepIdx]
			name := logging.SanitizePathComponent(inst.ID+"-"+step.StepID) + ".md"
			p := filepath.Join(a.logDir, "transcripts", logging.SanitizePathComponent(inst.CellID), name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
			return logging.LatestTranscript(a.logDir, inst.CellID)
		}
	}
	if id, ok := a.focusedTaskID(); ok {
		return logging.LatestTranscript(a.logDir, id)
	}
	return ""
}

// openTranscriptView opens the in-app transcript viewer for the focused task.
// Returns handled=false when there is nothing to show (no focused task or no
// transcript file yet) so the caller can let the key fall through.
func (a *App) openTranscriptView() (bool, tea.Cmd) {
	path := a.transcriptPathForFocus()
	if path == "" {
		// A task is focused but has no transcript yet: consume the key (showing
		// nothing) only when focus is on a task list; otherwise fall through.
		_, focused := a.focusedTaskID()
		return focused, nil
	}

	// On the Agents tab, reuse the existing markdown file viewer (glamour +
	// raw toggle + scrolling) instead of the Tasks sub-view, which only
	// renders while the Tasks tab is active.
	if a.model.ActiveTab() == "Agents" {
		ag := a.model.agentsTab
		if ag == nil {
			return false, nil
		}
		ag.FileName = "transcript — " + filepath.Base(path)
		ag.FilePath = path
		ag.FileErr = ""
		ag.FileScroll = 0
		ag.FileRaw = false
		ag.FileReturn = ag.View
		ag.invalidateFileLines()
		if data, err := os.ReadFile(path); err != nil {
			ag.FileErr = err.Error()
		} else {
			ag.FileContent = string(data)
		}
		ag.View = AgentViewFileContent
		return true, nil
	}

	t := a.model.tasksTab
	if t == nil || a.model.ActiveTab() != "Tasks" {
		return false, nil
	}
	t.TranscriptPath = path
	t.TranscriptContent = ""
	t.TranscriptErr = ""
	t.TranscriptScroll = 0
	t.TranscriptRaw = false
	t.TranscriptFollow = true
	t.TranscriptReturn = t.View
	t.invalidateTranscriptLines()
	t.View = TaskViewTranscript
	return true, a.loadTranscriptCmd(path)
}

// loadTranscriptCmd reads the transcript file off the UI thread.
func (a *App) loadTranscriptCmd(path string) tea.Cmd {
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		if err != nil {
			return taskTranscriptMsg{path: path, err: err.Error()}
		}
		return taskTranscriptMsg{path: path, content: string(data)}
	}
}

// transcriptWarmedMsg delivers glamour-rendered display lines computed off
// the UI thread. glamour is too slow for the render path — rendering a large
// transcript synchronously froze the UI for exactly the time the loading
// spinner should have been animating.
type transcriptWarmedMsg struct {
	path  string
	width int
	raw   bool
	lines []string
}

// applyTranscriptMsg folds a (re)read into the open transcript view and
// kicks off the off-thread render. The previously warmed lines stay on
// screen until the new render lands, so live reloads never flicker back to
// the spinner. Follow mode is tail-anchored at render time (pinToTail).
func (a *App) applyTranscriptMsg(msg taskTranscriptMsg) tea.Cmd {
	t := a.model.tasksTab
	if t == nil || t.View != TaskViewTranscript || t.TranscriptPath != msg.path {
		return nil
	}
	if msg.err != "" {
		t.TranscriptErr = msg.err
		return nil
	}
	if msg.content == t.TranscriptContent {
		return nil
	}
	t.TranscriptErr = ""
	t.TranscriptContent = msg.content
	return a.warmTranscriptCmd()
}

// warmTranscriptCmd renders the current transcript content into display
// lines in a background goroutine, for the current width and raw mode.
func (a *App) warmTranscriptCmd() tea.Cmd {
	t := a.model.tasksTab
	if t == nil || t.TranscriptPath == "" {
		return nil
	}
	path, content, raw := t.TranscriptPath, t.TranscriptContent, t.TranscriptRaw
	inner := a.transcriptInnerWidth()
	return func() tea.Msg {
		return transcriptWarmedMsg{
			path:  path,
			width: inner,
			raw:   raw,
			lines: computeTranscriptLines(content, inner, raw),
		}
	}
}

// applyTranscriptWarmed merges an off-thread render into the memo. A warm
// from before a resize, a raw toggle, or a navigation away no longer
// matches and is dropped.
func (a *App) applyTranscriptWarmed(msg transcriptWarmedMsg) {
	t := a.model.tasksTab
	if t == nil || t.View != TaskViewTranscript || t.TranscriptPath != msg.path {
		return
	}
	if msg.width != a.transcriptInnerWidth() || msg.raw != t.TranscriptRaw {
		return
	}
	t.transcriptLines = msg.lines
	t.transcriptLinesWidth = msg.width
	t.transcriptLinesRaw = msg.raw
	t.transcriptLinesValid = true
}

// transcriptInnerWidth is the content width transcript lines are wrapped to.
func (a *App) transcriptInnerWidth() int {
	inner := a.model.width - 2
	if inner < 1 {
		inner = 1
	}
	return inner
}

// handleTranscriptKey is the key map while the transcript view is open.
func (a *App) handleTranscriptKey(key string) (tea.Model, tea.Cmd) {
	t := a.model.tasksTab
	lines := a.taskTranscriptLines()
	switch key {
	case "esc", "backspace", "h", "left":
		t.View = t.TranscriptReturn
		t.TranscriptContent = ""
		t.TranscriptPath = ""
		t.invalidateTranscriptLines()
	case "t":
		t.TranscriptRaw = !t.TranscriptRaw
		t.TranscriptScroll = 0
		t.TranscriptFollow = false
		t.invalidateTranscriptLines()
		return a, a.warmTranscriptCmd()
	case "r":
		return a, a.loadTranscriptCmd(t.TranscriptPath)
	case "up", "k":
		t.TranscriptFollow = false
		if t.TranscriptScroll > 0 {
			t.TranscriptScroll--
		}
	case "down", "j":
		if t.TranscriptScroll < lastIndex(len(lines)) {
			t.TranscriptScroll++
		}
	case "pgup":
		t.TranscriptFollow = false
		t.TranscriptScroll = clampScroll(t.TranscriptScroll-a.pageSize(), len(lines))
	case "pgdown":
		t.TranscriptScroll = clampScroll(t.TranscriptScroll+a.pageSize(), len(lines))
	case "home":
		t.TranscriptFollow = false
		t.TranscriptScroll = 0
	case "end", "G":
		t.TranscriptFollow = true // render pins to the tail
	}
	return a, nil
}

// sanitizeForTUI makes arbitrary file content safe for box rendering: tabs
// are expanded (lipgloss counts a tab as one cell but terminals expand it to
// the next tab stop, overflowing the box border — agent transcripts are full
// of tab-indented Go code), carriage returns are dropped, and remaining C0
// control characters (except newline) are stripped so embedded escapes can
// never corrupt the terminal.
func sanitizeForTUI(s string) string {
	s = strings.ReplaceAll(s, "\t", "    ")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r >= 0x20 {
			return r
		}
		return -1
	}, s)
}

// invalidateTranscriptLines drops the memoized display lines so the next
// taskTranscriptLines call re-renders (after a toggle, resize, or reload).
func (t *TasksTab) invalidateTranscriptLines() {
	t.transcriptLines = nil
	t.transcriptLinesValid = false
}

// taskTranscriptLines returns the warmed display lines, or nil while the
// off-thread render for the current width/raw mode is still in flight (the
// view shows the loading spinner). It never renders synchronously — glamour
// on a large transcript would freeze the UI.
func (a *App) taskTranscriptLines() []string {
	t := a.model.tasksTab
	if t == nil {
		return nil
	}
	if t.transcriptLinesValid && t.transcriptLinesWidth == a.transcriptInnerWidth() && t.transcriptLinesRaw == t.TranscriptRaw {
		return t.transcriptLines
	}
	return nil
}

// computeTranscriptLines renders transcript content into display lines
// wrapped to inner width: glamour-rendered markdown unless raw is set. Pure
// function, safe to call off the UI thread.
func computeTranscriptLines(content string, inner int, raw bool) []string {
	content = sanitizeForTUI(content)
	if !raw {
		if rendered, err := renderMarkdown(content, inner); err == nil {
			return clampToWidth(strings.Split(strings.TrimRight(rendered, "\n"), "\n"), inner)
		}
	}
	return wrapPlain(content, inner)
}

func (a *App) renderTaskTranscript(t *TasksTab, height int) string {
	title := "TRANSCRIPT — " + filepath.Base(t.TranscriptPath)
	if t.TranscriptRaw {
		title += " (raw)"
	}
	if t.TranscriptFollow {
		title += " · following"
	}
	if t.TranscriptErr != "" {
		body := StyleError.Render("Could not read "+t.TranscriptPath+":") + "\n" + StyleMuted.Render(t.TranscriptErr) + "\n"
		return a.box(title, body, height)
	}
	lines := a.taskTranscriptLines()
	if lines == nil || strings.TrimSpace(t.TranscriptContent) == "" {
		frame := spinnerFrames[a.model.spinnerFrame%len(spinnerFrames)]
		return a.box(title, StyleMuted.Render(frame+" loading transcript…")+"\n", height)
	}
	rows := height - 2
	if rows < 1 {
		rows = 1
	}
	// Follow mode anchors the viewport to the tail: show the last full page,
	// not a window starting at the last line.
	start := clampScroll(t.TranscriptScroll, len(lines))
	if t.TranscriptFollow {
		start = pinToTail(len(lines), rows)
		t.TranscriptScroll = start
	}
	end := start + rows
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}
	return a.box(title, b.String(), height)
}
