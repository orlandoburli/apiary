package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// condPairThenParallelWF builds the shape reported in #379: a plan step, a pair
// of mutually-exclusive conditional steps (only one of which ever runs), then a
// `parallel:` quality gate, then a trailing sequential step. Authored in v2 form
// and lowered exactly like a real config, so the test exercises the lowering
// pass and the DAG scheduler together.
func condPairThenParallelWF(t *testing.T) config.WorkflowConfig {
	t.Helper()
	wf := config.WorkflowConfig{ID: "cond-par", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect",
			Output: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{"complexity": {Type: "string"}}},
			Memory: &config.MemoryConfig{Write: []string{"complexity"}}},
		{ID: "implement", Agent: "backend-dev", If: `${{ memory.complexity != "high" }}`},
		{ID: "implement-hard", Agent: "backend-dev", If: `${{ memory.complexity == "high" }}`},
		{ID: "validate", Join: "all", ParallelSteps: []config.StepConfig{
			{ID: "review", Agent: "architect"},
			{ID: "qa-validate", Agent: "architect"},
		}},
		{ID: "ask-review", Agent: "architect"},
	}}
	lowered, err := config.LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("LowerV2Workflow: %v", err)
	}
	return lowered
}

// TestDAG_ParallelAfterConditionalPair_Runs is the regression test for #379: a
// `parallel:` group placed after a pair of mutually-exclusive conditional steps
// must run exactly once no matter which branch the condition took.
//
// Before the fix the lowering pass wired the parallel node with an explicit
// DependsOn on its predecessor. An explicit dep must be fully *passed*
// (dag.go depsPassed), and a condition-skipped step never reaches stPassed, so
// whenever the immediately-preceding conditional step was skipped the parallel
// node stayed pending forever: no worker ran it, skipUnreachable did not touch
// it (cond_skipped is not a cascade trigger), the scheduler went quiescent and,
// with no step in stFailed, the instance reported SUCCESS.
func TestDAG_ParallelAfterConditionalPair_Runs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		complexity string
		ranStep    string
		skipped    string
	}{
		{"low complexity skips implement-hard", "low", "implement", "implement-hard"},
		{"high complexity skips implement", "high", "implement-hard", "implement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := newSeqExecutor()
			exec.scripts["plan"] = []StepResult{
				{Success: true, StructuredOutput: map[string]any{"complexity": tc.complexity}},
			}
			eng := testEngine(baseCfg(), newFakeStore(), exec, &fakeSide{})

			_, success, err := eng.RunInstance(context.Background(), condPairThenParallelWF(t), model.InternalTask{ID: "c1"})
			if err != nil {
				t.Fatalf("RunInstance: %v", err)
			}
			if !success {
				t.Fatal("expected the instance to succeed")
			}
			if exec.ran(tc.ranStep) != 1 {
				t.Errorf("%s ran %d times, want 1", tc.ranStep, exec.ran(tc.ranStep))
			}
			if exec.ran(tc.skipped) != 0 {
				t.Errorf("%s ran %d times, want 0 (condition false)", tc.skipped, exec.ran(tc.skipped))
			}
			// The heart of #379: the quality gates must not be silently dropped.
			if exec.ran("review") != 1 {
				t.Errorf("parallel child review ran %d times, want 1 (#379: group never executed)", exec.ran("review"))
			}
			if exec.ran("qa-validate") != 1 {
				t.Errorf("parallel child qa-validate ran %d times, want 1 (#379: group never executed)", exec.ran("qa-validate"))
			}
			// And everything after the group must run too.
			if exec.ran("ask-review") != 1 {
				t.Errorf("ask-review ran %d times, want 1 (successor of the stranded group)", exec.ran("ask-review"))
			}
		})
	}
}

