package dashboard

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func infoLines(n int) []LogEntry {
	now := time.Now()
	logs := make([]LogEntry, n)
	for i := range logs {
		logs[i] = LogEntry{Timestamp: now, Level: "INFO", Message: fmt.Sprintf("line %d", i)}
	}
	return logs
}

// Opening the task logs view lands pinned to the tail, and appended live-tail
// lines keep the viewport on the newest line.
func TestTaskLogsOpenPinnedToTail(t *testing.T) {
	a := newTestApp(80, 20)
	a.model.activeTab = 1 // Tasks

	a.Update(taskLogsMsg{taskID: "T-1", logs: infoLines(40)})
	tt := a.model.tasksTab
	if tt.View != TaskViewLogs || !tt.LogFollow {
		t.Fatalf("open: View=%v LogFollow=%v, want logs view with follow on", tt.View, tt.LogFollow)
	}

	const rows = 12 // height 14 minus borders
	a.renderTaskLogs(tt, 14)
	if want := pinToTail(len(a.taskLogLines()), rows); tt.LogScroll != want {
		t.Errorf("open render: LogScroll = %d, want pinned %d", tt.LogScroll, want)
	}

	a.Update(tailTaskLogsMsg{taskID: "T-1", logs: infoLines(5), newestID: 99})
	a.renderTaskLogs(tt, 14)
	if want := pinToTail(len(a.taskLogLines()), rows); tt.LogScroll != want {
		t.Errorf("tail render: LogScroll = %d, want pinned %d", tt.LogScroll, want)
	}
}

// Scrolling up disengages follow: appended lines no longer move the viewport.
// G re-engages it.
func TestTaskLogsScrollUpStopsFollowing(t *testing.T) {
	a := newTestApp(80, 20)
	a.model.activeTab = 1
	a.Update(taskLogsMsg{taskID: "T-1", logs: infoLines(40)})
	tt := a.model.tasksTab
	a.renderTaskLogs(tt, 14)

	a.handleKeyMsg(keyPress("up"))
	if tt.LogFollow {
		t.Fatal("up: follow should disengage")
	}
	anchored := tt.LogScroll

	a.Update(tailTaskLogsMsg{taskID: "T-1", logs: infoLines(10), newestID: 99})
	a.renderTaskLogs(tt, 14)
	if tt.LogScroll != anchored {
		t.Errorf("tail while unfollowed: LogScroll = %d, want anchored %d", tt.LogScroll, anchored)
	}

	a.handleKeyMsg(keyPress("G"))
	if !tt.LogFollow {
		t.Error("G: follow should re-engage")
	}
}

// The Logs tab follows the tail by default and preserves a scrolled-up
// position across the periodic full refresh.
func TestLogsTabFollowsTail(t *testing.T) {
	a := newTestApp(80, 20)
	a.model.activeTab = 4 // Logs

	a.Update(logsDataMsg{logs: infoLines(60)})
	l := a.model.logsTab
	const rows = 13 // height 15 minus borders
	out := a.renderLogsTab(15)
	if want := pinToTail(len(a.logVisualLines()), rows); l.Scrolled != want {
		t.Errorf("refresh render: Scrolled = %d, want pinned %d", l.Scrolled, want)
	}
	if !strings.Contains(stripANSI(out), "line 59") {
		t.Error("following view should show the newest line")
	}

	a.handleKeyMsg(keyPress("up"))
	if l.Follow {
		t.Fatal("up: follow should disengage")
	}
	anchored := l.Scrolled

	a.Update(logsDataMsg{logs: infoLines(80)})
	a.renderLogsTab(15)
	if l.Scrolled != anchored {
		t.Errorf("refresh while unfollowed: Scrolled = %d, want anchored %d", l.Scrolled, anchored)
	}

	a.handleKeyMsg(keyPress("end"))
	a.renderLogsTab(15)
	if want := pinToTail(len(a.logVisualLines()), rows); !l.Follow || l.Scrolled != want {
		t.Errorf("end: Follow=%v Scrolled=%d, want follow pinned at %d", l.Follow, l.Scrolled, want)
	}
}

// The agent task-log drill-down refreshes in place: same task swaps data
// without resetting the view or a scrolled-up cursor.
func TestAgentTaskLogsRefreshPreservesScroll(t *testing.T) {
	a := newTestApp(80, 20)
	a.model.activeTab = 2 // Agents

	a.Update(agentTaskLogsMsg{taskID: "T-9", logs: infoLines(40)})
	ag := a.model.agentsTab
	if ag.View != AgentViewTaskLogs || !ag.TaskLogFollow {
		t.Fatalf("open: View=%v follow=%v, want task-logs view following", ag.View, ag.TaskLogFollow)
	}
	const rows = 12
	a.renderAgentTaskLogs(ag, 14)
	if want := pinToTail(len(a.agentTaskLogLines()), rows); ag.TaskLogIdx != want {
		t.Errorf("open render: TaskLogIdx = %d, want pinned %d", ag.TaskLogIdx, want)
	}

	// Live refresh while following stays pinned.
	a.Update(agentTaskLogsMsg{taskID: "T-9", logs: infoLines(50)})
	if ag.View != AgentViewTaskLogs {
		t.Fatal("refresh must not leave the task-logs view")
	}
	a.renderAgentTaskLogs(ag, 14)
	if want := pinToTail(len(a.agentTaskLogLines()), rows); ag.TaskLogIdx != want {
		t.Errorf("refresh render: TaskLogIdx = %d, want pinned %d", ag.TaskLogIdx, want)
	}

	// Scrolled-up cursor survives the next refresh.
	a.handleKeyMsg(keyPress("up"))
	anchored := ag.TaskLogIdx
	a.Update(agentTaskLogsMsg{taskID: "T-9", logs: infoLines(60)})
	a.renderAgentTaskLogs(ag, 14)
	if ag.TaskLogIdx != anchored {
		t.Errorf("refresh while unfollowed: TaskLogIdx = %d, want anchored %d", ag.TaskLogIdx, anchored)
	}
}

// Step-log refreshes only apply to the open panel for the same step; a stale
// refresh never opens the panel by itself.
func TestWorkflowStepLogsRefreshScoped(t *testing.T) {
	a := newTestApp(80, 20)
	tt := a.model.tasksTab

	a.Update(workflowStepLogsMsg{stepID: "build", open: false, logs: infoLines(5)})
	if tt.WorkflowShowLogs || tt.WorkflowLogs != nil {
		t.Fatal("stale refresh must not open the log panel")
	}

	a.Update(workflowStepLogsMsg{stepID: "build", open: true, logs: infoLines(5)})
	if !tt.WorkflowShowLogs || !tt.WorkflowLogFollow || tt.WorkflowLogStepID != "build" {
		t.Fatalf("open: ShowLogs=%v Follow=%v StepID=%q", tt.WorkflowShowLogs, tt.WorkflowLogFollow, tt.WorkflowLogStepID)
	}

	a.Update(workflowStepLogsMsg{stepID: "test", open: false, logs: infoLines(9)})
	if len(tt.WorkflowLogs) != 5 {
		t.Error("refresh for another step must be dropped")
	}

	a.Update(workflowStepLogsMsg{stepID: "build", open: false, logs: infoLines(9)})
	if len(tt.WorkflowLogs) != 9 {
		t.Error("refresh for the open step should swap the data")
	}
}
