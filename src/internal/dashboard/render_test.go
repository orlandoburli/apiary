package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// keyPress builds a tea.KeyMsg whose String() matches the dashboard's handlers.
func keyPress(s string) tea.KeyMsg {
	switch s {
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func newTestApp(w, h int) *App {
	a := &App{model: NewModel()}
	a.model.width = w
	a.model.height = h
	return a
}

// stripANSI removes SGR escape sequences for plain-text assertions.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// assertFramed checks every body line of a box starts/ends with a vertical bar
// and that no line's visible width exceeds the terminal width.
func assertFramed(t *testing.T, out string, width int) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected a framed box, got %d lines", len(lines))
	}
	if top := stripANSI(lines[0]); !strings.HasPrefix(top, "┌") || !strings.HasSuffix(top, "┐") {
		t.Errorf("top border malformed: %q", top)
	}
	if last := stripANSI(lines[len(lines)-1]); !strings.HasPrefix(last, "└") || !strings.HasSuffix(last, "┘") {
		t.Errorf("bottom border malformed: %q", last)
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != width {
			t.Errorf("line %d width = %d, want %d: %q", i, w, width, stripANSI(ln))
		}
		if i > 0 && i < len(lines)-1 {
			plain := stripANSI(ln)
			if !strings.HasPrefix(plain, "│") || !strings.HasSuffix(plain, "│") {
				t.Errorf("body line %d not framed with side borders: %q", i, plain)
			}
		}
	}
}

func TestBoxFullBorders(t *testing.T) {
	a := newTestApp(80, 20)
	assertFramed(t, a.box("TASKS", "hello\nworld", 10), 80)
}

func TestTabsRenderFramedAndAligned(t *testing.T) {
	now := time.Now()
	a := newTestApp(100, 24)

	a.model.tasksTab.History = []TaskItem{
		{TaskID: "T-1", Title: "A very long task title that should be truncated to fit the column nicely", Agent: "engineer", Status: "success", CompletedAt: &now},
		{TaskID: "T-2", Title: "short", Agent: "investigator", Status: "running", StartedAt: &now},
	}
	assertFramed(t, a.renderTaskList(a.model.tasksTab, 12), 100)

	a.model.agentsTab.Agents = []AgentStatus{
		{ID: "engineer", Status: "active", PID: 12345, MaxWorkers: 3, CompletedCount: 12, AvgDurationMs: 64000, SuccessRate: 0.91},
		{ID: "investigator", Status: "idle", MaxWorkers: 2, CompletedCount: 3, AvgDurationMs: 1200, SuccessRate: 1.0},
	}
	assertFramed(t, a.renderAgentsTab(12), 100)
}

