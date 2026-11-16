package dashboard

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// The Running tab lists one row per in-flight agent process. The rows must
// carry the facts a user needs to tell "who is working on what": the agent,
// the task it is on, the model, and the PID.
func TestRunningTabRendersRows(t *testing.T) {
	a := newTestApp(120, 20)
	a.model.activeTab = tabIndex(a.model, "Running")
	now := time.Now()
	a.model.runningTab.Agents = []RunningAgent{
		{ExecID: 1, TaskID: "T-1", Number: "ERP-42", Title: "fix login", TaskURL: "https://example.test/42",
			AgentID: "engineer", Model: "claude-opus", Runner: "cli", PID: 4242, Alive: true, StartedAt: &now},
		{ExecID: 2, TaskID: "T-2", Number: "ERP-7", Title: "write docs",
			AgentID: "reviewer", Model: "gpt-5", Runner: "cli", PID: 4243, Alive: false, StartedAt: &now},
	}

	out := stripANSI(a.renderRunningTab(14))
	for _, want := range []string{"RUNNING", "AGENT", "TASK", "MODEL", "RUNNER", "PID", "ELAPSED",
		"engineer", "reviewer", "ERP-42", "ERP-7", "fix login", "claude-opus", "4242", "4243",
		"alive", "stale heartbeat"} {
		if !strings.Contains(out, want) {
			t.Errorf("Running tab output missing %q:\n%s", want, out)
		}
	}
}

// The empty state must say what is missing rather than render a blank panel:
// an empty list is the normal state of the Running tab between tasks.
func TestRunningTabEmptyState(t *testing.T) {
	a := newTestApp(90, 20)
	a.model.activeTab = tabIndex(a.model, "Running")

	out := stripANSI(a.renderRunningTab(14))
	if !strings.Contains(out, "No agents running right now") {
		t.Errorf("empty Running tab should explain itself:\n%s", out)
	}
}

// The cursor moves through the rows and clamps at both ends — it must never
// index outside the list.
func TestRunningTabCursorNavigation(t *testing.T) {
	a := newTestApp(90, 20)
	a.model.activeTab = tabIndex(a.model, "Running")
	a.model.runningTab.Agents = []RunningAgent{
		{AgentID: "a1", TaskID: "T-1"},
		{AgentID: "a2", TaskID: "T-2"},
		{AgentID: "a3", TaskID: "T-3"},
	}

	// Up at the top must not go negative.
	a.handleKeyMsg(keyPress("up"))
	if a.model.runningTab.SelectedIdx != 0 {
		t.Errorf("up at top: SelectedIdx = %d, want 0", a.model.runningTab.SelectedIdx)
	}

	a.handleKeyMsg(keyPress("down"))
	a.handleKeyMsg(keyPress("down"))
	if a.model.runningTab.SelectedIdx != 2 {
		t.Errorf("down ×2: SelectedIdx = %d, want 2", a.model.runningTab.SelectedIdx)
	}

	// Down at the bottom must not run past the last row.
	a.handleKeyMsg(keyPress("down"))
	if a.model.runningTab.SelectedIdx != 2 {
		t.Errorf("down at bottom: SelectedIdx = %d, want 2", a.model.runningTab.SelectedIdx)
	}

	a.handleKeyMsg(keyPress("home"))
	if a.model.runningTab.SelectedIdx != 0 {
		t.Errorf("home: SelectedIdx = %d, want 0", a.model.runningTab.SelectedIdx)
	}

	a.handleKeyMsg(keyPress("end"))
	if a.model.runningTab.SelectedIdx != 2 {
		t.Errorf("end: SelectedIdx = %d, want 2", a.model.runningTab.SelectedIdx)
	}
}

