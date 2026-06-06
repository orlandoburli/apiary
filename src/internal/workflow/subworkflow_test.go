package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

// cfgWithWorkflows returns a config whose Workflows slice is populated so the
// engine can resolve sub-workflow references.
func cfgWithWorkflows(wfs ...config.WorkflowConfig) *config.Config {
	c := baseCfg()
	c.Workflows = wfs
	return c
}

func TestSubWorkflow_ChildRunsAndLinks(t *testing.T) {
	child := config.WorkflowConfig{ID: "standard-review", Steps: []config.StepConfig{
		{ID: "review", Agent: "architect"},
		{ID: "fix", Agent: "backend-dev", DependsOn: []string{"review"}},
	}}
	parent := config.WorkflowConfig{ID: "feature", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "review-phase", Type: config.StepTypeWorkflow, Workflow: "standard-review", DependsOn: []string{"implement"}},
	}}

	cfg := cfgWithWorkflows(child, parent)
	store := newFakeStore()
	exec := &fakeExecutor{}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	parentID, success, _ := eng.RunInstance(context.Background(), parent, model.SourceItem{ID: "c1"})
	if !success {
		t.Fatal("expected success")
	}

	// Child steps ran.
	ids := executedIDs(exec.seen)
	for _, want := range []string{"implement", "review", "fix"} {
		if !contains(ids, want) {
			t.Errorf("expected %q to run, got %v", want, ids)
		}
	}

	// A child instance exists, linked to the parent.
	var childInst *db.WorkflowInstance
	for _, inst := range store.instances {
		if inst.WorkflowID == "standard-review" {
			childInst = inst
		}
	}
	if childInst == nil {
		t.Fatal("expected a child instance for standard-review")
	}
	if childInst.ParentInstanceID != parentID {
		t.Errorf("child parent_instance_id = %q, want %q", childInst.ParentInstanceID, parentID)
	}
	if childInst.State != db.InstanceStateDone {
		t.Errorf("child state = %q, want done", childInst.State)
	}
}

func TestSubWorkflow_ChildFailureFailsParentStep(t *testing.T) {
	child := config.WorkflowConfig{ID: "review", Steps: []config.StepConfig{
		{ID: "check", Agent: "architect"},
	}}
	parent := config.WorkflowConfig{ID: "main", Steps: []config.StepConfig{
		{ID: "sub", Type: config.StepTypeWorkflow, Workflow: "review"},
		{ID: "after", Agent: "backend-dev", DependsOn: []string{"sub"}},
	}}

	cfg := cfgWithWorkflows(child, parent)
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"check": {Success: false}}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	_, success, _ := eng.RunInstance(context.Background(), parent, model.SourceItem{ID: "c1"})
	if success {
		t.Fatal("expected parent to fail when child fails")
	}
	// "after" depends on the failed sub-workflow step → must not run.
	if contains(executedIDs(exec.seen), "after") {
		t.Error("step after a failed sub-workflow should not run")
	}
}

func TestSubWorkflow_UnknownReferenceFails(t *testing.T) {
	parent := config.WorkflowConfig{ID: "main", Steps: []config.StepConfig{
		{ID: "sub", Type: config.StepTypeWorkflow, Workflow: "ghost"},
	}}
	cfg := cfgWithWorkflows(parent)
	store := newFakeStore()
	eng := testEngine(cfg, store, &fakeExecutor{}, &fakeSide{})

	_, success, _ := eng.RunInstance(context.Background(), parent, model.SourceItem{ID: "c1"})
	if success {
		t.Fatal("expected failure for unknown sub-workflow reference")
	}
}

func TestSubWorkflow_ParentMemorySeedsChild(t *testing.T) {
	child := config.WorkflowConfig{ID: "child", Steps: []config.StepConfig{
		{ID: "use", Agent: "architect"},
	}}
	parent := config.WorkflowConfig{ID: "parent", Steps: []config.StepConfig{
		{
			ID: "plan", Agent: "architect",
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"complexity": {Type: "string"}}},
			Memory: &config.MemoryConfig{Write: []string{"complexity"}},
		},
		{ID: "delegate", Type: config.StepTypeWorkflow, Workflow: "child", DependsOn: []string{"plan"}},
	}}

	cfg := cfgWithWorkflows(child, parent)
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"plan": {Success: true, StructuredOutput: map[string]any{"complexity": "high"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	_, success, _ := eng.RunInstance(context.Background(), parent, model.SourceItem{ID: "c1"})
	if !success {
		t.Fatal("expected success")
	}

	// The child's "use" step should see the parent's memory (complexity: high).
	var useMem string
	for _, req := range exec.seen {
		if req.Step.ID == "use" {
			useMem = req.MemoryDoc
		}
	}
	if !strings.Contains(useMem, "complexity: high") {
		t.Errorf("child step should inherit parent memory, got:\n%s", useMem)
	}
}

func TestSubWorkflow_NoNestingBeyondDepth(t *testing.T) {
	// Build a child that itself contains a workflow step; runtime must refuse it
	// (config validation also forbids this, but the engine guards independently).
	grandchild := config.WorkflowConfig{ID: "gc", Steps: []config.StepConfig{{ID: "x", Agent: "architect"}}}
	child := config.WorkflowConfig{ID: "child", Steps: []config.StepConfig{
		{ID: "nested", Type: config.StepTypeWorkflow, Workflow: "gc"},
	}}
	parent := config.WorkflowConfig{ID: "parent", Steps: []config.StepConfig{
		{ID: "sub", Type: config.StepTypeWorkflow, Workflow: "child"},
	}}

	cfg := cfgWithWorkflows(grandchild, child, parent)
	store := newFakeStore()
	eng := testEngine(cfg, store, &fakeExecutor{}, &fakeSide{})

	_, success, _ := eng.RunInstance(context.Background(), parent, model.SourceItem{ID: "c1"})
	if success {
		t.Fatal("expected failure: nesting beyond one level must be refused")
	}
}
