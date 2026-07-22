package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/orlandoburli/apiary/internal/db"
)

func TestCompareInstancesAlignsStepsAndComputesDeltas(t *testing.T) {
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "compare.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	for _, inst := range []*db.WorkflowInstance{
		{ID: "before", WorkflowID: "feature", CellID: "1", State: db.InstanceStateFailed},
		{ID: "after", WorkflowID: "feature", CellID: "1", State: db.InstanceStateDone, ResumedFrom: "before"},
	} {
		if err := dbc.CreateWorkflowInstance(ctx, inst); err != nil {
			t.Fatal(err)
		}
	}
	steps := []*db.StepRun{
		{ID: "before-plan", WorkflowInstanceID: "before", StepID: "plan", State: db.StepStatePassed, InputPrompt: "old", Output: "a", TotalTokens: 10, CostUSD: 0.1},
		{ID: "after-plan", WorkflowInstanceID: "after", StepID: "plan", State: db.StepStatePassed, InputPrompt: "new", Output: "b", TotalTokens: 15, CostUSD: 0.15},
		{ID: "after-review", WorkflowInstanceID: "after", StepID: "review", State: db.StepStatePassed},
	}
	for _, step := range steps {
		if err := dbc.CreateStepRun(ctx, step); err != nil {
			t.Fatal(err)
		}
	}
	comparison, err := (&Dispatcher{db: dbc}).CompareInstances(ctx, "before", "after")
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Steps) != 2 {
		t.Fatalf("steps = %d", len(comparison.Steps))
	}
	plan := comparison.Steps[0]
	if !plan.InputChanged || !plan.OutputChanged || plan.TokenDelta != 5 {
		t.Errorf("plan comparison = %+v", plan)
	}
	if plan.CostDeltaUSD < 0.049 || plan.CostDeltaUSD > 0.051 {
		t.Errorf("cost delta = %f", plan.CostDeltaUSD)
	}
	if comparison.Steps[1].Before != nil || comparison.Steps[1].After == nil {
		t.Errorf("added step = %+v", comparison.Steps[1])
	}
}
