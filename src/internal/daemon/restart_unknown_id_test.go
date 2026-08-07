package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

// countingSource records every side effect ForceRestart performs on a source so a
// test can assert that an unresolvable id touches nothing at all.
type countingSource struct {
	cell        model.SourceItem
	polls       []string
	statesSet   []string
	removeCalls int
}

func (f *countingSource) ID() string                                    { return "jira" }
func (f *countingSource) Connect(context.Context, map[string]any) error { return nil }
func (f *countingSource) Poll(context.Context, time.Time) ([]model.SourceItem, error) {
	return nil, nil
}
func (f *countingSource) Acknowledge(context.Context, model.SourceItem, model.AckAction) error {
	return nil
}
func (f *countingSource) WriteResult(context.Context, model.SourceItem, model.RunResult) error {
	return nil
}
func (f *countingSource) PollTask(_ context.Context, id string) (model.SourceItem, error) {
	f.polls = append(f.polls, id)
	return f.cell, nil
}
func (f *countingSource) RemoveLabels(context.Context, model.SourceItem, []string) error {
	f.removeCalls++
	return nil
}
func (f *countingSource) SetState(_ context.Context, _ model.SourceItem, state string) error {
	f.statesSet = append(f.statesSet, state)
	return nil
}

func restartUnknownDispatcher(dbc *db.Client, fake *countingSource) *Dispatcher {
	return &Dispatcher{
		db: dbc,
		cfg: &config.Config{
			Sources: []config.SourceConfig{{ID: "jira"}},
			Workflows: []config.WorkflowConfig{
				{ID: "eng", Trigger: &config.TriggerConfig{Match: config.RouteMatch{
					ExcludeLabels: []string{"in-progress"},
				}}, Steps: []config.StepConfig{{ID: "run", Agent: "engineer"}}},
			},
		},
		sources: map[string]source.Adapter{"jira": fake},
	}
}

// TestForceRestart_UnknownIDIsRejected covers issue #377: `apiary restart <id>`
// with an id that is not a cell (there, an internal_tasks id) reported success and
// went on to run every restart side effect with that raw id — killing an unrelated
// healthy run. An unresolvable id must fail loudly and touch nothing.
func TestForceRestart_UnknownIDIsRejected(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	// Two Jira cells, mirroring the report: the id the user typed belongs to
	// neither cell's id space.
	_, instA := seedBoundTask(ctx, t, dbc, "jira", "295651")
	_, instB := seedBoundTask(ctx, t, dbc, "jira", "297869")

	fake := &countingSource{cell: model.SourceItem{ID: "297869", Labels: []string{"in-progress"}}}
	d := restartUnknownDispatcher(dbc, fake)

	_, err := d.ForceRestart(ctx, "019fd9312ac74556ae907abbbd17e3be")
	if err == nil {
		t.Fatalf("ForceRestart with an unknown id must fail, got nil")
	}
	if !errors.Is(err, ErrUnknownCell) {
		t.Fatalf("error = %v, want it to wrap ErrUnknownCell", err)
	}

	// Nothing was restarted: both healthy instances stay running.
	for _, id := range []string{instA, instB} {
		inst, err := dbc.GetWorkflowInstance(ctx, id)
		if err != nil || inst == nil {
			t.Fatalf("get instance %s: inst=%v err=%v", id, inst, err)
		}
		if inst.State != db.InstanceStateRunning {
			t.Errorf("instance %s state = %q, want it left %q", id, inst.State, db.InstanceStateRunning)
		}
	}

	// And no source was touched with the bogus id.
	if len(fake.polls) != 0 || len(fake.statesSet) != 0 || fake.removeCalls != 0 {
		t.Errorf("unknown id must not reach the source: polls=%v states=%v removeLabels=%d",
			fake.polls, fake.statesSet, fake.removeCalls)
	}
}

// TestForceRestart_InternalTaskIDPointsAtItsCell keeps the failure actionable: the
// exact mistake from #377 (passing an internal task id) names the cell to use.
func TestForceRestart_InternalTaskIDPointsAtItsCell(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	taskID, _ := seedBoundTask(ctx, t, dbc, "jira", "295651")

	fake := &countingSource{}
	d := restartUnknownDispatcher(dbc, fake)

	_, err := d.ForceRestart(ctx, taskID)
	if err == nil || !errors.Is(err, ErrUnknownCell) {
		t.Fatalf("ForceRestart(task id) = %v, want an ErrUnknownCell failure", err)
	}
	if !strings.Contains(err.Error(), "295651") {
		t.Errorf("error %q should name the bound cell 295651", err)
	}
}

// TestForceRestart_KnownCellStillRestarts guards the fix against over-reach: a real
// cell id must still cancel, interrupt its instance, and strip control labels.
func TestForceRestart_KnownCellStillRestarts(t *testing.T) {
	ctx := context.Background()
	dbc := openTestDB(ctx, t)

	_, instID := seedBoundTask(ctx, t, dbc, "jira", "295651")

	fake := &countingSource{cell: model.SourceItem{ID: "295651", Labels: []string{"in-progress", "bug"}}}
	d := restartUnknownDispatcher(dbc, fake)

	if _, err := d.ForceRestart(ctx, "295651"); err != nil {
		t.Fatalf("ForceRestart(known cell): %v", err)
	}

	inst, err := dbc.GetWorkflowInstance(ctx, instID)
	if err != nil || inst == nil {
		t.Fatalf("get instance: inst=%v err=%v", inst, err)
	}
	if inst.State != db.InstanceStateInterrupted {
		t.Errorf("instance state = %q, want %q", inst.State, db.InstanceStateInterrupted)
	}
	if len(fake.statesSet) != 1 || fake.statesSet[0] != "todo" {
		t.Errorf("statesSet = %v, want [todo]", fake.statesSet)
	}
	if fake.removeCalls != 1 {
		t.Errorf("removeCalls = %d, want 1 (the in-progress control label)", fake.removeCalls)
	}
}
