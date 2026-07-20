package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// writeTranscriptFixture creates logDir/transcripts/<task>/<name>.md and
// returns its path.
func writeTranscriptFixture(t *testing.T, logDir, task, name, content string) string {
	t.Helper()
	dir := filepath.Join(logDir, "transcripts", task)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenTranscriptViewFromTaskList(t *testing.T) {
	a := newTestApp(80, 24)
	a.logDir = t.TempDir()
	a.model.activeTab = 1 // Tasks
	path := writeTranscriptFixture(t, a.logDir, "task-1", "wf1-implement", "# Hello\n\ntranscript body\n")

	tt := a.model.tasksTab
	tt.History = []TaskItem{{TaskID: "task-1"}}
	tt.SelectedIdx = 0

	handled, cmd := a.openTranscriptView()
	if !handled {
		t.Fatal("expected t to be handled with a transcript present")
	}
	if tt.View != TaskViewTranscript || tt.TranscriptPath != path {
		t.Fatalf("view=%v path=%q", tt.View, tt.TranscriptPath)
	}
	if !tt.TranscriptFollow {
		t.Fatal("follow should start enabled")
	}
	// Deliver the async load and check rendering.
	msg := cmd().(taskTranscriptMsg)
	a.applyTranscriptMsg(msg)
	out := stripANSI(a.renderTaskTranscript(tt, 20))
	if !strings.Contains(out, "transcript body") {
		t.Fatalf("rendered view missing content:\n%s", out)
	}
	if !strings.Contains(out, "TRANSCRIPT — wf1-implement.md") {
		t.Fatalf("missing title:\n%s", out)
	}
}

func TestTranscriptStaleReloadIgnored(t *testing.T) {
	a := newTestApp(80, 24)
	a.logDir = t.TempDir()
	tt := a.model.tasksTab
	tt.View = TaskViewTranscript
	tt.TranscriptPath = "/current.md"
	a.applyTranscriptMsg(taskTranscriptMsg{path: "/stale.md", content: "old"})
	if tt.TranscriptContent != "" {
		t.Fatal("stale reload for another path must be ignored")
	}
}

// Regression: follow mode must anchor the viewport to the LAST PAGE of the
// transcript. Pinning to the last line index rendered one line followed by an
// empty screen (and every live reload snapped back to it), which made the
// view look completely broken on real transcripts.
func TestTranscriptFollowShowsTail(t *testing.T) {
	a := newTestApp(120, 40)
	a.model.activeTab = 1
	tt := a.model.tasksTab
	tt.View = TaskViewTranscript
	tt.TranscriptPath = "/x.md"
	tt.TranscriptFollow = true

	var content strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&content, "line %d\n\n", i)
	}
	content.WriteString("FINAL LINE\n")
	a.applyTranscriptMsg(taskTranscriptMsg{path: "/x.md", content: content.String()})

	out := stripANSI(a.renderTaskTranscript(tt, 38))
	if !strings.Contains(out, "FINAL LINE") {
		t.Fatalf("follow mode must show the tail of the transcript:\n%s", out)
	}
	// The visible window must be full of content, not one line + blanks.
	nonEmpty := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.Trim(l, "│ ┌┐└┘─") != "" {
			nonEmpty++
		}
	}
	if nonEmpty < 10 {
		t.Fatalf("follow window nearly empty (%d content lines):\n%s", nonEmpty, out)
	}
}

// Regression: tab-indented code in tool calls (Go code is tab-indented)
// overflowed the box — lipgloss counts a tab as one cell but the terminal
// expands it to the next tab stop, so every tabbed line pushed past the right
// border and shattered the layout. Tabs must be expanded before rendering.
func TestTranscriptTabsDoNotBreakBox(t *testing.T) {
	a := newTestApp(100, 30)
	a.model.activeTab = 1
	tt := a.model.tasksTab
	tt.View = TaskViewTranscript
	tt.TranscriptPath = "/x.md"
	code := "### 🔧 Tool: `Bash`\n\n```go\nfunc create() {\n\t_, err := pool.Exec(ctx,\n\t\t`INSERT INTO tenants (id, nome, slug)`)\n\tif err != nil {\n\t\tt.Fatalf(\"schema: %v\", err)\n\t}\n}\n```\n"
	a.applyTranscriptMsg(taskTranscriptMsg{path: "/x.md", content: code})

	for _, raw := range []bool{false, true} {
		tt.TranscriptRaw = raw
		tt.invalidateTranscriptLines()
		for i, l := range a.taskTranscriptLines() {
			if strings.Contains(l, "\t") {
				t.Fatalf("raw=%v line %d still contains a tab: %q", raw, i, l)
			}
			if w := lipgloss.Width(l); w > 98 {
				t.Fatalf("raw=%v line %d width %d exceeds box inner width: %q", raw, i, w, l)
			}
		}
	}
}

func TestTranscriptLoadingSpinner(t *testing.T) {
	a := newTestApp(100, 30)
	a.model.activeTab = 1
	tt := a.model.tasksTab
	tt.View = TaskViewTranscript
	tt.TranscriptPath = "/x.md"
	out := stripANSI(a.renderTaskTranscript(tt, 20))
	if !strings.Contains(out, "loading transcript") {
		t.Fatalf("empty view must show the loading spinner:\n%s", out)
	}
}

func TestTranscriptKeysScrollAndFollow(t *testing.T) {
	a := newTestApp(80, 10)
	a.logDir = t.TempDir()
	tt := a.model.tasksTab
	tt.View = TaskViewTranscript
	tt.TranscriptReturn = TaskViewList
	tt.TranscriptPath = "/x.md"
	tt.TranscriptFollow = true
	tt.TranscriptContent = strings.Repeat("line\n", 50)
	tt.invalidateTranscriptLines()

	a.handleTranscriptKey("up")
	if tt.TranscriptFollow {
		t.Fatal("scrolling up must clear follow")
	}
	a.handleTranscriptKey("end")
	if !tt.TranscriptFollow {
		t.Fatal("end must re-enable follow")
	}
	a.handleTranscriptKey("t")
	if !tt.TranscriptRaw {
		t.Fatal("t must toggle raw mode")
	}
	a.handleTranscriptKey("esc")
	if tt.View != TaskViewList || tt.TranscriptPath != "" {
		t.Fatalf("esc must restore the return view, got %v", tt.View)
	}
}

func TestOpenTranscriptViewNoFileConsumesKey(t *testing.T) {
	a := newTestApp(80, 24)
	a.logDir = t.TempDir()
	a.model.activeTab = 1 // Tasks
	tt := a.model.tasksTab
	tt.History = []TaskItem{{TaskID: "task-without-transcript"}}
	tt.SelectedIdx = 0
	handled, _ := a.openTranscriptView()
	if !handled {
		t.Fatal("focused task without transcript should consume the key (no-op)")
	}
	if tt.View != TaskViewList {
		t.Fatal("view must not change when there is no transcript")
	}
}