func TestLogMarkdownInlineRender(t *testing.T) {
	a := newTestApp(90, 24)
	md := "## Status\n\n- done\n- **verified**\n"
	plain := "dispatching to agent=investigator model=claude-opus-4-8 runner=cli"

	// Heuristic: multi-line markdown is detected; the operational one-liner isn't.
	if !looksLikeMarkdown(md) {
		t.Fatal("multi-line markdown should be detected")
	}
	if looksLikeMarkdown(plain) {
		t.Fatal("operational one-liner should not be treated as markdown")
	}

	// glamour never runs synchronously (#175): before the warm-up lands, markdown
	// paints plain-wrapped so the first frame is instant.
	msgWidth := a.logMsgWidth()
	if got := a.logMessageLines(md, msgWidth); strings.Join(got, "\n") != strings.Join(wrapPlain(md, msgWidth), "\n") {
		t.Error("markdown should paint plain-wrapped before the warm-up delivers")
	}

	// The warm-up renders the markdown off-thread and skips the one-liner.
	cmd := a.warmMarkdownCmd([]LogEntry{{Message: md}, {Message: plain}})
	if cmd == nil {
		t.Fatal("warm-up should have work for the markdown entry")
	}
	wm, ok := cmd().(mdWarmedMsg)
	if !ok {
		t.Fatalf("warm command returned %T, want mdWarmedMsg", wm)
	}
	if _, ok := wm.rendered[md]; !ok {
		t.Fatal("markdown entry missing from the warmed batch")
	}
	if _, ok := wm.rendered[plain]; ok {
		t.Error("operational one-liner must not be glamour-rendered")
	}

	// Merging serves the styled lines from the cache and clears pending.
	a.Update(wm)
	rendered := a.logMessageLines(md, msgWidth)
	if strings.Join(rendered, "\n") != strings.Join(wm.rendered[md], "\n") {
		t.Error("warmed lines should be served from the cache")
	}
	joined := stripANSI(strings.Join(rendered, "\n"))
	if !strings.Contains(joined, "Status") || !strings.Contains(joined, "verified") {
		t.Errorf("rendered markdown lost its text: %s", joined)
	}
	// No rendered line exceeds the content width (box border stays aligned).
	for i, ln := range rendered {
		if w := lipgloss.Width(ln); w > msgWidth {
			t.Errorf("rendered line %d width %d exceeds msgWidth %d: %q", i, w, msgWidth, stripANSI(ln))
		}
	}
	if a.logMDPending[md] {
		t.Error("pending flag should clear once the batch merges")
	}

	// Plain operational lines pass through wrapPlain untouched.
	if got := a.logMessageLines(plain, msgWidth); strings.Join(got, "\n") != strings.Join(wrapPlain(plain, msgWidth), "\n") {
		t.Error("plain message should pass through wrapPlain unchanged")
	}

	// Everything cached/pending-free: a second warm-up is a no-op.
	if a.warmMarkdownCmd([]LogEntry{{Message: md}, {Message: plain}}) != nil {
		t.Error("second warm-up should have nothing to do")
	}

	// All three log paths render framed with the markdown entry inline.
	now := time.Now()
	entries := []LogEntry{
		{Timestamp: now, Level: "INFO", Message: plain},
		{Timestamp: now, Level: "INFO", Message: md},
	}
	a.model.activeTab = 4 // Logs
	a.model.logsTab.Wrap = true
	a.model.logsTab.Logs = entries
	assertFramed(t, a.renderLogsTab(16), 90)

	// logEntryLines feeds the Tasks- and Agents-tab task-log views.
	taskLines := a.logEntryLines(entries)
	if len(taskLines) < 4 { // 1 plain + several rendered markdown lines
		t.Errorf("expected inline markdown to expand task logs, got %d lines", len(taskLines))
	}
}

func TestLogsTabWrapAndScroll(t *testing.T) {
	now := time.Now()
	long := "this is a very long operational log message that certainly exceeds the available width of the logs panel and must either wrap onto multiple lines or be horizontally scrollable by the user"
	a := newTestApp(60, 16)
	a.model.logsTab.Logs = []LogEntry{
		{Timestamp: now, Level: "INFO", Message: long},
		{Timestamp: now, Level: "ERROR", Message: "boom"},
	}

	// Wrapped: many visual lines, all within width, framed.
	a.model.logsTab.Wrap = true
	wrapped := a.logVisualLines()
	if len(wrapped) < 3 {
		t.Fatalf("wrap should break the long line; got %d visual lines", len(wrapped))
	}
	assertFramed(t, a.renderLogsTab(12), 60)

	// Unwrapped: one line per entry; horizontal scroll drops leading chars.
	a.model.logsTab.Wrap = false
	a.model.logsTab.HScroll = 0
	base := a.logVisualLines()
	if len(base) != 2 {
		t.Fatalf("unwrapped should be one line per entry; got %d", len(base))
	}
	a.model.logsTab.HScroll = 20
	scrolled := a.logVisualLines()
	if scrolled[0] == base[0] {
		t.Error("horizontal scroll did not change the rendered first line")
	}
	assertFramed(t, a.renderLogsTab(12), 60)
}

