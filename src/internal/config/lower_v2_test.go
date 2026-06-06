package config

import (
	"testing"
)

func ptr(b bool) *bool { return &b }

func TestLowerV2_NoV2Fields_Passthrough(t *testing.T) {
	wf := WorkflowConfig{
		ID: "plain",
		Steps: []StepConfig{
			{ID: "a", Agent: "ag", DependsOn: []string{}},
			{ID: "b", Agent: "ag", DependsOn: []string{"a"}},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Steps) != 2 || out.Steps[0].ID != "a" || out.Steps[1].ID != "b" {
		t.Errorf("passthrough changed steps: %+v", out.Steps)
	}
}

func TestLowerV2_ImplicitSequencing(t *testing.T) {
	// Steps without depends_on get implicit sequencing from declaration order.
	wf := WorkflowConfig{
		ID: "seq",
		Steps: []StepConfig{
			{ID: "a", Agent: "ag"},
			{ID: "b", Agent: "ag"},
			{ID: "c", Agent: "ag"},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Without any v2 fields, passthrough — no lowering applied.
	// Steps without any v2 fields: LowerV2 passes through unchanged.
	if len(out.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(out.Steps))
	}
}

func TestLowerV2_ImplicitSequencing_V2Fields(t *testing.T) {
	// When any step has v2 fields, lowering runs and adds implicit depends_on.
	wf := WorkflowConfig{
		ID: "seq",
		Steps: []StepConfig{
			{ID: "a", Agent: "ag"},
			{ID: "b", Agent: "ag", If: `${{ cell.priority == "high" }}`},
			{ID: "c", Agent: "ag"},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(out.Steps))
	}
	// a: no prev
	if len(out.Steps[0].DependsOn) != 0 || len(out.Steps[0].SeqDependsOn) != 0 {
		t.Errorf("step a should have no depends_on/seq_depends_on, got %v / %v",
			out.Steps[0].DependsOn, out.Steps[0].SeqDependsOn)
	}
	// b: implicit seq dep on a (via SeqDependsOn, not DependsOn)
	if len(out.Steps[1].DependsOn) != 0 {
		t.Errorf("step b should have no explicit depends_on, got %v", out.Steps[1].DependsOn)
	}
	if len(out.Steps[1].SeqDependsOn) != 1 || out.Steps[1].SeqDependsOn[0] != "a" {
		t.Errorf("step b seq_depends_on = %v, want [a]", out.Steps[1].SeqDependsOn)
	}
	// c: implicit seq dep on b
	if len(out.Steps[2].DependsOn) != 0 {
		t.Errorf("step c should have no explicit depends_on, got %v", out.Steps[2].DependsOn)
	}
	if len(out.Steps[2].SeqDependsOn) != 1 || out.Steps[2].SeqDependsOn[0] != "b" {
		t.Errorf("step c seq_depends_on = %v, want [b]", out.Steps[2].SeqDependsOn)
	}
}

func TestLowerV2_IfLowersToCondition(t *testing.T) {
	wf := WorkflowConfig{
		ID: "cond",
		Steps: []StepConfig{
			{
				ID: "implement", Agent: "ag",
				If: `${{ memory.track == "implement" }}`,
			},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := out.Steps[0]
	if step.Condition != `memory.track == "implement"` {
		t.Errorf("condition = %q, want stripped expression", step.Condition)
	}
	if step.If != "" {
		t.Errorf("If should be cleared after lowering, got %q", step.If)
	}
}

func TestLowerV2_OutputAliasLowersToOutputSchema(t *testing.T) {
	schema := &OutputSchema{Type: "object", Properties: map[string]SchemaField{
		"verdict": {Type: "string"},
	}}
	wf := WorkflowConfig{
		ID: "out",
		Steps: []StepConfig{
			{ID: "review", Agent: "ag", Output: schema},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := out.Steps[0]
	if step.OutputSchema == nil {
		t.Error("OutputSchema should be set from Output alias")
	}
	if step.Output != nil {
		t.Error("Output alias should be cleared after lowering")
	}
}

func TestLowerV2_RejectWhenLowersToFailWhen(t *testing.T) {
	wf := WorkflowConfig{
		ID: "rw",
		Steps: []StepConfig{
			{
				ID: "review", Agent: "ag",
				Output: &OutputSchema{Type: "object",
					Properties: map[string]SchemaField{"verdict": {Type: "string"}}},
				RejectWhen: `${{ review.verdict == "rejected" }}`,
				OnReject:   &OnRejectConfig{RestartFrom: "implement", Max: 3},
			},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := out.Steps[0]
	if step.FailWhen == "" {
		t.Error("FailWhen should be set from RejectWhen")
	}
	if step.RejectWhen != "" {
		t.Errorf("RejectWhen should be cleared, got %q", step.RejectWhen)
	}
	if step.OnFail == nil {
		t.Fatal("OnFail should be set from OnReject")
	}
	if step.OnFail.Goto != "implement" {
		t.Errorf("OnFail.Goto = %q, want implement", step.OnFail.Goto)
	}
	if step.OnFail.MaxRetries != 3 {
		t.Errorf("OnFail.MaxRetries = %d, want 3", step.OnFail.MaxRetries)
	}
	if step.OnReject != nil {
		t.Error("OnReject should be cleared after lowering")
	}
}

func TestLowerV2_GroupDissolvedInline(t *testing.T) {
	// A group step with sub-steps is dissolved; children are inlined.
	wf := WorkflowConfig{
		ID: "grp",
		Steps: []StepConfig{
			{ID: "classify", Agent: "ag",
				Output: &OutputSchema{Type: "object",
					Properties: map[string]SchemaField{"track": {Type: "string"}}}},
			{
				ID: "impl-track",
				If: `${{ classify.track == "implement" }}`,
				SubSteps: []StepConfig{
					{ID: "implement", Agent: "ag"},
					{ID: "review", Agent: "ag"},
				},
			},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Group is dissolved: classify + implement + review = 3 flat steps.
	if len(out.Steps) != 3 {
		t.Fatalf("expected 3 flat steps (group dissolved), got %d: %v",
			len(out.Steps), stepIDList(out.Steps))
	}
	ids := stepIDList(out.Steps)
	if ids[0] != "classify" || ids[1] != "implement" || ids[2] != "review" {
		t.Errorf("expected [classify, implement, review], got %v", ids)
	}
	// implement inherits the group's if: condition.
	if out.Steps[1].Condition == "" {
		t.Error("implement should inherit the group's condition")
	}
	// implement has implicit seq dep on classify (group's predecessor).
	if len(out.Steps[1].SeqDependsOn) == 0 || out.Steps[1].SeqDependsOn[0] != "classify" {
		t.Errorf("implement seq_depends_on = %v, want [classify]", out.Steps[1].SeqDependsOn)
	}
	// review has implicit seq dep on implement (within group).
	if len(out.Steps[2].SeqDependsOn) == 0 || out.Steps[2].SeqDependsOn[0] != "implement" {
		t.Errorf("review seq_depends_on = %v, want [implement]", out.Steps[2].SeqDependsOn)
	}
}

func TestLowerV2_ParallelStepKept(t *testing.T) {
	wf := WorkflowConfig{
		ID: "par",
		Steps: []StepConfig{
			{ID: "setup", Agent: "ag"},
			{
				ID:   "checks",
				Join: "all",
				ParallelSteps: []StepConfig{
					{ID: "tests", Agent: "ag"},
					{ID: "docs", Agent: "ag"},
				},
			},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// setup + checks = 2 flat steps (parallel node kept, not dissolved).
	if len(out.Steps) != 2 {
		t.Fatalf("expected 2 flat steps, got %d: %v", len(out.Steps), stepIDList(out.Steps))
	}
	checks := out.Steps[1]
	if checks.Type != StepTypeParallel {
		t.Errorf("checks type = %q, want parallel", checks.Type)
	}
	if checks.Join != "all" {
		t.Errorf("checks join = %q, want all", checks.Join)
	}
	if len(checks.SubSteps) != 2 {
		t.Errorf("checks should have 2 children, got %d", len(checks.SubSteps))
	}
	if checks.DependsOn[0] != "setup" {
		t.Errorf("checks depends_on = %v, want [setup]", checks.DependsOn)
	}
}

func TestLowerV2_ForEachSingleStepBody(t *testing.T) {
	wf := WorkflowConfig{
		ID: "fe",
		Steps: []StepConfig{
			{
				ID: "design", Agent: "ag",
				Output: &OutputSchema{Type: "object",
					Properties: map[string]SchemaField{"tasks": {Type: "array"}}},
			},
			{
				ID:          "build",
				ForEachExpr: "${{ design.tasks }}",
				As:          "task",
				Max:         20,
				SubSteps: []StepConfig{
					{ID: "impl", Agent: "ag", Prompt: "Implement: ${{ task.title }}"},
				},
			},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(out.Steps))
	}
	build := out.Steps[1]
	if build.Type != StepTypeForeach {
		t.Errorf("build type = %q, want foreach", build.Type)
	}
	if build.MaxItems != 20 {
		t.Errorf("build max_items = %d, want 20", build.MaxItems)
	}
	if build.As != "task" {
		t.Errorf("build as = %q, want task", build.As)
	}
	if build.Step == nil {
		t.Fatal("build.Step should be set for single-step body")
	}
	if build.Step.ID != "impl" {
		t.Errorf("inner step id = %q, want impl", build.Step.ID)
	}
}

func TestLowerV2_ForEachMultiStepBodyWrapsAnon(t *testing.T) {
	wf := WorkflowConfig{
		ID: "fe2",
		Steps: []StepConfig{
			{
				ID: "design", Agent: "ag",
				Output: &OutputSchema{Type: "object",
					Properties: map[string]SchemaField{"tasks": {Type: "array"}}},
			},
			{
				ID:          "build",
				ForEachExpr: "${{ design.tasks }}",
				As:          "task",
				SubSteps: []StepConfig{
					{ID: "impl", Agent: "ag"},
					{ID: "verify", Agent: "ag"},
				},
			},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	build := out.Steps[1]
	if build.Step == nil {
		t.Fatal("build.Step should be set for multi-step body (anon sub-workflow)")
	}
	if build.Step.Type != StepTypeWorkflow {
		t.Errorf("anon wrapper type = %q, want workflow", build.Step.Type)
	}
	if len(build.Step.SubSteps) != 2 {
		t.Errorf("anon wrapper has %d children, want 2", len(build.Step.SubSteps))
	}
}

func TestLowerV2_AutoWireMemoryFromRejectWhen(t *testing.T) {
	wf := WorkflowConfig{
		ID: "aw",
		Steps: []StepConfig{
			{
				ID: "review", Agent: "ag",
				Output: &OutputSchema{Type: "object",
					Properties: map[string]SchemaField{"verdict": {Type: "string"}}},
				RejectWhen: `${{ review.verdict == "rejected" }}`,
				OnReject:   &OnRejectConfig{RestartFrom: "implement", Max: 2},
			},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The fail_when expression should use memory.verdict (after rewrite).
	if out.Steps[0].FailWhen != `memory.verdict == "rejected"` {
		t.Errorf("FailWhen = %q, want memory.verdict == \"rejected\"", out.Steps[0].FailWhen)
	}
}

func TestLowerV2_ComposeCond(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", ""},
		{"A", "", "A"},
		{"", "B", "B"},
		{"A", "B", "(A) and (B)"},
	}
	for _, c := range cases {
		got := composeCond(c.a, c.b)
		if got != c.want {
			t.Errorf("composeCond(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestLowerV2_LowerExpr(t *testing.T) {
	cases := []struct{ in, want string }{
		{`${{ memory.track == "implement" }}`, `memory.track == "implement"`},
		{`memory.track == "implement"`, `memory.track == "implement"`},
		{`${{verdict == "ok"}}`, `verdict == "ok"`},
	}
	for _, c := range cases {
		got := lowerExpr(c.in)
		if got != c.want {
			t.Errorf("lowerExpr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func stepIDList(steps []StepConfig) []string {
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = s.ID
	}
	return ids
}
