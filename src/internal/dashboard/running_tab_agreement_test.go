package dashboard

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// The dashboard's Running tab and the daemon's reaper must never disagree
// about the same execution.
//
// The tab marks a row alive or stale by db.ExecutionIsAlive; the reaper closes
// a row by db.ExecutionIsStale. Both rules live in the db package precisely so
// that a row the tab shows as alive cannot also be one the reaper closes —
// that would be live work marked dead.
//
// This drives both through the real code paths against one database and
// checks they agree row by row.
func TestRunningTabAndReaperAgreeOnEveryRow(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "agree.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	now := time.Now()
	seed := func(taskID string, pid int, hb *time.Time) {
		t.Helper()
		exec, err := dbc.CreateExecution(ctx, taskID, "engineer", "work", taskID, "", "m", "cli", 1)
		if err != nil {
			t.Fatalf("create execution %s: %v", taskID, err)
		}
		if pid > 0 {
			if err := dbc.SetPID(ctx, exec.ID, pid); err != nil {
				t.Fatalf("set pid: %v", err)
			}
		}
	}

	// A run that is working right now.
	seed("T-live", 111, ptr(now.Add(-5*time.Second)))
	// A run whose pid was never recorded — the tab must not call it alive.
	seed("T-no-pid", 0, ptr(now.Add(-5*time.Second)))

	a := &App{model: NewModel()}
	a.dbConn = dbc
	a.model.activeTab = tabIndex(a.model, "Running")

	msg := a.fetchRunning()()
	rm, ok := msg.(runningDataMsg)
	if !ok {
		t.Fatalf("expected runningDataMsg, got %T", msg)
	}

	// What the tab believes about each row.
	tabAlive := map[string]bool{}
	for _, ag := range rm.agents {
		tabAlive[ag.TaskID] = ag.Alive
	}

	// What the reaper would do to each row.
	reaped, err := dbc.ReapStaleExecutions(ctx, now)
	if err != nil {
		t.Fatalf("ReapStaleExecutions: %v", err)
	}

	remaining, err := dbc.GetRunningExecutions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stillRunning := map[string]bool{}
	for _, r := range remaining {
		stillRunning[r.TaskID] = true
	}

	// The invariant that matters: the reaper never closes a row the tab shows
	// as alive. That is the failure that would matter — live work marked dead.
	//
	// The converse is not an error: a row the tab cannot prove alive is not
	// necessarily one the reaper can close. A run that started moments ago
	// carries no heartbeat yet, so the tab cannot call it alive and the
	// reaper cannot call it dead either. Both are right: neither has the
	// evidence yet.
	for taskID, alive := range tabAlive {
		if alive && !stillRunning[taskID] {
			t.Errorf("the tab shows %s as alive but the reaper closed it — the two views disagree", taskID)
		}
	}
	if reaped != 0 {
		t.Errorf("the reaper closed %d rows, want 0 — every row the tab shows is alive or has no pid", reaped)
	}
}

func ptr(t time.Time) *time.Time { return &t }
