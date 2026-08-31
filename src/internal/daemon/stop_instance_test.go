package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
)

func stopInstanceFixture(t *testing.T) *Dispatcher {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "stop.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	return &Dispatcher{db: dbc}
}

// TestStopInstance_CancelsOnlyTheNamedInstance is the recovery half of issue
// #422: when a cell carries two live instances, stopping one must leave the
// other running. Cancellation used to be keyed by cell alone, which held only
// whichever run registered last.
func TestStopInstance_CancelsOnlyTheNamedInstance(t *testing.T) {
	ctx := context.Background()
	d := stopInstanceFixture(t)

	for _, id := range []string{"i-keep", "i-drop"} {
		if err := d.db.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
			ID: id, WorkflowID: "impl", CellID: "c1", SourceID: "src",
			TaskID: "T1", State: db.InstanceStateRunning,
		}); err != nil {
			t.Fatalf("create instance %s: %v", id, err)
		}
	}

	keepCtx, keepCancel := context.WithCancel(ctx)
	defer keepCancel()
	dropCtx, dropCancel := context.WithCancel(ctx)
	defer dropCancel()
	d.instanceCancel.Store("i-keep", context.CancelFunc(keepCancel))
	d.instanceCancel.Store("i-drop", context.CancelFunc(dropCancel))
	// The cell-keyed map holds the run that started last, as the executor leaves it.
	d.runCancel.Store("c1", context.CancelFunc(dropCancel))

	if err := d.StopInstance(ctx, "i-drop"); err != nil {
		t.Fatalf("stop instance: %v", err)
	}
	if dropCtx.Err() == nil {
		t.Error("the named instance's step was not cancelled")
	}
	if keepCtx.Err() != nil {
		t.Error("stopping one instance cancelled the sibling still running on the same cell")
	}
	inst, err := d.db.GetWorkflowInstance(ctx, "i-drop")
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if inst.State != db.InstanceStateBlocked {
		t.Errorf("stopped instance state = %q, want interrupted", inst.State)
	}
	keep, err := d.db.GetWorkflowInstance(ctx, "i-keep")
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if keep.State != db.InstanceStateRunning {
		t.Errorf("sibling instance state = %q, want running", keep.State)
	}
}

// TestStopInstance_FallsBackToTheCell keeps the pre-instance-keyed behavior for
// a run that registered no per-instance cancel (nothing in flight for it).
func TestStopInstance_FallsBackToTheCell(t *testing.T) {
	ctx := context.Background()
	d := stopInstanceFixture(t)
	if err := d.db.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "i-1", WorkflowID: "impl", CellID: "c1", SourceID: "src",
		TaskID: "T1", State: db.InstanceStateRunning,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.runCancel.Store("c1", context.CancelFunc(cancel))

	if err := d.StopInstance(ctx, "i-1"); err != nil {
		t.Fatalf("stop instance: %v", err)
	}
	if runCtx.Err() == nil {
		t.Error("the cell's in-flight run was not cancelled")
	}
}

// TestStopInstance_UnknownID reports a missing instance instead of claiming a
// run was stopped.
func TestStopInstance_UnknownID(t *testing.T) {
	d := stopInstanceFixture(t)
	if err := d.StopInstance(context.Background(), "nope"); !errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("stop unknown instance: err = %v, want ErrInstanceNotFound", err)
	}
}
