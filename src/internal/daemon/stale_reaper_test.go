package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// The periodic reaper runs while the daemon is working, so unlike the startup
// reconciliation it must not close a row that is still live.
//
// A row's staleness is decided in the db package (db.ExecutionIsStale), and
// the db package's own tests cover the boundary cases — this test is about the
// daemon's half: that the pass runs, and that it leaves a live run alone.
func TestReapStaleExecutionsOnceLeavesLiveRunsAlone(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "reaper.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	// A run that is working right now: a pid and a heartbeat from a moment ago.
	exec, err := dbc.CreateExecution(ctx, "T-live", "engineer", "work", "T-live", "", "m", "cli", 1)
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	if err := dbc.SetPID(ctx, exec.ID, 111); err != nil {
		t.Fatalf("set pid: %v", err)
	}
	if err := dbc.SendHeartbeat(ctx, exec.ID); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	d := &Dispatcher{db: dbc}
	d.reapStaleExecutionsOnce(ctx)

	running, err := dbc.GetRunningExecutions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 {
		t.Fatalf("%d executions still listed as running, want 1 (the live one)", len(running))
	}
	if running[0].TaskID != "T-live" {
		t.Errorf("the remaining running execution is %q, want T-live", running[0].TaskID)
	}
}

// The reaper must stop when the daemon's context is cancelled — a goroutine
// that outlives the daemon would keep writing to a database the daemon no
// longer owns.
func TestReapStaleExecutionsLoopStopsWithContext(t *testing.T) {
	d := &Dispatcher{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.reapStaleExecutionsLoop(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reaper loop did not stop when its context was cancelled")
	}
}
