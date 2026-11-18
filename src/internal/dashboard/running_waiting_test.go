package dashboard

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// The Running tab shows the workflows the hive holds but cannot advance:
// instances parked on an approval, a CI run or a blocker issue.
//
// A workflow is not a process. It stays non-terminal long after the agent that
// was running it is gone — parked on a CI run for a day, or waiting for a
// human to approve it. Nothing else in the dashboard shows those, so a task
// that has been waiting for two days is invisible everywhere else.
func TestRunningTabShowsWaitingWorkflows(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "waiting.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// An instance parked on a CI run, and one waiting for an approval.
	seedInstance(t, c, "inst-ci", "implementation", "T-1", "blocked", "ci")
	seedInstance(t, c, "inst-approval", "implementation", "T-2", "blocked", "approval")

	a := newTestApp(140, 30)
	a.dbConn = c
	a.model.activeTab = tabIndex(a.model, "Running")

	msg := a.fetchRunning()()
	rm, ok := msg.(runningDataMsg)
	if !ok {
		t.Fatalf("expected runningDataMsg, got %T", msg)
	}
	if len(rm.waiting) != 2 {
		t.Fatalf("got %d waiting instances, want 2: %+v", len(rm.waiting), rm.waiting)
	}

	upd, _ := a.Update(msg)
	a = upd.(*App)
	out := stripANSI(a.renderRunningTab(24))

	for _, want := range []string{"WAITING", "implementation", "CI run", "human approval"} {
		if !strings.Contains(out, want) {
			t.Errorf("the waiting section is missing %q:\n%s", want, out)
		}
	}
}

// The waiting section must not be hidden when nothing is running: "no agents
// running" plus a list of workflows waiting is exactly the state an operator
// needs to see, and it is the state where the running section is empty.
func TestRunningTabShowsWaitingWhenNothingIsRunning(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "waiting2.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	seedInstance(t, c, "inst-ci", "implementation", "T-1", "blocked", "ci")

	a := newTestApp(140, 30)
	a.dbConn = c
	a.model.activeTab = tabIndex(a.model, "Running")

	msg := a.fetchRunning()()
	upd, _ := a.Update(msg)
	a = upd.(*App)

	out := stripANSI(a.renderRunningTab(24))
	if !strings.Contains(out, "CI run") {
		t.Errorf("the waiting workflows must be shown even with nothing running:\n%s", out)
	}
	if !strings.Contains(out, "No agents running right now") {
		t.Errorf("the empty running section should still say so:\n%s", out)
	}
}

// A terminal instance is not waiting for anything: it is done.
func TestRunningTabOmitsTerminalInstances(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "waiting3.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	seedInstance(t, c, "inst-done", "implementation", "T-1", "done", "")
	seedInstance(t, c, "inst-failed", "implementation", "T-2", "failed", "")

	a := &App{model: NewModel()}
	a.dbConn = c
	msg := a.fetchRunning()()
	rm := msg.(runningDataMsg)
	if len(rm.waiting) != 0 {
		t.Fatalf("terminal instances must not be listed as waiting: %+v", rm.waiting)
	}
}

// The box drops whatever does not fit, silently. A waiting list that
// overflowed would therefore vanish below the fold — the one failure this
// section must not have. What was cut off must be counted and shown.
func TestRunningTabReportsHowManyWaitingRowsWereCut(t *testing.T) {
	ctx := context.Background()
	c, err := db.New(ctx, filepath.Join(t.TempDir(), "waiting4.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	for i := 0; i < 40; i++ {
		seedInstance(t, c, "inst-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "implementation",
			"T-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "blocked", "ci")
	}

	a := newTestApp(140, 30)
	a.dbConn = c
	a.model.activeTab = tabIndex(a.model, "Running")

	msg := a.fetchRunning()()
	upd, _ := a.Update(msg)
	a = upd.(*App)

	out := stripANSI(a.renderRunningTab(20))
	if !strings.Contains(out, "and ") || !strings.Contains(out, "more") {
		t.Errorf("the rows that did not fit must be counted:\n%s", out)
	}
}

// The rows a person can act on come first: a workflow waiting on a human, a
// CI run or a blocker is waiting for something, while an interrupted instance
// is the debris of a run that died.
func TestWaitingRowsAreOrderedByWhatCanBeActedOn(t *testing.T) {
	rank := func(reason string) int {
		return waitingRank(WaitingInstance{WaitReason: reason})
	}
	// An approval is the most actionable — a human can clear it now.
	if rank("approval") >= rank("ci") {
		t.Errorf("an approval (%d) must rank above a CI wait (%d)", rank("approval"), rank("ci"))
	}
	if rank("ci") >= rank("dependency") {
		t.Errorf("a CI wait (%d) must rank above a dependency wait (%d)", rank("ci"), rank("dependency"))
	}
	if rank("dependency") >= rank("retry_backoff") {
		t.Errorf("a dependency wait (%d) must rank above a retry backoff (%d)", rank("dependency"), rank("retry_backoff"))
	}
	// An interrupted instance is over: it needs a decision, not a wait, so it
	// sorts last.
	if rank("interrupted") <= rank("retry_backoff") {
		t.Errorf("an interrupted instance (%d) must rank below a retry backoff (%d)", rank("interrupted"), rank("retry_backoff"))
	}
}

// The age column says how long a workflow has been waiting — the number that
// makes a two-day-old approval visible.
func TestWaitingAgeRenders(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m30s"},
		{90 * time.Minute, "1h30m"},
		{50 * time.Hour, "2d02h"},
	}
	for _, tc := range cases {
		if got := waitingAge(tc.age); got != tc.want {
			t.Errorf("waitingAge(%v) = %q, want %q", tc.age, got, tc.want)
		}
	}
	if got := waitingAge(0); got != "—" {
		t.Errorf("waitingAge(0) = %q, want an em dash", got)
	}
}

// seedInstance creates a workflow instance in a given state.
func seedInstance(t *testing.T, c *db.Client, id, workflowID, taskID, state, blockedReason string) {
	t.Helper()
	ctx := context.Background()
	inst := &db.WorkflowInstance{
		ID: id, WorkflowID: workflowID, TaskID: taskID, CellID: taskID,
		State: state, BlockedReason: blockedReason,
		CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now().Add(-time.Hour),
	}
	if err := c.CreateWorkflowInstance(ctx, inst); err != nil {
		t.Fatalf("seed instance %s: %v", id, err)
	}
}
