package workflow

import (
	"context"
	"encoding/json"
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

	parentID, success, _ := eng.RunInstance(context.Background(), parent, model.InternalTask{ID: "c1"})
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

	_, success, _ := eng.RunInstance(context.Background(), parent, model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("expected parent to fail when child fails")
	}
	// "after" depends on the failed sub-workflow step → must not run.
	if contains(executedIDs(exec.seen), "after") {
		t.Error("step after a failed sub-workflow should not run")
	}
	for _, sr := range store.stepRuns {
		if sr.StepID == "sub" && sr.State != db.StepStateFailed {
			t.Errorf("parent call step state = %q, want failed", sr.State)
		}
	}
}

func TestSubWorkflow_UnknownReferenceFails(t *testing.T) {
	parent := config.WorkflowConfig{ID: "main", Steps: []config.StepConfig{
		{ID: "sub", Type: config.StepTypeWorkflow, Workflow: "ghost"},
	}}
	cfg := cfgWithWorkflows(parent)
	store := newFakeStore()
	eng := testEngine(cfg, store, &fakeExecutor{}, &fakeSide{})

	_, success, _ := eng.RunInstance(context.Background(), parent, model.InternalTask{ID: "c1"})
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

	_, success, _ := eng.RunInstance(context.Background(), parent, model.InternalTask{ID: "c1"})
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

func TestSubWorkflow_AcyclicNestingRuns(t *testing.T) {
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

	_, success, _ := eng.RunInstance(context.Background(), parent, model.InternalTask{ID: "c1"})
	if !success {
		t.Fatal("expected acyclic nested workflows to succeed")
	}
	if !contains(executedIDs(eng.exec.(*fakeExecutor).seen), "x") {
		t.Fatal("expected grandchild step to execute")
	}
}

func TestSubWorkflow_TypedInputsOutputsAndCallHistory(t *testing.T) {
	prepare := config.WorkflowConfig{
		ID: "prepare",
		Inputs: map[string]config.WorkflowInput{
			"repository": {Type: "string", Required: true},
		},
		Outputs: map[string]config.WorkflowOutput{
			"workspace": {Type: "string", Value: "${{ steps.checkout.workspace }}"},
		},
		Steps: []config.StepConfig{{
			ID: "checkout", Agent: "architect", Prompt: "Clone ${{ inputs.repository }}",
			OutputSchema: &config.OutputSchema{Type: "object", Properties: map[string]config.SchemaField{
				"workspace": {Type: "string"},
			}},
		}},
	}
	test := config.WorkflowConfig{
		ID: "test",
		Inputs: map[string]config.WorkflowInput{
			"workspace": {Type: "string", Required: true},
		},
		Steps: []config.StepConfig{{ID: "run-tests", Agent: "backend-dev", Prompt: "Test ${{ inputs.workspace }}"}},
	}
	parent := config.WorkflowConfig{ID: "main", Steps: []config.StepConfig{
		{ID: "prepare-call", Type: config.StepTypeWorkflow, Workflow: "prepare", With: map[string]any{"repository": "${{ task.repository }}"}},
		{ID: "test-call", Type: config.StepTypeWorkflow, Workflow: "test", DependsOn: []string{"prepare-call"}, With: map[string]any{"workspace": "${{ steps.prepare-call.workspace }}"}},
	}}
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"checkout": {Success: true, StructuredOutput: map[string]any{"workspace": "/tmp/apiary"}},
	}}
	eng := testEngine(cfgWithWorkflows(prepare, test, parent), store, exec, &fakeSide{})
	_, success, _ := eng.RunInstance(context.Background(), parent, model.InternalTask{
		ID: "task-1", Input: map[string]any{"repository": "orlandoburli/apiary"},
	})
	if !success {
		t.Fatal("expected typed subworkflow chain to succeed")
	}
	var checkoutPrompt, testPrompt string
	for _, req := range exec.seen {
		switch req.Step.ID {
		case "checkout":
			checkoutPrompt = req.Prompt
		case "run-tests":
			testPrompt = req.Prompt
		}
	}
	if checkoutPrompt != "Clone orlandoburli/apiary" || testPrompt != "Test /tmp/apiary" {
		t.Fatalf("rendered prompts = %q, %q", checkoutPrompt, testPrompt)
	}
	var call *db.StepRun
	for _, sr := range store.stepRuns {
		if sr.StepID == "prepare-call" {
			call = sr
		}
	}
	if call == nil || call.State != db.StepStatePassed {
		t.Fatalf("parent call history = %#v", call)
	}
	var outputs map[string]any
	if err := json.Unmarshal([]byte(call.StructuredOutput), &outputs); err != nil {
		t.Fatalf("decode call outputs: %v", err)
	}
	if outputs["workspace"] != "/tmp/apiary" {
		t.Fatalf("call outputs = %#v", outputs)
	}
}