func TestAgentSubViewsFramed(t *testing.T) {
	now := time.Now()
	a := newTestApp(90, 20)
	a.model.agentsTab.Agents = []AgentStatus{
		{ID: "engineer", Status: "active", PID: 12345, MaxWorkers: 3, CompletedCount: 10, AvgDurationMs: 64000, SuccessRate: 0.9, LastTaskEndedAt: &now},
	}
	a.model.agentsTab.Detail = &a.model.agentsTab.Agents[0]
	a.model.agentsTab.Activity = []TaskItem{
		{TaskID: "T-1", Title: "implement handler", Status: "success", Duration: 32 * time.Second, CompletedAt: &now},
		{TaskID: "T-2", Title: "fix bug", Status: "failed", Duration: 5 * time.Second, CompletedAt: &now},
	}

	a.model.agentsTab.View = AgentViewDetail
	assertFramed(t, a.renderAgentsTab(14), 90)

	a.model.agentsTab.View = AgentViewActivity
	assertFramed(t, a.renderAgentsTab(14), 90)
}

func TestLogsPagingKeys(t *testing.T) {
	now := time.Now()
	a := newTestApp(80, 20) // pageSize = height-5 = 15
	a.model.activeTab = 4   // Logs (index 4 after inserting Usage at 3)
	// 60 short messages → 60 visual lines when wrapped.
	for i := 0; i < 60; i++ {
		a.model.logsTab.Logs = append(a.model.logsTab.Logs, LogEntry{Timestamp: now, Level: "INFO", Message: "line"})
	}
	total := len(a.logVisualLines())
	if total != 60 {
		t.Fatalf("expected 60 visual lines, got %d", total)
	}

	send := func(key string) { a.handleKeyMsg(keyPress(key)) }

	send("end")
	if a.model.logsTab.Scrolled != total-1 {
		t.Errorf("end → %d, want %d", a.model.logsTab.Scrolled, total-1)
	}
	send("home")
	if a.model.logsTab.Scrolled != 0 {
		t.Errorf("home → %d, want 0", a.model.logsTab.Scrolled)
	}
	send("pgdown")
	if a.model.logsTab.Scrolled != a.pageSize() {
		t.Errorf("pgdown → %d, want %d", a.model.logsTab.Scrolled, a.pageSize())
	}
	send("pgup")
	if a.model.logsTab.Scrolled != 0 {
		t.Errorf("pgup from one page → %d, want 0", a.model.logsTab.Scrolled)
	}
	// Paging never exceeds the last line.
	for i := 0; i < 100; i++ {
		send("pgdown")
	}
	if a.model.logsTab.Scrolled != total-1 {
		t.Errorf("pgdown clamp → %d, want %d", a.model.logsTab.Scrolled, total-1)
	}
}

func TestAgentActivityDrillToLogs(t *testing.T) {
	now := time.Now()
	a := newTestApp(90, 20)
	a.model.activeTab = 2 // Agents
	a.model.agentsTab.Agents = []AgentStatus{
		{ID: "engineer", Status: "active", PID: 12345, MaxWorkers: 3, RunningCount: 1, CurrentTask: "build X"},
	}
	a.model.agentsTab.View = AgentViewActivity
	a.model.agentsTab.Activity = []TaskItem{
		{TaskID: "T-1", Title: "first", Status: "running", StartedAt: &now},
		{TaskID: "T-2", Title: "second", Status: "success", Duration: 3 * time.Second, CompletedAt: &now},
		{TaskID: "T-3", Title: "third", Status: "failed", Duration: time.Second, CompletedAt: &now},
	}

	send := func(key string) { a.handleKeyMsg(keyPress(key)) }

	// Cursor moves with ↓ and the selected task id tracks it.
	send("down")
	if a.model.agentsTab.ActivityIdx != 1 {
		t.Fatalf("down → ActivityIdx %d, want 1", a.model.agentsTab.ActivityIdx)
	}
	if id, ok := a.selectedActivityTaskID(); !ok || id != "T-2" {
		t.Fatalf("selectedActivityTaskID = %q (%v), want T-2", id, ok)
	}
	// end jumps to last.
	send("end")
	if a.model.agentsTab.ActivityIdx != 2 {
		t.Fatalf("end → ActivityIdx %d, want 2", a.model.agentsTab.ActivityIdx)
	}

	// Activity view renders framed with a cursor.
	assertFramed(t, a.renderAgentsTab(14), 90)

	// Drill into the task-logs view and verify it is framed + back works.
	a.model.agentsTab.View = AgentViewTaskLogs
	a.model.agentsTab.LogsTaskID = "T-3"
	a.model.agentsTab.TaskLogs = []LogEntry{
		{Timestamp: now, Level: "INFO", Message: "starting"},
		{Timestamp: now, Level: "ERROR", Message: "boom"},
	}
	assertFramed(t, a.renderAgentsTab(14), 90)

	send("esc")
	if a.model.agentsTab.View != AgentViewActivity {
		t.Fatalf("esc from task logs → view %d, want AgentViewActivity", a.model.agentsTab.View)
	}
}

