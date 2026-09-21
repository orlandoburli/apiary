package workflow

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// memDocExecutor wraps a seqExecutor and records the memory document each step
// invocation received, in call order per step id.
type memDocExecutor struct {
	*seqExecutor
	mu   sync.Mutex
	docs map[string][]string
}

func newMemDocExecutor() *memDocExecutor {
	return &memDocExecutor{seqExecutor: newSeqExecutor(), docs: map[string][]string{}}
}

func (m *memDocExecutor) ExecuteStep(ctx context.Context, req StepRequest) StepResult {
	m.mu.Lock()
	m.docs[req.Step.ID] = append(m.docs[req.Step.ID], req.MemoryDoc)
	m.mu.Unlock()
	return m.seqExecutor.ExecuteStep(ctx, req)
}

func (m *memDocExecutor) doc(stepID string, call int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if call >= len(m.docs[stepID]) {
		return ""
	}
	return m.docs[stepID][call]
}

// feedbackGateWF is parallelGateWF with a qa_reason the loop target must see.
func feedbackGateWF() config.WorkflowConfig {
	wf := parallelGateWF(3)
	validate := &wf.Steps[1].SubSteps[1]
	validate.OutputSchema.Properties["qa_reason"] = config.SchemaField{Type: "string"}
	validate.Memory.Write = []string{"qa_verdict", "qa_reason"}
	return wf
}

// TestDAG_ParallelGateRejection_ReasonReachesLoopTarget is the project-erp
// #5581 regression: QA rejected twice, the gate looped back to implement both
// times, and implement received a prompt identical to its first run — the
// rejection reason never reached it, so it merged the PR instead of fixing it.
func TestDAG_ParallelGateRejection_ReasonReachesLoopTarget(t *testing.T) {
	exec := newMemDocExecutor()
	exec.scripts["review"] = []StepResult{
		{Success: true, StructuredOutput: map[string]any{"review_verdict": "approved"}},
	}
	exec.scripts["validate"] = []StepResult{
		{Success: true, StructuredOutput: map[string]any{"qa_verdict": "rejected", "qa_reason": "missing E2E test"}},
		{Success: true, StructuredOutput: map[string]any{"qa_verdict": "approved", "qa_reason": "all good"}},
	}
	eng := testEngine(baseCfg(), newFakeStore(), exec, &fakeSide{})

	_, success, err := eng.RunInstance(context.Background(), feedbackGateWF(), model.InternalTask{ID: "c1"})
	if err != nil || !success {
		t.Fatalf("RunInstance: success=%v err=%v", success, err)
	}
	if exec.ran("implement") != 2 {
		t.Fatalf("expected implement to run twice, got %d", exec.ran("implement"))
	}

	if first := exec.doc("implement", 0); strings.Contains(first, "qa_reason") {
		t.Errorf("first implement run must not see any qa_reason, got:\n%s", first)
	}
	loopBack := exec.doc("implement", 1)
	for _, want := range []string{"qa_verdict: rejected", "qa_reason: missing E2E test", "review_verdict: approved"} {
		if !strings.Contains(loopBack, want) {
			t.Errorf("loop-back implement memory is missing %q, got:\n%s", want, loopBack)
		}
	}
}

// TestDAG_ParallelGatePass_ChildWritesReachLaterSteps verifies the children's
// memory.write fields are visible downstream once the group passes, and that a
// stale rejection from an earlier attempt does not linger after the pass.
func TestDAG_ParallelGatePass_ChildWritesReachLaterSteps(t *testing.T) {
	exec := newMemDocExecutor()
	exec.scripts["review"] = []StepResult{
		{Success: true, StructuredOutput: map[string]any{"review_verdict": "approved"}},
	}
	exec.scripts["validate"] = []StepResult{
		{Success: true, StructuredOutput: map[string]any{"qa_verdict": "rejected", "qa_reason": "missing E2E test"}},
		{Success: true, StructuredOutput: map[string]any{"qa_verdict": "approved", "qa_reason": "all good"}},
	}
	eng := testEngine(baseCfg(), newFakeStore(), exec, &fakeSide{})

	if _, success, err := eng.RunInstance(context.Background(), feedbackGateWF(), model.InternalTask{ID: "c1"}); err != nil || !success {
		t.Fatalf("RunInstance: success=%v err=%v", success, err)
	}

	merge := exec.doc("merge", 0)
	for _, want := range []string{"review_verdict: approved", "qa_verdict: approved", "qa_reason: all good"} {
		if !strings.Contains(merge, want) {
			t.Errorf("merge memory is missing %q, got:\n%s", want, merge)
		}
	}
	if strings.Contains(merge, "missing E2E test") || strings.Contains(merge, "qa_verdict: rejected") {
		t.Errorf("merge memory still carries the stale rejection, got:\n%s", merge)
	}
}

// TestDAG_LeafRejection_ReasonReachesLoopTarget covers the same gap for a plain
// (non-parallel) step with a rejection gate: a failed step never entered the
// memory document, so a sequential review's reason was lost on loop-back too.
func TestDAG_LeafRejection_ReasonReachesLoopTarget(t *testing.T) {
	wf := config.WorkflowConfig{ID: "leaf-gate", Steps: []config.StepConfig{
		{ID: "implement", Agent: "backend-dev"},
		{ID: "review", Agent: "architect", DependsOn: []string{"implement"},
			OutputSchema: &config.OutputSchema{Type: "object", Properties: map[string]config.SchemaField{
				"review_verdict": {Type: "string"}, "review_reason": {Type: "string"}}},
			Memory:   &config.MemoryConfig{Write: []string{"review_verdict", "review_reason"}},
			FailWhen: `memory.review_verdict == "rejected"`,
			OnFail:   &config.StepOutcome{Goto: "implement", MaxRetries: 2}},
	}}
	exec := newMemDocExecutor()
	exec.scripts["review"] = []StepResult{
		{Success: true, StructuredOutput: map[string]any{"review_verdict": "rejected", "review_reason": "handler runs raw SQL"}},
		{Success: true, StructuredOutput: map[string]any{"review_verdict": "approved", "review_reason": "lgtm"}},
	}
	eng := testEngine(baseCfg(), newFakeStore(), exec, &fakeSide{})

	if _, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"}); err != nil || !success {
		t.Fatalf("RunInstance: success=%v err=%v", success, err)
	}
	loopBack := exec.doc("implement", 1)
	if !strings.Contains(loopBack, "review_reason: handler runs raw SQL") {
		t.Errorf("loop-back implement memory is missing the review reason, got:\n%s", loopBack)
	}
}