func TestSubWorkflow_InputDefault(t *testing.T) {
	child := config.WorkflowConfig{
		ID: "child",
		Inputs: map[string]config.WorkflowInput{
			"branch": {Type: "string", Default: "main"},
		},
		Steps: []config.StepConfig{{ID: "run", Agent: "architect", Prompt: "Build ${{ inputs.branch }}"}},
	}
	parent := config.WorkflowConfig{ID: "parent", Steps: []config.StepConfig{{ID: "call", Type: config.StepTypeWorkflow, Workflow: "child"}}}
	exec := &fakeExecutor{}
	eng := testEngine(cfgWithWorkflows(child, parent), newFakeStore(), exec, &fakeSide{})
	_, success, _ := eng.RunInstance(context.Background(), parent, model.InternalTask{ID: "task-1"})
	if !success || len(exec.seen) != 1 || exec.seen[0].Prompt != "Build main" {
		t.Fatalf("default input was not applied: success=%v requests=%#v", success, exec.seen)
	}
}

type contextExecutor struct {
	started chan<- struct{}
}

func (e contextExecutor) ExecuteStep(ctx context.Context, _ StepRequest) StepResult {
	if e.started != nil {
		e.started <- struct{}{}
	}
	<-ctx.Done()
	return StepResult{Success: false, Err: ctx.Err(), Output: ctx.Err().Error()}
}

func TestSubWorkflow_TimeoutCancelsChildAndFailsHistory(t *testing.T) {
	child := config.WorkflowConfig{ID: "child", Steps: []config.StepConfig{{ID: "block", Agent: "architect"}}}
	parent := config.WorkflowConfig{ID: "parent", Steps: []config.StepConfig{{
		ID: "call", Type: config.StepTypeWorkflow, Workflow: "child", Timeout: "5ms",
	}}}
	store := newFakeStore()
	eng := testEngine(cfgWithWorkflows(child, parent), store, contextExecutor{}, &fakeSide{})
	_, success, _ := eng.RunInstance(context.Background(), parent, model.InternalTask{ID: "task-1"})
	if success {
		t.Fatal("expected timed-out child to fail parent")
	}
	for _, inst := range store.instances {
		if inst.WorkflowID == "child" && inst.State != db.InstanceStateFailed {
			t.Fatalf("child state = %q, want failed", inst.State)
		}
	}
	for _, sr := range store.stepRuns {
		if sr.StepID == "call" && (sr.State != db.StepStateFailed || !strings.Contains(sr.Output, "deadline exceeded")) {
			t.Fatalf("call history = %#v", sr)
		}
	}
}

func TestSubWorkflow_ParentCancellationCancelsChild(t *testing.T) {
	child := config.WorkflowConfig{ID: "child", Steps: []config.StepConfig{{ID: "block", Agent: "architect"}}}
	parent := config.WorkflowConfig{ID: "parent", Steps: []config.StepConfig{{ID: "call", Type: config.StepTypeWorkflow, Workflow: "child"}}}
	started := make(chan struct{}, 1)
	store := newFakeStore()
	eng := testEngine(cfgWithWorkflows(child, parent), store, contextExecutor{started: started}, &fakeSide{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() {
		_, success, _ := eng.RunInstance(ctx, parent, model.InternalTask{ID: "task-1"})
		result <- success
	}()
	<-started
	cancel()
	if success := <-result; success {
		t.Fatal("expected parent cancellation to fail the child call")
	}
	for _, inst := range store.instances {
		if inst.WorkflowID == "child" && inst.State != db.InstanceStateFailed {
			t.Fatalf("canceled child state = %q, want failed", inst.State)
		}
	}
}
