package daemon

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/router"
)

// TestForceRestart_InterruptsWorkflowInstance covers the stuck-queue case a daemon
// restart creates: a `running` workflow_instance that was stranded (orphaned) and
// then shadows its workflow on every poll via dropActiveMatches, so the cell never
// re-dispatches. ForceRestart used to touch only the legacy task_executions/tasks
// rows and leave the instance `running`. It must now mark every non-terminal
// instance for the cell interrupted, which also clears the dropActiveMatches shadow.
func TestForceRestart_InterruptsWorkflowInstance(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	taskID, instID := seedBoundTask(ctx, t, dbc, "github", "1956")

	// Sanity: the running instance shadows its workflow before the restart.
	match := router.Match{Route: config.RouteConfig{ID: "wf"}}
	d := &Dispatcher{db: dbc, cfg: &config.Config{}}
	if kept, _ := d.dropActiveMatches(ctx, taskID, []router.Match{match}); len(kept) != 0 {
		t.Fatalf("precondition: running instance should shadow the workflow, got %d kept", len(kept))
	}

	if _, err := d.ForceRestart(ctx, "1956"); err != nil {
		t.Fatalf("ForceRestart: %v", err)
	}

	// The stranded instance is now interrupted (terminal), not running.
	inst, err := dbc.GetWorkflowInstance(ctx, instID)
	if err != nil || inst == nil {
		t.Fatalf("get instance: inst=%v err=%v", inst, err)
	}
	if inst.State != db.InstanceStateInterrupted {
		t.Fatalf("instance state = %q, want %q", inst.State, db.InstanceStateInterrupted)
	}

	// dropActiveMatches no longer shadows the workflow, so the next poll re-dispatches.
	if kept, _ := d.dropActiveMatches(ctx, taskID, []router.Match{match}); len(kept) != 1 {
		t.Fatalf("after restart the workflow should re-dispatch, got %d kept", len(kept))
	}
}
