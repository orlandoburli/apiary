package dashboard

import "testing"

// An older page of flat logs is prepended and the viewport stays anchored: the
// scroll offset advances by the visual-line count of the newly prepended entries,
// so the line the user was looking at doesn't jump.
func TestOlderTaskLogs_PrependAnchorsScroll(t *testing.T) {
	a := newTestApp(90, 44)
	tt := a.model.tasksTab
	tt.View = TaskViewLogs
	tt.LogTaskID = "42"
	tt.LogHasMore = true
	tt.LogLoadingMore = true
	tt.LogScroll = 0
	tt.Logs = []LogEntry{{Level: "info", Message: "newest"}}

	older := []LogEntry{{Level: "info", Message: "old-1"}, {Level: "info", Message: "old-2"}}
	delta := len(a.logEntryLines(older))

	a.Update(olderTaskLogsMsg{taskID: "42", logs: older, oldestID: 5, hasMore: false})

	if len(tt.Logs) != 3 || tt.Logs[0].Message != "old-1" || tt.Logs[2].Message != "newest" {
		t.Fatalf("older logs not prepended in order: %+v", tt.Logs)
	}
	if tt.LogScroll != delta {
		t.Errorf("LogScroll = %d, want %d (anchored by prepended visual-line count)", tt.LogScroll, delta)
	}
	if tt.LogOldestID != 5 {
		t.Errorf("cursor LogOldestID = %d, want 5", tt.LogOldestID)
	}
	if tt.LogHasMore {
		t.Error("LogHasMore should be false after the final page")
	}
	if tt.LogLoadingMore {
		t.Error("LogLoadingMore should be cleared after the page lands")
	}
}

// A late older-logs message for a different task (the user navigated away and
// opened another task's logs) must be ignored, not prepended.
func TestOlderTaskLogs_IgnoresStaleTask(t *testing.T) {
	a := newTestApp(90, 44)
	tt := a.model.tasksTab
	tt.View = TaskViewLogs
	tt.LogTaskID = "current"
	tt.Logs = []LogEntry{{Level: "info", Message: "keep"}}

	a.Update(olderTaskLogsMsg{taskID: "stale", logs: []LogEntry{{Message: "drop"}}, hasMore: true})

	if len(tt.Logs) != 1 || tt.Logs[0].Message != "keep" {
		t.Errorf("stale older-logs msg should be ignored, got: %+v", tt.Logs)
	}
}
