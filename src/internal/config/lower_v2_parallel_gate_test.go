package config

import (
	"strings"
	"testing"
)

// TestLowerV2_ParallelRejectWhenLowersToFailWhen guards the regression behind
// project-erp #4140: reject_when/on_reject authored on a parallel: group step
// were silently dropped by lowerParallelStep, so the gate never fired and the
// workflow proceeded past a rejected verdict.
func TestLowerV2_ParallelRejectWhenLowersToFailWhen(t *testing.T) {
	wf := WorkflowConfig{
		ID: "gate",
		Steps: []StepConfig{
			{ID: "implement", Agent: "ag"},
			{
				ID:   "gate",
				Join: "all",
				ParallelSteps: []StepConfig{
					{ID: "review", Agent: "ag",
						Output: &OutputSchema{Type: "object",
							Properties: map[string]SchemaField{"review_verdict": {Type: "string"}}}},
					{ID: "validate", Agent: "ag",
						Output: &OutputSchema{Type: "object",
							Properties: map[string]SchemaField{"qa_verdict": {Type: "string"}}}},
				},
				RejectWhen: `${{ memory.review_verdict == "rejected" or memory.qa_verdict == "rejected" }}`,
				OnReject:   &OnRejectConfig{RestartFrom: "implement", Max: 3},
			},
			{ID: "merge", Agent: "ag"},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gate := out.Steps[1]
	if gate.Type != StepTypeParallel {
		t.Fatalf("gate type = %q, want parallel", gate.Type)
	}
	if gate.FailWhen == "" {
		t.Fatal("FailWhen should be set from RejectWhen on a parallel step")
	}
	if !strings.Contains(gate.FailWhen, "memory.qa_verdict") {
		t.Errorf("FailWhen = %q, want memory.qa_verdict reference preserved", gate.FailWhen)
	}
	if gate.OnFail == nil {
		t.Fatal("OnFail should be set from OnReject on a parallel step")
	}
	if gate.OnFail.Goto != "implement" || gate.OnFail.MaxRetries != 3 {
		t.Errorf("OnFail = %+v, want goto implement, max_retries 3", gate.OnFail)
	}
	// Auto-wiring: the referenced fields must land in the children's memory.write
	// so their outputs actually reach workflow memory.
	byID := map[string]StepConfig{}
	for _, c := range gate.SubSteps {
		byID[c.ID] = c
	}
	if rv := byID["review"]; rv.Memory == nil || !memoryWrites(rv.Memory, "review_verdict") {
		t.Errorf("review child should auto-wire memory.write review_verdict, got %+v", rv.Memory)
	}
	if qa := byID["validate"]; qa.Memory == nil || !memoryWrites(qa.Memory, "qa_verdict") {
		t.Errorf("validate child should auto-wire memory.write qa_verdict, got %+v", qa.Memory)
	}
}

// TestLowerV2_ForEachRejectWhenLowersToFailWhen: same silent drop existed on
// for_each parents.
func TestLowerV2_ForEachRejectWhenLowersToFailWhen(t *testing.T) {
	wf := WorkflowConfig{
		ID: "fe",
		Steps: []StepConfig{
			{ID: "design", Agent: "ag",
				Output: &OutputSchema{Type: "object",
					Properties: map[string]SchemaField{"tasks": {Type: "array"}}}},
			{
				ID:          "fanout",
				ForEachExpr: "${{ design.tasks }}",
				SubSteps:    []StepConfig{{ID: "worker", Agent: "ag"}},
				RejectWhen:  `${{ memory.tasks == "" }}`,
				OnReject:    &OnRejectConfig{RestartFrom: "design", Max: 1},
			},
		},
	}
	out, err := LowerV2Workflow(wf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fanout := out.Steps[1]
	if fanout.FailWhen == "" {
		t.Error("FailWhen should be set from RejectWhen on a for_each step")
	}
	if fanout.OnFail == nil || fanout.OnFail.Goto != "design" || fanout.OnFail.MaxRetries != 1 {
		t.Errorf("OnFail = %+v, want goto design, max_retries 1", fanout.OnFail)
	}
}

// TestValidateV2_GateOnSequentialGroupRejected: a gate on a plain sequential
// group has no emitted step to live on (the group dissolves during lowering),
// so validation must fail loud instead of dropping it silently.
func TestValidateV2_GateOnSequentialGroupRejected(t *testing.T) {
	wf := WorkflowConfig{
		ID: "grp",
		Steps: []StepConfig{
			{ID: "implement", Agent: "ag"},
			{
				ID: "checks",
				SubSteps: []StepConfig{
					{ID: "review", Agent: "ag"},
					{ID: "validate", Agent: "ag"},
				},
				RejectWhen: `${{ memory.verdict == "rejected" }}`,
				OnReject:   &OnRejectConfig{RestartFrom: "implement", Max: 3},
			},
		},
	}
	errs := validateV2Workflow("wf", wf)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "sequential group") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a sequential-group gate validation error, got %v", errs)
	}
}
