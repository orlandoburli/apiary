package config

import "testing"

// TestLowerV2_GroupNodesUseSeqDependsOn is the lowering-side regression test for
// #379. Implicit sequencing between authored steps must always be expressed as
// SeqDependsOn, for every node kind — leaf, parallel and for_each alike.
//
// The DAG engine treats the two edge kinds differently on purpose (dag.go
// depsPassed): an explicit DependsOn requires the dependency to have *passed*,
// while a SeqDependsOn is also satisfied by a condition-skipped dependency. The
// lowering pass wired parallel and for_each nodes with a hard DependsOn, so a
// group authored right after a conditional step could never become runnable when
// that condition was false — it stayed pending forever, the scheduler went
// quiescent, and (with no step in a failed state) the instance reported success
// while its steps had silently never run.
func TestLowerV2_GroupNodesUseSeqDependsOn(t *testing.T) {
	wf := WorkflowConfig{ID: "seq-deps", Steps: []StepConfig{
		{ID: "plan", Agent: "ag",
			Output: &OutputSchema{Type: "object",
				Properties: map[string]SchemaField{
					"complexity": {Type: "string"},
					"tasks":      {Type: "array"},
				}}},
		{ID: "implement", Agent: "ag", If: `${{ plan.complexity == "high" }}`},
		{ID: "validate", Join: "all", ParallelSteps: []StepConfig{
			{ID: "review", Agent: "ag"},
			{ID: "qa", Agent: "ag"},
		}},
		{ID: "fan", ForEachExpr: "${{ plan.tasks }}", As: "task",
			SubSteps: []StepConfig{{ID: "work", Agent: "ag"}}},
	}}

	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("LowerV2Workflow: %v", err)
	}
	byID := map[string]StepConfig{}
	for _, s := range out.Steps {
		byID[s.ID] = s
	}

	for _, tc := range []struct{ step, want string }{
		{"implement", "plan"},
		{"validate", "implement"},
		{"fan", "validate"},
	} {
		s, ok := byID[tc.step]
		if !ok {
			t.Fatalf("step %q missing from lowered output", tc.step)
		}
		if len(s.DependsOn) != 0 {
			t.Errorf("step %q depends_on = %v, want none — a hard edge strands the step when %q is condition-skipped (#379)",
				tc.step, s.DependsOn, tc.want)
		}
		if len(s.SeqDependsOn) != 1 || s.SeqDependsOn[0] != tc.want {
			t.Errorf("step %q seq_depends_on = %v, want [%s]", tc.step, s.SeqDependsOn, tc.want)
		}
	}
}
