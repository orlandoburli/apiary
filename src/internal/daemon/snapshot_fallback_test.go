package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
	"github.com/orlandoburli/apiary/internal/state"
)

// A hot reload can remove or rename a workflow while instances are parked on
// it. Leaving them parked forever would strand work that already ran; the
// instance's snapshot is what it started with, so rehydration resumes from it.
func TestRehydrateParkedApprovalFallsBackToTheSnapshot(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "snapshot-fallback.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	// The live config no longer defines "feature".
	cfg := &config.Config{
		Version:  "1",
		Agents:   []config.AgentConfig{{ID: "architect", Model: "test/model"}},
		Settings: config.Settings{StateLock: false, ResultComment: false},
	}
	d := dispatcherWithConfig(cfg, &Dispatcher{
		db:          dbc,
		agentRunner: map[string]string{"architect": "claude"},
		agentSem:    map[string]chan struct{}{},
		stats:       map[string]*sourceStat{},
	})
	d.binder = source.NewSourceBinder(dbc)

	cell := model.SourceItem{ID: "ISSUE-1", SourceID: "src", Number: "#1", Title: "Add feature", State: "todo"}
	task, err := d.binder.Bind(ctx, cell)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	instID := "wf-parked-gone"
	if err := dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: instID, WorkflowID: "feature", TaskID: task.ID, CellID: cell.ID, SourceID: "src",
		State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonApproval),
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	started := time.Now()
	if err := dbc.CreateStepRun(ctx, &db.StepRun{
		ID: "sr-plan", WorkflowInstanceID: instID, StepID: "plan", AgentID: "architect",
		State: db.StepStatePassed, Output: "planned", StartedAt: &started, FinishedAt: &started,
	}); err != nil {
		t.Fatalf("create step run: %v", err)
	}

	// The definition the instance started with, written when it was created.
	wf := config.WorkflowConfig{ID: "feature", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect"},
		{ID: "gate", Type: config.StepTypeApproval, DependsOn: []string{"plan"}, Message: "Plan ready."},
	}}
	raw, err := json.Marshal(wf)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := dbc.PutWorkflowSnapshot(ctx, instID, string(raw)); err != nil {
		t.Fatalf("put snapshot: %v", err)
	}

	d.rehydrateParkedApprovals(ctx)

	if got := len(d.workflowEngine().ParkedApprovals()); got != 1 {
		t.Fatalf("parked approvals = %d, want 1 — the snapshot should have been used", got)
	}
}

// Without a snapshot there is nothing to resume from; the instance stays parked
// rather than being driven with a definition that no longer exists.
func TestRehydrateParkedApprovalWithoutSnapshotStaysParked(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "no-snapshot.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	cfg := &config.Config{Version: "1", Settings: config.Settings{StateLock: false, ResultComment: false}}
	d := dispatcherWithConfig(cfg, &Dispatcher{
		db:       dbc,
		agentSem: map[string]chan struct{}{},
		stats:    map[string]*sourceStat{},
	})
	d.binder = source.NewSourceBinder(dbc)

	cell := model.SourceItem{ID: "ISSUE-2", SourceID: "src", Number: "#2", Title: "Gone", State: "todo"}
	task, err := d.binder.Bind(ctx, cell)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := dbc.CreateWorkflowInstance(ctx, &db.WorkflowInstance{
		ID: "wf-no-snapshot", WorkflowID: "vanished", TaskID: task.ID, CellID: cell.ID, SourceID: "src",
		State: db.InstanceStateBlocked, BlockedReason: string(state.ReasonApproval),
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	d.rehydrateParkedApprovals(ctx)
	if got := len(d.workflowEngine().ParkedApprovals()); got != 0 {
		t.Fatalf("parked approvals = %d, want 0 — there is nothing to resume from", got)
	}
}
