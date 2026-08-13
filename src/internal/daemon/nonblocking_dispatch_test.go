package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/router"
)

// A saturated agent semaphore must NOT block fanOut (and thus the poll loop):
// the slot is acquired inside the dispatch goroutine, so fanOut returns promptly
// and the run waits off the poll thread. Before the fix this blocked forever.
func TestFanOut_DoesNotBlockOnSaturatedAgent(t *testing.T) {
	d, runner, _ := newFanoutDispatcher(t, []config.WorkflowConfig{
		fanoutWorkflow("wf-a", "a", 10, false),
	})
	// Saturate agent "a": capacity 1, already full.
	d.agentSem = map[string]chan struct{}{"a": make(chan struct{}, 1)}
	d.agentSem["a"] <- struct{}{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cell := model.SourceItem{ID: "c1", SourceID: "src", Title: "x"}
	task := model.InternalTask{ID: "t1"}
	matches := []router.Match{{
		Route:  config.RouteConfig{ID: "wf-a", Agent: "a"},
		Worker: config.WorkerConfig{ID: "a", Model: "m"},
	}}

	done := make(chan struct{})
	go func() {
		// persisted=false → no DB side effects; the dispatch goroutine parks on
		// the saturated semaphore until ctx is cancelled.
		d.fanOut(ctx, cell, nil, task, false, matches, fanOutOpts{ownsInFlight: true})
		close(done)
	}()

	select {
	case <-done:
		// fanOut returned without waiting for a slot — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("fanOut blocked on a saturated agent semaphore (poll loop would stall)")
	}

	// The run is parked, not executed, while the slot is unavailable.
	if got := runner.n.Load(); got != 0 {
		t.Errorf("dispatch ran %d time(s) while agent saturated, want 0", got)
	}
	cancel() // let the parked dispatch goroutine unwind via ctx.Done
}