// TestDAG_ForeachAfterConditionalStep_Runs is the for_each twin of #379: the
// lowering pass wired foreach nodes with the same hard DependsOn edge, so a
// for_each placed after a condition-skipped step was stranded identically.
// The workflow is expressed in IR form (Type: foreach + Items) because the v2
// `for_each:` lowering emits a `steps.<id>.outputs.<field>` items path that the
// runtime rejects — a separate defect, not in scope here.
func TestDAG_ForeachAfterConditionalStep_Runs(t *testing.T) {
	wf := config.WorkflowConfig{ID: "cond-fe", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect",
			OutputSchema: &config.OutputSchema{Type: "object",
				Properties: map[string]config.SchemaField{
					"complexity": {Type: "string"},
					"tasks":      {Type: "array"},
				}},
			Memory: &config.MemoryConfig{Write: []string{"complexity", "tasks"}}},
		{ID: "implement-hard", Agent: "backend-dev", SeqDependsOn: []string{"plan"},
			Condition: `memory.complexity == "high"`},
		{ID: "fan", Type: config.StepTypeForeach, SeqDependsOn: []string{"implement-hard"},
			Items: "steps.plan.output.tasks", As: "task",
			Step: &config.StepConfig{ID: "work", Agent: "backend-dev"}},
	}}

	exec := newSeqExecutor()
	exec.scripts["plan"] = []StepResult{{Success: true, StructuredOutput: map[string]any{
		"complexity": "low",
		"tasks":      []any{"t1", "t2"},
	}}}
	eng := testEngine(baseCfg(), newFakeStore(), exec, &fakeSide{})

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected the instance to succeed")
	}
	// Item steps are dispatched as "<foreach id>[<index>]".
	if exec.ran("fan[0]") != 1 || exec.ran("fan[1]") != 1 {
		t.Errorf("foreach body runs = fan[0]:%d fan[1]:%d, want 1 each", exec.ran("fan[0]"), exec.ran("fan[1]"))
	}
}

// TestDAG_CondSkippedExplicitDep_CascadesToSkipped covers the recording half of
// #379: a step whose explicit depends_on was condition-skipped can never run
// (depsPassed demands a passed dependency), so it must be recorded as skipped
// rather than left pending. Before the fix it sat pending forever — invisible in
// the run, and indistinguishable from a step nobody had gotten to yet.
func TestDAG_CondSkippedExplicitDep_CascadesToSkipped(t *testing.T) {
	wf := config.WorkflowConfig{ID: "cond-explicit", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect"},
		{ID: "maybe", Agent: "backend-dev", DependsOn: []string{"plan"}, Condition: `cell.title == "never"`},
		{ID: "after", Agent: "backend-dev", DependsOn: []string{"maybe"}},
	}}

	exec := newSeqExecutor()
	eng := testEngine(baseCfg(), newFakeStore(), exec, &fakeSide{})
	r := eng.initDAG("wf-x", wf, model.InternalTask{ID: "c1"}, nil, nil, 0)
	outcome := eng.driveDAG(context.Background(), r)

	if outcome != outcomeDone {
		t.Fatalf("outcome = %v, want outcomeDone (an intentional condition skip is not a failure)", outcome)
	}
	if r.state["maybe"] != stCondSkipped {
		t.Errorf("maybe state = %q, want %q", r.state["maybe"], stCondSkipped)
	}
	if r.state["after"] != stSkipped {
		t.Errorf("after state = %q, want %q (must be recorded as skipped, not left pending)", r.state["after"], stSkipped)
	}
	if exec.ran("after") != 0 {
		t.Errorf("after ran %d times, want 0", exec.ran("after"))
	}
}

// TestDAG_StrandedStepFailsInstance covers the second half of #379: an instance
// that goes quiescent with a declared step still pending — neither run, nor
// condition-skipped, nor cascade-skipped — must NOT report success. That is the
// state #379 produced (the parallel group sat pending forever) and the engine
// happily called it done.
//
// The graph is driven directly with a dependency pre-seeded into a state that no
// cascade rule covers, which is the generic shape of the bug: this guard fires
// for any future wiring defect that makes a step unreachable, without relying on
// the specific lowering mistake that caused #379.
func TestDAG_StrandedStepFailsInstance(t *testing.T) {
	wf := config.WorkflowConfig{ID: "stranded", Steps: []config.StepConfig{
		{ID: "plan", Agent: "architect"},
		{ID: "after", Agent: "backend-dev", DependsOn: []string{"plan"}},
	}}

	exec := newSeqExecutor()
	eng := testEngine(baseCfg(), newFakeStore(), exec, &fakeSide{})
	r := eng.initDAG("wf-x", wf, model.InternalTask{ID: "c1"}, nil, nil, 0)
	// plan is neither runnable nor terminal: "after" can never become runnable.
	r.state["plan"] = stWaiting

	if outcome := eng.driveDAG(context.Background(), r); outcome != outcomeFailed {
		t.Fatalf("outcome = %v, want outcomeFailed: step %q never ran and was never skipped, reporting success hides dropped work (#379)",
			outcome, "after")
	}
	if got := r.strandedSteps(); len(got) != 1 || got[0] != "after" {
		t.Errorf("strandedSteps() = %v, want [after]", got)
	}
	if exec.ran("after") != 0 {
		t.Errorf("after ran %d times, want 0", exec.ran("after"))
	}
}