// The selected row identifies the task whose workflow the user can open, so
// the helper that resolves it must track the cursor.
func TestSelectedRunningTaskID(t *testing.T) {
	a := newTestApp(90, 20)
	a.model.activeTab = tabIndex(a.model, "Running")
	a.model.runningTab.Agents = []RunningAgent{
		{AgentID: "a1", TaskID: "T-1"},
		{AgentID: "a2", TaskID: "T-2"},
	}

	id, ok := a.selectedRunningTaskID()
	if !ok || id != "T-1" {
		t.Fatalf("selectedRunningTaskID = %q (%v), want T-1", id, ok)
	}

	a.handleKeyMsg(keyPress("down"))
	id, ok = a.selectedRunningTaskID()
	if !ok || id != "T-2" {
		t.Fatalf("selectedRunningTaskID after down = %q (%v), want T-2", id, ok)
	}

	// With no rows there is nothing to open.
	a.model.runningTab.Agents = nil
	if _, ok := a.selectedRunningTaskID(); ok {
		t.Error("selectedRunningTaskID on an empty list should report false")
	}
}

// The focused-task URL resolver must serve the Running tab too: "o" opens the
// selected running agent's task in the browser.
func TestFocusedTaskURLOnRunningTab(t *testing.T) {
	a := newTestApp(90, 20)
	a.model.activeTab = tabIndex(a.model, "Running")
	a.model.runningTab.Agents = []RunningAgent{
		{AgentID: "a1", TaskID: "T-1", TaskURL: "https://example.test/1"},
		{AgentID: "a2", TaskID: "T-2", TaskURL: "https://example.test/2"},
	}

	u, ok := a.focusedTaskURL()
	if !ok || u != "https://example.test/1" {
		t.Fatalf("focusedTaskURL on Running = %q (%v), want the first row's URL", u, ok)
	}

	a.handleKeyMsg(keyPress("down"))
	u, ok = a.focusedTaskURL()
	if !ok || u != "https://example.test/2" {
		t.Fatalf("focusedTaskURL after down = %q (%v), want the second row's URL", u, ok)
	}
}

// The tab must exist and be reachable: the tab bar lists it, and fetchActiveTab
// queries it when it is the active tab.
func TestRunningTabIsRegistered(t *testing.T) {
	m := NewModel()
	if tabIndex(m, "Running") < 0 {
		t.Fatal("Running tab is not registered")
	}
	if m.runningTab == nil {
		t.Fatal("runningTab model is nil")
	}
}

