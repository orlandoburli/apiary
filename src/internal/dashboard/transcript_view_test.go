package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
