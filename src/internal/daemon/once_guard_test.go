package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/router"
)

// TestDropOnceMatches verifies the run-at-most-once guard (issue #119): a route
// that opted into `once: true` is dropped once the task has a completed (done)
// instance of it, so a spec/decomposition workflow whose source item stays in its
// trigger set is not re-dispatched into a duplicate fan-out. Routes without `once`
// are never dropped here, and a once-route that has not yet completed still runs.
func TestDropOnceMatches(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "once.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	// task T1: the decomposition (once) workflow already ran to done; a second
	// once-workflow only ever failed (not done); a normal workflow is also done.
	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i1", WorkflowID: "decompose", CellID: "1986", TaskID: "T1", State: db.InstanceStateDone})
	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i2", WorkflowID: "decompose-retry", CellID: "1986", TaskID: "T1", State: db.InstanceStateFailed})
	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i3", WorkflowID: "implementation", CellID: "1986", TaskID: "T1", State: db.InstanceStateDone})

	d := &Dispatcher{db: dbc}

	matches := []router.Match{
		{Route: config.RouteConfig{ID: "decompose", Once: true}},       // once + done → dropped
		{Route: config.RouteConfig{ID: "decompose-retry", Once: true}}, // once but only failed → kept (retryable)
		{Route: config.RouteConfig{ID: "implementation", Once: false}}, // not once → kept even though done
		{Route: config.RouteConfig{ID: "po-spec", Once: true}},         // once but never ran → kept
	}
	got := d.dropOnceMatches(ctx, "T1", matches)

	kept := map[string]bool{}
	for _, m := range got {
		kept[m.Route.ID] = true
	}
	if kept["decompose"] {
		t.Error("decompose is once-only and already done; it must be dropped")
	}
	if !kept["decompose-retry"] {
		t.Error("decompose-retry only failed (not done); once must not block a retry")
	}
	if !kept["implementation"] {
		t.Error("implementation did not opt into once; it must be kept")
	}
	if !kept["po-spec"] {
		t.Error("po-spec is once but never completed; it must be kept")
	}
	if len(got) != 3 {
		t.Errorf("expected 3 kept matches, got %d", len(got))
	}
}