// The alive/stale distinction is the tab's whole point: a row the database
// still calls 'running' is only live work when the process proves it — a PID
// and a heartbeat from the last minute. Anything else is a stale row left
// behind by a process that died without flipping its execution row.
func TestRunningProcessAlive(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-5 * time.Second)
	old := now.Add(-time.Hour)

	cases := []struct {
		name      string
		pid       int
		heartbeat *time.Time
		want      bool
	}{
		{"fresh heartbeat with a pid", 1234, &fresh, true},
		{"stale heartbeat", 1234, &old, false},
		{"no pid", 0, &fresh, false},
		{"no heartbeat", 1234, nil, false},
	}
	for _, tc := range cases {
		if got := runningProcessAlive(tc.pid, tc.heartbeat, now); got != tc.want {
			t.Errorf("%s: runningProcessAlive = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The rows the tab lists must be exactly the executions the database still
// calls 'running' — a finished execution must not appear.
func TestFetchRunningListsOnlyRunningExecutions(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "running.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	live, err := c.CreateExecution(ctx, "T-live", "engineer", "work", "REF-1", "https://x/1", "claude-opus", "cli", 1)
	if err != nil {
		t.Fatalf("create live execution: %v", err)
	}
	if err := c.SetPID(ctx, live.ID, 111); err != nil {
		t.Fatalf("set pid: %v", err)
	}
	if err := c.SendHeartbeat(ctx, live.ID); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	done, err := c.CreateExecution(ctx, "T-done", "engineer", "work", "REF-3", "https://x/3", "claude-opus", "cli", 1)
	if err != nil {
		t.Fatalf("create done execution: %v", err)
	}
	done.Status = "success"
	now := time.Now()
	done.CompletedAt = &now
	if err := c.UpdateExecution(ctx, done); err != nil {
		t.Fatalf("finish execution: %v", err)
	}

	a := &App{model: NewModel(), dbConn: c}
	msg := a.fetchRunning()()
	rm, ok := msg.(runningDataMsg)
	if !ok {
		t.Fatalf("expected runningDataMsg, got %T", msg)
	}
	if len(rm.agents) != 1 {
		t.Fatalf("got %d running agents, want 1 (only the 'running' row): %+v", len(rm.agents), rm.agents)
	}
	ag := rm.agents[0]
	if ag.TaskID != "T-live" {
		t.Errorf("listed task = %q, want T-live", ag.TaskID)
	}
	if ag.PID != 111 {
		t.Errorf("listed pid = %d, want 111", ag.PID)
	}
	if !ag.Alive {
		t.Error("an execution with a fresh heartbeat and a PID must be alive")
	}
	if ag.Number != "REF-1" {
		t.Errorf("listed number = %q, want REF-1", ag.Number)
	}
	if ag.TaskURL != "https://x/1" {
		t.Errorf("listed url = %q, want https://x/1", ag.TaskURL)
	}
}

// The drill-in must land the user on the task's workflow monitor — in the
// Tasks tab, because that is where the monitor is rendered. Opening the
// monitor while leaving the user on the Running tab would show them nothing
// for the key they pressed.
func TestRunningTabDrilldownOpensWorkflowMonitor(t *testing.T) {
	a := newTestApp(120, 20)
	a.model.activeTab = tabIndex(a.model, "Running")
	a.model.runningTab.Agents = []RunningAgent{
		{AgentID: "engineer", TaskID: "T-1", Number: "ERP-42"},
	}

	// The monitor opens for the selected row's task.
	a.Update(workflowMonitorMsg{
		taskID: "T-1",
		instances: []*WorkflowInstanceItem{
			{ID: "inst-1", CellID: "T-1", Workflow: "feature-development"},
		},
	})

	if got := a.model.ActiveTab(); got != "Tasks" {
		t.Errorf("after drill-down the active tab = %q, want Tasks", got)
	}
	if a.model.tasksTab.View != TaskViewWorkflow {
		t.Errorf("after drill-down the Tasks view = %v, want TaskViewWorkflow", a.model.tasksTab.View)
	}
	if a.model.tasksTab.WorkflowTaskID != "T-1" {
		t.Errorf("monitor task = %q, want T-1", a.model.tasksTab.WorkflowTaskID)
	}
}

// The full path a user takes: a running agent's task has a workflow
// instance, and pressing enter on that row opens that instance's monitor.
//
// Exercised against a real DB, because the drill-down resolves the task's
// instances by querying workflow_instances for the task's cell id — the same
// id the execution rows carry.
func TestRunningTabEnterOpensTheTasksWorkflow(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "drill.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	base := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)
	if err := c.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "i1", WorkflowID: "feature-development", CellID: "ISSUE-9", TaskID: "ISSUE-9",
		State: db.InstanceStateRunning, CreatedAt: base,
	}); err != nil {
		t.Fatalf("create workflow instance: %v", err)
	}

	a := &App{model: NewModel(), dbConn: c}
	a.model.width = 120
	a.model.height = 20
	a.model.activeTab = tabIndex(a.model, "Running")
	a.model.runningTab.Agents = []RunningAgent{
		{AgentID: "engineer", TaskID: "ISSUE-9", Number: "#9", Alive: true},
	}

	// Press enter on the running agent's row: the key must resolve the
	// selected row's task.
	if id, ok := a.selectedRunningTaskID(); !ok || id != "ISSUE-9" {
		t.Fatalf("selectedRunningTaskID = %q (%v), want ISSUE-9", id, ok)
	}

	// The monitor command must find that task's workflow instance.
	msg, ok := a.openWorkflowMonitorOrLogs("ISSUE-9")().(workflowMonitorMsg)
	if !ok {
		t.Fatal("expected workflowMonitorMsg for a task with a workflow instance")
	}
	if len(msg.instances) != 1 || msg.instances[0].ID != "i1" {
		t.Fatalf("expected the task's workflow instance, got %+v", msg.instances)
	}

	a.Update(msg)
	if got := a.model.ActiveTab(); got != "Tasks" {
		t.Errorf("after drill-down the active tab = %q, want Tasks", got)
	}
	if a.model.tasksTab.View != TaskViewWorkflow {
		t.Errorf("after drill-down the Tasks view = %v, want TaskViewWorkflow", a.model.tasksTab.View)
	}
	if a.model.tasksTab.WorkflowTaskID != "ISSUE-9" {
		t.Errorf("monitor task = %q, want ISSUE-9", a.model.tasksTab.WorkflowTaskID)
	}
}
