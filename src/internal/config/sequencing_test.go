package config

import "testing"

// A workflow using no v2 field is still sequenced. Before this, the lowering
// pass exited early and such a workflow reached the engine with no dependency
// edges at all — every step immediately runnable, so they ran concurrently. An
// approval step gated nothing: the step after it could finish before the
// instance had even parked.
func TestFlatWorkflowIsSequenced(t *testing.T) {
	wf := WorkflowConfig{ID: "flat", Steps: []StepConfig{
		{ID: "gate", Type: StepTypeApproval, Message: "Deploy?"},
		{ID: "deploy", Agent: "engineer"},
		{ID: "notify", Agent: "engineer"},
	}}

	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Steps[0].SeqDependsOn) != 0 {
		t.Errorf("the first step should depend on nothing, got %v", out.Steps[0].SeqDependsOn)
	}
	for i, want := range map[int]string{1: "gate", 2: "deploy"} {
		got := out.Steps[i].SeqDependsOn
		if len(got) != 1 || got[0] != want {
			t.Errorf("step %q should follow %q, got %v", out.Steps[i].ID, want, got)
		}
	}
}

// Sequencing must not override an edge the step already carries, and running it
// twice must not change the result — LowerV2Workflow is documented idempotent.
func TestSequencingIsIdempotentAndPreservesExplicitEdges(t *testing.T) {
	wf := WorkflowConfig{ID: "flat", Steps: []StepConfig{
		{ID: "a", Agent: "engineer"},
		{ID: "b", Agent: "engineer", DependsOn: []string{"a"}},
		{ID: "c", Agent: "engineer", SeqDependsOn: []string{"a"}},
	}}

	once, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatal(err)
	}
	if len(once.Steps[1].SeqDependsOn) != 0 {
		t.Errorf("an explicit depends_on must not gain a seq edge, got %v", once.Steps[1].SeqDependsOn)
	}
	if got := once.Steps[2].SeqDependsOn; len(got) != 1 || got[0] != "a" {
		t.Errorf("an existing seq edge must be preserved, got %v", got)
	}

	twice, err := LowerV2Workflow(once)
	if err != nil {
		t.Fatal(err)
	}
	for i := range twice.Steps {
		a, b := once.Steps[i].SeqDependsOn, twice.Steps[i].SeqDependsOn
		if len(a) != len(b) || (len(a) == 1 && a[0] != b[0]) {
			t.Errorf("step %q changed on re-lowering: %v → %v", twice.Steps[i].ID, a, b)
		}
	}
}

// A v2 workflow keeps going through the full pass, which does its own chaining;
// sequencing must not double up or disturb it.
func TestV2WorkflowStillLowersNormally(t *testing.T) {
	wf := WorkflowConfig{ID: "v2", Steps: []StepConfig{
		{ID: "classify", Agent: "engineer"},
		{ID: "build", Agent: "engineer", If: "true"},
	}}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Steps[1].SeqDependsOn; len(got) != 1 || got[0] != "classify" {
		t.Fatalf("v2 chaining broke: %v", got)
	}
}