func TestAgentRelatedFilesFlow(t *testing.T) {
	// buildAgentFiles resolves paths relative to the working directory, so run
	// the test from a temp dir laid out like a real project.
	dir := t.TempDir()
	t.Chdir(dir)
	soulPath := filepath.Join("souls", "engineer.md")
	if err := os.MkdirAll(filepath.Dir(soulPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(soulPath, []byte("# Engineer soul\nbe excellent"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(".claude", "skills", "git-workflow", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("# git-workflow skill\nuse worktrees"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newTestApp(90, 20)
	a.model.activeTab = 2 // Agents
	a.model.agentsTab.Agents = []AgentStatus{
		{ID: "engineer", Status: "active", MaxWorkers: 2, SoulFile: soulPath, Skills: []string{"git-workflow", "ghost-skill"}},
	}
	a.model.agentsTab.Detail = &a.model.agentsTab.Agents[0]
	a.model.agentsTab.View = AgentViewDetail

	// buildAgentFiles lists soul + every skill; the missing one is flagged.
	files := a.buildAgentFiles(a.model.agentsTab.Detail)
	if len(files) != 3 {
		t.Fatalf("buildAgentFiles → %d files, want 3", len(files))
	}
	if files[0].Kind != "soul" || files[0].Missing {
		t.Errorf("file[0] = %+v, want present soul", files[0])
	}
	if files[1].Name != "git-workflow" || files[1].Missing {
		t.Errorf("file[1] = %+v, want present skill git-workflow", files[1])
	}
	if files[2].Name != "ghost-skill" || !files[2].Missing {
		t.Errorf("file[2] = %+v, want missing skill ghost-skill", files[2])
	}

	send := func(key string) { a.handleKeyMsg(keyPress(key)) }

	// f opens the files list; it renders framed.
	send("f")
	if a.model.agentsTab.View != AgentViewFiles {
		t.Fatalf("f → view %d, want AgentViewFiles", a.model.agentsTab.View)
	}
	assertFramed(t, a.renderAgentsTab(14), 90)

	// ↓ then enter opens the second file's content.
	send("down")
	send("enter")
	if a.model.agentsTab.View != AgentViewFileContent {
		t.Fatalf("enter → view %d, want AgentViewFileContent", a.model.agentsTab.View)
	}
	if !strings.Contains(a.model.agentsTab.FileContent, "git-workflow skill") {
		t.Errorf("FileContent = %q, want git-workflow skill body", a.model.agentsTab.FileContent)
	}
	out := stripANSI(a.renderAgentsTab(14))
	if !strings.Contains(out, "use worktrees") {
		t.Errorf("file viewer should show the file body; got:\n%s", out)
	}
	assertFramed(t, a.renderAgentsTab(14), 90)

	// esc walks back: content → files → detail.
	send("esc")
	if a.model.agentsTab.View != AgentViewFiles {
		t.Fatalf("esc from content → view %d, want AgentViewFiles", a.model.agentsTab.View)
	}
	send("esc")
	if a.model.agentsTab.View != AgentViewDetail {
		t.Fatalf("esc from files → view %d, want AgentViewDetail", a.model.agentsTab.View)
	}
}

func TestAgentMarkdownViewerToggle(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	soulPath := filepath.Join("souls", "engineer.md")
	if err := os.MkdirAll(filepath.Dir(soulPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# Engineer\n\nImplements tasks with **care**.\n\n- impact analysis\n- tests\n"
	if err := os.WriteFile(soulPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newTestApp(90, 20)
	a.model.activeTab = 2 // Agents
	a.model.agentsTab.Agents = []AgentStatus{
		{ID: "engineer", Status: "active", MaxWorkers: 2, SoulFile: soulPath},
	}
	a.model.agentsTab.Detail = &a.model.agentsTab.Agents[0]
	a.model.agentsTab.View = AgentViewDetail

	send := func(key string) { a.handleKeyMsg(keyPress(key)) }

	// Open the soul file: defaults to rendered markdown.
	send("f")
	send("enter")
	if a.model.agentsTab.View != AgentViewFileContent {
		t.Fatalf("enter → view %d, want AgentViewFileContent", a.model.agentsTab.View)
	}
	if a.model.agentsTab.FileRaw {
		t.Fatal("markdown should default to rendered, not raw")
	}
	rendered := a.agentFileLines()
	// Rendered markdown keeps the text but reflows it (margins, bullet glyphs,
	// styling), so it must differ from a plain wrap of the same source. The exact
	// glamour styling is terminal-dependent, so we assert the difference and the
	// preserved text rather than specific markers.
	joined := stripANSI(strings.Join(rendered, "\n"))
	if !strings.Contains(joined, "Engineer") || !strings.Contains(joined, "impact analysis") {
		t.Errorf("rendered markdown lost its text; got:\n%s", joined)
	}
	if strings.Join(rendered, "\n") == strings.Join(wrapPlain(body, a.model.width-2), "\n") {
		t.Error("rendered markdown should differ from plain wrapping")
	}
	assertFramed(t, a.renderAgentsTab(14), 90)

	// t toggles to raw: the source text (with the '#' marker) is shown verbatim,
	// matching plain wrapping, and the title is flagged.
	send("t")
	if !a.model.agentsTab.FileRaw {
		t.Fatal("t did not toggle to raw mode")
	}
	raw := a.agentFileLines()
	wantRaw := wrapPlain(body, a.model.width-2)
	if strings.Join(raw, "\n") != strings.Join(wantRaw, "\n") {
		t.Errorf("raw mode should equal plain wrapping;\ngot:  %q\nwant: %q", raw, wantRaw)
	}
	if title := stripANSI(a.renderAgentsTab(14)); !strings.Contains(title, "(raw)") {
		t.Error("raw mode should flag the title with (raw)")
	}
	assertFramed(t, a.renderAgentsTab(14), 90)

	// t toggles back to rendered.
	send("t")
	if a.model.agentsTab.FileRaw {
		t.Fatal("second t did not toggle back to rendered")
	}
}

func TestTaskNumberAndOpenURL(t *testing.T) {
	now := time.Now()
	a := newTestApp(100, 24)
	a.model.activeTab = 1 // Tasks
	a.model.tasksTab.History = []TaskItem{
		{TaskID: "uuid-1", Number: "ERP-42", Title: "do a thing", Agent: "engineer",
			Status: "running", URL: "https://app.plane.so/ws/projects/p/work-items/42/", StartedAt: &now},
		{TaskID: "uuid-2", Number: "ERP-7", Title: "another", Agent: "po", Status: "success", CompletedAt: &now},
	}

	// The human number is rendered in the list.
	out := stripANSI(a.renderTaskList(a.model.tasksTab, 12))
	if !strings.Contains(out, "ERP-42") {
		t.Errorf("task list should show the number ERP-42; got:\n%s", out)
	}
	assertFramed(t, a.renderTaskList(a.model.tasksTab, 12), 100)

	// Open resolves to the selected row's URL.
	a.model.tasksTab.SelectedIdx = 0
	if u, ok := a.focusedTaskURL(); !ok || u != a.model.tasksTab.History[0].URL {
		t.Errorf("focusedTaskURL = %q (%v), want row-0 URL", u, ok)
	}
	// A row without a URL reports no link.
	a.model.tasksTab.SelectedIdx = 1
	if _, ok := a.focusedTaskURL(); ok {
		t.Error("focusedTaskURL should be false for a row without a URL")
	}

	// Detail view prefers the open detail's URL.
	a.model.tasksTab.View = TaskViewDetail
	a.model.tasksTab.Detail = &a.model.tasksTab.History[0]
	if u, ok := a.focusedTaskURL(); !ok || u != a.model.tasksTab.History[0].URL {
		t.Errorf("focusedTaskURL in detail = %q (%v), want detail URL", u, ok)
	}
}

func TestTaskDetailShowsUsageWhenPresent(t *testing.T) {
	a := newTestApp(80, 24)
	a.model.tasksTab.View = TaskViewDetail
	a.model.tasksTab.Detail = &TaskItem{
		TaskID:       "T-1",
		Number:       "PRJ-5",
		Title:        "test task",
		Agent:        "engineer",
		Status:       "success",
		InputTokens:  1500,
		OutputTokens: 420,
		TotalTokens:  1920,
		NumTurns:     8,
		NumToolCalls: 23,
		CostUSD:      0.0425,
	}
	out := stripANSI(a.renderTaskDetail(a.model.tasksTab, 20))
	if !strings.Contains(out, "1.5k in / 420 out / 1.9k total") {
		t.Errorf("should show token breakdown; got:\n%s", out)
	}
	if !strings.Contains(out, "8 / 23") {
		t.Errorf("should show turns/tool calls; got:\n%s", out)
	}
	if !strings.Contains(out, "$0.0425") {
		t.Errorf("should show cost; got:\n%s", out)
	}
}

func TestTaskDetailShowsUsageWhenZero(t *testing.T) {
	a := newTestApp(80, 24)
	a.model.tasksTab.View = TaskViewDetail
	a.model.tasksTab.Detail = &TaskItem{
		TaskID: "T-2",
		Number: "PRJ-6",
		Title:  "old task",
		Agent:  "engineer",
		Status: "success",
	}
	out := stripANSI(a.renderTaskDetail(a.model.tasksTab, 20))
	if !strings.Contains(out, "0 in / 0 out / 0 total") {
		t.Errorf("should show tokens even when zero; got:\n%s", out)
	}
	if !strings.Contains(out, "0 / 0") {
		t.Errorf("should show turns/calls even when zero; got:\n%s", out)
	}
	if !strings.Contains(out, "$0.00") {
		t.Errorf("should show cost even when zero; got:\n%s", out)
	}
}

// A failed task populates CompletedAt too, so the terminal-timestamp row must
// be relabeled "Failed at" rather than "Completed" (which reads as success).
func TestTaskDetailFailedRelabelsCompletedRow(t *testing.T) {
	now := time.Now()
	a := newTestApp(80, 24)
	a.model.tasksTab.View = TaskViewDetail
	a.model.tasksTab.Detail = &TaskItem{
		TaskID:      "T-9",
		Title:       "failed task",
		Status:      "failed",
		CompletedAt: &now,
	}
	out := stripANSI(a.renderTaskDetail(a.model.tasksTab, 20))
	if !strings.Contains(out, "Failed at:") {
		t.Errorf("failed task should show a 'Failed at' row; got:\n%s", out)
	}
	if strings.Contains(out, "Completed:") {
		t.Errorf("failed task should not show a 'Completed' row; got:\n%s", out)
	}
}

func TestTaskDetailDoneKeepsCompletedRow(t *testing.T) {
	now := time.Now()
	a := newTestApp(80, 24)
	a.model.tasksTab.View = TaskViewDetail
	a.model.tasksTab.Detail = &TaskItem{
		TaskID:      "T-10",
		Title:       "done task",
		Status:      "done",
		CompletedAt: &now,
	}
	out := stripANSI(a.renderTaskDetail(a.model.tasksTab, 20))
	if !strings.Contains(out, "Completed:") {
		t.Errorf("done task should keep the 'Completed' row; got:\n%s", out)
	}
}

// detailWithManyChildren builds a task whose content far exceeds a short box,
// so the detail view must scroll rather than truncate the tail.
func detailWithManyChildren(n int) *TaskItem {
	children := make([]TaskLineageItem, n)
	for i := range children {
		children[i] = TaskLineageItem{
			TaskID:        fmt.Sprintf("tk_child_%02d", i),
			Title:         fmt.Sprintf("child task number %02d", i),
			State:         "running",
			InstanceCount: 1,
		}
	}
	return &TaskItem{
		TaskID:   "T-scroll",
		Title:    "parent with many children",
		Status:   "running",
		Children: children,
	}
}

// A task with more content than the box is tall must scroll: at the top the tail
// is hidden, and scrolling to the end reveals it while hiding the header.
func TestTaskDetailScrolls(t *testing.T) {
	a := newTestApp(90, 24)
	tt := a.model.tasksTab
	tt.View = TaskViewDetail
	tt.Detail = detailWithManyChildren(40)

	const height = 20 // 18 body rows — far fewer than the ~55 content lines

	top := stripANSI(a.renderTaskDetail(tt, height))
	if !strings.Contains(top, "Number") {
		t.Fatalf("at scroll 0 the header should be visible; got:\n%s", top)
	}
	if strings.Contains(top, "child task number 39") {
		t.Errorf("at scroll 0 the last child should be cut off; got:\n%s", top)
	}

	// Jump to the end via the key handler, then render (render applies the clamp).
	tt.View = TaskViewDetail
	a.handleTaskSubViewKey("G")
	bottom := stripANSI(a.renderTaskDetail(tt, height))
	if !strings.Contains(bottom, "child task number 39") {
		t.Errorf("after scrolling to the end the last child should be visible; got:\n%s", bottom)
	}
	if strings.Contains(bottom, "Number") {
		t.Errorf("after scrolling to the end the header should be off-screen; got:\n%s", bottom)
	}

	// The scroll offset must never run past the last full page.
	lines := a.taskDetailLines(tt)
	if max := len(lines) - (height - 2); tt.DetailScroll != max {
		t.Errorf("DetailScroll = %d, want clamped to %d", tt.DetailScroll, max)
	}

	// The box stays framed at every scroll position.
	assertFramed(t, a.renderTaskDetail(tt, height), 90)
}

// Short content (fits the box) must never scroll, and the up/down keys are no-ops.
func TestTaskDetailNoScrollWhenContentFits(t *testing.T) {
	a := newTestApp(90, 40)
	tt := a.model.tasksTab
	tt.View = TaskViewDetail
	tt.Detail = &TaskItem{TaskID: "T-1", Title: "tiny", Status: "done"}

	a.handleTaskSubViewKey("down")
	a.renderTaskDetail(tt, 36) // render clamps
	if tt.DetailScroll != 0 {
		t.Errorf("DetailScroll = %d, want 0 when content fits", tt.DetailScroll)
	}
}

func TestContextualFooter(t *testing.T) {
	a := newTestApp(100, 24)

	// Footer width must always fill the terminal exactly.
	for _, tab := range []int{0, 1, 2, 3} {
		a.model.activeTab = tab
		if w := lipgloss.Width(a.renderFooter()); w != 100 {
			t.Errorf("tab %d footer width = %d, want 100", tab, w)
		}
	}

	keyset := func() string {
		var ks []string
		for _, f := range a.footerKeys() {
			ks = append(ks, f.k)
		}
		return strings.Join(ks, ",")
	}

	// Tasks list vs logs sub-view must show different hints.
	a.model.activeTab = 1 // Tasks
	a.model.tasksTab.View = TaskViewList
	listKeys := keyset()
	a.model.tasksTab.View = TaskViewLogs
	logKeys := keyset()
	if listKeys == logKeys {
		t.Errorf("expected different footer hints for list vs logs sub-view, both = %q", listKeys)
	}
	if !strings.Contains(logKeys, "esc") {
		t.Errorf("logs sub-view footer should offer esc/back; got %q", logKeys)
	}
}
