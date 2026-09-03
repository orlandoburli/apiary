package dashboard

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func pagedTasks(prefix string, n int) []TaskItem {
	out := make([]TaskItem, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, TaskItem{TaskID: prefix + string(rune('a'+i)), InternalTaskID: prefix + string(rune('a'+i)), Title: prefix, Status: "done"})
	}
	return out
}

// The first page sets the cursor and the has-more flag; an older page is
// appended below the loaded rows, grows the refresh window and moves the cursor.
func TestOlderTasks_AppendGrowsWindow(t *testing.T) {
	a := newTestApp(90, 44)
	tt := a.model.tasksTab
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	a.Update(tasksDataMsg{items: pagedTasks("p1", 3), limit: 3, oldestAt: at, oldestID: "p1c", hasMore: true})
	if tt.ListWindow != 3 || !tt.ListHasMore || tt.ListOldestID != "p1c" || !tt.ListOldestAt.Equal(at) {
		t.Fatalf("first page state: window=%d hasMore=%v cursor=%q@%v", tt.ListWindow, tt.ListHasMore, tt.ListOldestID, tt.ListOldestAt)
	}

	tt.ListLoadingMore = true
	older := at.Add(-time.Hour)
	a.Update(olderTasksMsg{items: pagedTasks("p2", 2), oldestAt: older, oldestID: "p2b", hasMore: false})
	if len(tt.History) != 5 || tt.History[3].TaskID != "p2a" {
		t.Fatalf("older page not appended in order: %+v", tt.History)
	}
	if tt.ListWindow != 5 || tt.ListHasMore || tt.ListLoadingMore || tt.ListOldestID != "p2b" {
		t.Errorf("after append: window=%d hasMore=%v loading=%v cursor=%q, want 5/false/false/p2b", tt.ListWindow, tt.ListHasMore, tt.ListLoadingMore, tt.ListOldestID)
	}
}

// A refresh that was issued before an older page landed asked for the old,
// smaller window. Applying it would collapse the list back to the first page,
// so it is dropped; a refresh for the grown window is applied.
func TestOlderTasks_StaleRefreshDoesNotCollapseList(t *testing.T) {
	a := newTestApp(90, 44)
	tt := a.model.tasksTab
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	a.Update(tasksDataMsg{items: pagedTasks("p1", 3), limit: 3, oldestAt: at, oldestID: "p1c", hasMore: true})
	a.Update(olderTasksMsg{items: pagedTasks("p2", 2), oldestAt: at.Add(-time.Hour), oldestID: "p2b", hasMore: true})
	if len(tt.History) != 5 {
		t.Fatalf("setup: %d rows, want 5", len(tt.History))
	}

	a.Update(tasksDataMsg{items: pagedTasks("p1", 3), limit: 3, oldestAt: at, oldestID: "p1c", hasMore: true})
	if len(tt.History) != 5 || tt.ListWindow != 5 || tt.ListOldestID != "p2b" {
		t.Errorf("stale refresh applied: rows=%d window=%d cursor=%q, want 5/5/p2b", len(tt.History), tt.ListWindow, tt.ListOldestID)
	}

	a.Update(tasksDataMsg{items: pagedTasks("p1", 5), limit: 5, oldestAt: at.Add(-time.Hour), oldestID: "p1e", hasMore: false})
	if len(tt.History) != 5 || tt.History[3].TaskID != "p1d" || tt.ListHasMore {
		t.Errorf("window-sized refresh not applied: rows=%d row3=%q hasMore=%v", len(tt.History), tt.History[3].TaskID, tt.ListHasMore)
	}
}

// Pushing the cursor past the last row starts the older-page fetch exactly once
// (the in-flight guard swallows repeats), and only while a page may exist.
func TestOlderTasks_DownPastLastRowLoadsOnce(t *testing.T) {
	a := newTestApp(90, 44)
	a.model.activeTab = 1 // Tasks
	tt := a.model.tasksTab
	tt.View = TaskViewList
	a.Update(tasksDataMsg{items: pagedTasks("p1", 2), limit: 2, oldestAt: time.Now(), oldestID: "p1b", hasMore: true})

	down := tea.KeyMsg{Type: tea.KeyDown}
	_, cmd := a.Update(down) // 0 -> 1
	if cmd != nil || tt.SelectedIdx != 1 {
		t.Fatalf("plain move: idx=%d cmd=%v", tt.SelectedIdx, cmd != nil)
	}
	_, cmd = a.Update(down) // at the last row: load
	if cmd == nil || !tt.ListLoadingMore {
		t.Fatalf("down past the last row should start the fetch (cmd=%v loading=%v)", cmd != nil, tt.ListLoadingMore)
	}
	_, cmd = a.Update(down) // in flight: no second fetch
	if cmd != nil {
		t.Errorf("a second fetch was issued while one is in flight")
	}

	tt.ListLoadingMore = false
	tt.ListHasMore = false
	_, cmd = a.Update(down)
	if cmd != nil {
		t.Errorf("fetch issued with no older page available")
	}
}

// The list render shows the load-more hint under the last row while an older
// page is available, and the in-flight variant while it loads.
func TestRenderTaskList_LoadMoreHint(t *testing.T) {
	a := newTestApp(90, 44)
	tt := a.model.tasksTab
	tt.History = pagedTasks("p1", 2)
	tt.ListHasMore = true
	if out := a.renderTaskList(tt, 20); !strings.Contains(stripANSI(out), "older tasks") {
		t.Errorf("hint missing from render:\n%s", out)
	}
	tt.ListLoadingMore = true
	if out := a.renderTaskList(tt, 20); !strings.Contains(stripANSI(out), "loading older tasks") {
		t.Errorf("loading hint missing from render:\n%s", out)
	}
	tt.ListHasMore, tt.ListLoadingMore = false, false
	if out := a.renderTaskList(tt, 20); strings.Contains(stripANSI(out), "older tasks") {
		t.Errorf("hint shown with no older page:\n%s", out)
	}
}
