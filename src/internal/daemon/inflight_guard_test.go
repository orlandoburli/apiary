package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/router"
)

// TestDropActiveMatches verifies the source-agnostic in-flight guard: a workflow
// with a non-terminal instance for the task is dropped (no re-dispatch), while a
// completed earlier workflow and a not-yet-run workflow are kept — so the
// triage→implementation hand-off still flows and a parked instance isn't doubled.
func TestDropActiveMatches(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "inflight.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	// task T1: implementation parked at an approval step (the gap inFlight misses);
	// triage already done.
	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i1", WorkflowID: "implementation", CellID: "1948", TaskID: "T1", State: db.InstanceStateApprovalWaiting})
	_ = dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{ID: "i2", WorkflowID: "triage", CellID: "1948", TaskID: "T1", State: db.InstanceStateDone})

	d := &Dispatcher{db: dbc}

	matches := []router.Match{
		{Route: config.RouteConfig{ID: "implementation"}}, // active → dropped
		{Route: config.RouteConfig{ID: "triage"}},         // done → kept
		{Route: config.RouteConfig{ID: "po-spec"}},        // never ran → kept
	}
	got := d.dropActiveMatches(ctx, "T1", matches)

	kept := map[string]bool{}
	for _, m := range got {
		kept[m.Route.ID] = true
	}
	if kept["implementation"] {
		t.Error("implementation is parked (approval_waiting) and must be dropped")
	}
	if !kept["triage"] || !kept["po-spec"] {
		t.Errorf("triage(done)/po-spec(absent) must be kept; got %v", kept)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 kept matches, got %d", len(got))
	}
}
