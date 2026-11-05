package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	runnerpkg "github.com/orlandoburli/apiary/internal/runner"
	"github.com/orlandoburli/apiary/internal/source"
)

// TestCheckWaits_HonoursCheckInterval pins that a parked wait_for with an explicit
// check_interval is not re-queried on every poll cycle. checkWaits runs once per
// cycle of every source, so a 15s source used to make each parked CI wait hit the
// forge several times a minute regardless of check_interval.
func TestCheckWaits_HonoursCheckInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "waits.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	wf := ciWorkflow()
	wf.Steps[1].WaitFor.CheckInterval = "1h"

	cfg := &config.Config{
		Version:   "1",
		Sources:   []config.SourceConfig{{ID: "src", Type: "fake"}},
		Agents:    []config.AgentConfig{{ID: "eng", Model: "test/model"}},
		Workflows: []config.WorkflowConfig{wf},
	}
	adapter := newCIAdapter()
	runner := &gateRunner{entered: make(chan struct{}), release: make(chan struct{})}
	d := &Dispatcher{
		cfg:         cfg,
		db:          dbc,
		sources:     map[string]source.Adapter{"src": adapter},
		runners:     map[string]runnerpkg.Runner{"agent-eng": runner},
		agentRunner: map[string]string{"eng": "claude"},
		agentSem:    map[string]chan struct{}{"eng": make(chan struct{}, 1)},
	}
	d.binder = source.NewSourceBinder(dbc)

	task, err := d.binder.Bind(ctx, model.SourceItem{ID: "A", SourceID: "src", Title: "A"})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	adapter.set("A", "pending")
	inst, _, _ := d.workflowEngine().RunInstance(ctx, wf, task)
	if got, _ := dbc.GetWorkflowInstance(ctx, inst); got == nil || got.State != db.InstanceStateBlocked {
		t.Fatalf("instance not parked at wait_for (state=%v)", got)
	}

	start := adapter.pollCount("A")
	waitUntil(t, 2*time.Second, func() bool {
		d.checkWaits(ctx)
		return adapter.pollCount("A") == start+1
	}, "first poll cycle did not re-check the parked wait")

	// Further cycles inside the interval must not query the forge again.
	for i := 0; i < 5; i++ {
		d.checkWaits(ctx)
		time.Sleep(20 * time.Millisecond)
	}
	if got := adapter.pollCount("A"); got != start+1 {
		t.Errorf("parked wait re-checked %d times within check_interval, want 1", got-start)
	}
}
