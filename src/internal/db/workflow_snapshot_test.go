package db

import (
	"context"
	"testing"
)

func TestWorkflowSnapshotRoundTrip(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: "wf-snapshot", WorkflowID: "feature", CellID: "1", State: InstanceStateRunning}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutWorkflowSnapshot(ctx, "wf-snapshot", `{"id":"feature"}`); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetWorkflowSnapshot(ctx, "wf-snapshot")
	if err != nil || got != `{"id":"feature"}` {
		t.Fatalf("snapshot = %q, err = %v", got, err)
	}
	missing, err := c.GetWorkflowSnapshot(ctx, "missing")
	if err != nil || missing != "" {
		t.Fatalf("missing snapshot = %q, err = %v", missing, err)
	}
}
