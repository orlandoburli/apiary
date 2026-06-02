package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func newTestApp(w, h int) *App {
	a := &App{model: NewModel()}
	a.model.width = w
	a.model.height = h
	return a
}

// assertFramed checks every body line of a box starts/ends with a vertical bar
// and that no line's visible width exceeds the terminal width.
func assertFramed(t *testing.T, out string, width int) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected a framed box, got %d lines", len(lines))
	}
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") {
		t.Errorf("top border malformed: %q", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "└") || !strings.HasSuffix(last, "┘") {
		t.Errorf("bottom border malformed: %q", last)
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != width {
			t.Errorf("line %d width = %d, want %d: %q", i, w, width, ln)
		}
		if i > 0 && i < len(lines)-1 {
			if !strings.HasPrefix(ln, "│") || !strings.HasSuffix(ln, "│") {
				t.Errorf("body line %d not framed with side borders: %q", i, ln)
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
		{ID: "engineer", Status: "active", CompletedCount: 12, AvgDurationMs: 64000, SuccessRate: 0.91},
		{ID: "investigator", Status: "idle", CompletedCount: 3, AvgDurationMs: 1200, SuccessRate: 1.0},
	}
	assertFramed(t, a.renderAgentsTab(12), 100)
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
