package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCountConsecutiveFailedInstances(t *testing.T) {
	ctx := context.Background()
	c, err := New(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	mk := func(id, wf, state string) {
		if err := c.CreateWorkflowInstance(ctx, &WorkflowInstance{ID: id, WorkflowID: wf, CellID: "1", TaskID: "T1", State: state}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	count := func() int {
		n, err := c.CountConsecutiveFailedInstances(ctx, "T1", "impl")
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	if count() != 0 {
		t.Fatalf("empty: want 0, got %d", count())
	}
	mk("i1", "impl", InstanceStateFailed)
	mk("i2", "impl", InstanceStateFailed)
	if count() != 2 {
		t.Errorf("two failures: want 2, got %d", count())
	}
	// A success resets the consecutive run.
	mk("i3", "impl", InstanceStateDone)
	if count() != 0 {
		t.Errorf("after success: want 0, got %d", count())
	}
	mk("i4", "impl", InstanceStateFailed)
	if count() != 1 {
		t.Errorf("one failure after success: want 1, got %d", count())
	}
	// A different workflow's failures are not counted.
	mk("o1", "other", InstanceStateFailed)
	mk("o2", "other", InstanceStateFailed)
	if count() != 1 {
		t.Errorf("cross-workflow leak: want 1, got %d", count())
	}
}
