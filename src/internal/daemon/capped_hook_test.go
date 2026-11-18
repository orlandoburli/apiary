package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// The failure cap must escalate through on_exhausted, not on_fail.
//
// on_fail is applied after every failure, and a workflow's on_fail commonly
// adds a label its own trigger excludes — needs-attention, which every
// workflow adds and every trigger excludes. That took the task out of its own
// trigger after the first failure, so it was never retried and settings.
// max_attempts never meant anything.
//
// on_exhausted is the hook for this moment: the retry budget is spent and a
// human is now needed. When it is set it replaces on_fail at the cap; when it
// is not, on_fail is applied as before — so a workflow that has not opted in
// behaves exactly as it did.
func TestParkCappedTaskPrefersOnExhausted(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })

	// A workflow with both hooks: on_exhausted is the one that must fire.
	wf := config.WorkflowConfig{
		ID:          "impl",
		OnFail:      &config.OnComplete{AddLabels: []string{"needs-attention"}},
		OnExhausted: &config.OnComplete{AddLabels: []string{"needs-human"}},
	}
	d := &Dispatcher{cfg: &config.Config{Workflows: []config.WorkflowConfig{wf}}, db: dbc}

	// The hook applier needs a source adapter, which this harness has none
	// of — so assert on the hook *selection*, which is the part that was
	// wrong before.
	hook := cappedTaskHook(d.cfg, "impl")
	if hook == nil {
		t.Fatal("no hook selected for a capped workflow")
	}
	if len(hook.AddLabels) != 1 || hook.AddLabels[0] != "needs-human" {
		t.Errorf("capped workflow hook = %+v, want the on_exhausted hook", hook.AddLabels)
	}

	// A workflow with only on_fail keeps it — nothing changes for a workflow
	// that has not opted in.
	wf2 := config.WorkflowConfig{
		ID:     "legacy",
		OnFail: &config.OnComplete{AddLabels: []string{"needs-attention"}},
	}
	d2 := &Dispatcher{cfg: &config.Config{Workflows: []config.WorkflowConfig{wf2}}, db: dbc}
	hook2 := cappedTaskHook(d2.cfg, "legacy")
	if hook2 == nil || len(hook2.AddLabels) != 1 || hook2.AddLabels[0] != "needs-attention" {
		t.Errorf("a workflow without on_exhausted must keep on_fail, got %+v", hook2)
	}

	// A workflow with neither has nothing to apply.
	d3 := &Dispatcher{cfg: &config.Config{Workflows: []config.WorkflowConfig{{ID: "bare"}}}, db: dbc}
	if hook := cappedTaskHook(d3.cfg, "bare"); hook != nil {
		t.Errorf("a workflow with no hook must select none, got %+v", hook)
	}
	_ = model.InternalTask{}
}
