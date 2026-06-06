package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

func TestRenderItemTemplate(t *testing.T) {
	item := map[string]any{"file": "auth.go", "desc": "null deref"}
	got := renderItemTemplate("Fix {{ issue.file }}: {{ issue.desc }}", "issue", item)
	if got != "Fix auth.go: null deref" {
		t.Errorf("got %q", got)
	}
	// scalar item with bare {{ as }}
	if g := renderItemTemplate("Value is {{ x }}", "x", "hello"); g != "Value is hello" {
		t.Errorf("scalar render got %q", g)
	}
	// unknown var left untouched
	if g := renderItemTemplate("{{ other }}", "issue", item); g != "{{ other }}" {
		t.Errorf("unknown var should be left as-is, got %q", g)
	}
	// missing field renders empty
	if g := renderItemTemplate("[{{ issue.missing }}]", "issue", item); g != "[]" {
		t.Errorf("missing field should render empty, got %q", g)
	}
}

// foreachWorkflow builds a workflow whose first step emits an items array and a
// foreach step over it.
func foreachWorkflow(foreach config.StepConfig) config.WorkflowConfig {
	plan := config.StepConfig{
		ID: "plan", Agent: "architect",
		OutputSchema: &config.OutputSchema{Type: "object",
			Properties: map[string]config.SchemaField{
				"issues": {Type: "array", Items: &config.SchemaField{Type: "object"}},
			}},
		Memory: &config.MemoryConfig{Write: []string{"issues"}},
	}
	foreach.ID = "fix-each"
	foreach.Type = config.StepTypeForeach
	foreach.DependsOn = []string{"plan"}
	foreach.Items = "steps.plan.output.issues"
	return config.WorkflowConfig{ID: "w", Steps: []config.StepConfig{plan, foreach}}
}

func planWithIssues(n int) StepResult {
	issues := make([]any, n)
	for i := 0; i < n; i++ {
		issues[i] = map[string]any{"file": "f" + itoa(i) + ".go"}
	}
	return StepResult{Success: true, StructuredOutput: map[string]any{"issues": issues}}
}

func TestForeach_RunsOnePerItem(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := newSeqExecutor()
	exec.scripts["plan"] = []StepResult{planWithIssues(3)}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := foreachWorkflow(config.StepConfig{
		As:   "issue",
		Step: &config.StepConfig{Agent: "backend-dev", Prompt: "Fix {{ issue.file }}"},
	})

	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if !success {
		t.Fatal("expected success")
	}

	// Three sub-runs, one per item, with rendered prompts.
	subRuns := 0
	for _, r := range exec.seen {
		if strings.HasPrefix(r, "fix-each[") {
			subRuns++
		}
	}
	if subRuns != 3 {
		t.Fatalf("expected 3 sub-runs, got %d (seen=%v)", subRuns, exec.seen)
	}
}

func TestForeach_RenderedPromptReachesExecutor(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	// capture executor records full StepRequest
	exec := &fakeExecutor{results: map[string]StepResult{"plan": planWithIssues(2)}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := foreachWorkflow(config.StepConfig{
		As:   "issue",
		Step: &config.StepConfig{Agent: "backend-dev", Prompt: "Fix {{ issue.file }}"},
	})
	_, _, _ = eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})

	var prompts []string
	for _, req := range exec.seen {
		if strings.HasPrefix(req.Step.ID, "fix-each[") {
			prompts = append(prompts, req.Prompt)
		}
	}
	if len(prompts) != 2 {
		t.Fatalf("expected 2 rendered prompts, got %v", prompts)
	}
	if prompts[0] != "Fix f0.go" || prompts[1] != "Fix f1.go" {
		t.Errorf("rendered prompts wrong: %v", prompts)
	}
}

func TestForeach_MaxItemsGuard(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"plan": planWithIssues(10)}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := foreachWorkflow(config.StepConfig{
		As:       "issue",
		MaxItems: 5,
		Step:     &config.StepConfig{Agent: "backend-dev"},
	})
	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("expected failure when items exceed max_items")
	}
	// No sub-runs should have executed.
	for _, req := range exec.seen {
		if strings.HasPrefix(req.Step.ID, "fix-each[") {
			t.Errorf("no sub-runs expected, but %q ran", req.Step.ID)
		}
	}
}

func TestForeach_FailFast(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := newSeqExecutor()
	exec.scripts["plan"] = []StepResult{planWithIssues(4)}
	// every sub-run fails
	exec.scripts["fix-each[0]"] = []StepResult{{Success: false}}
	exec.scripts["fix-each[1]"] = []StepResult{{Success: false}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := foreachWorkflow(config.StepConfig{
		As:       "issue",
		FailFast: true,
		Step:     &config.StepConfig{Agent: "backend-dev"},
	})
	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("expected failure")
	}
	// fail_fast: stop after the first failing item → only 1 sub-run.
	subRuns := 0
	for _, id := range exec.seen {
		if strings.HasPrefix(id, "fix-each[") {
			subRuns++
		}
	}
	if subRuns != 1 {
		t.Errorf("expected 1 sub-run with fail_fast, got %d", subRuns)
	}
}

func TestForeach_AllPassDownstreamRuns(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{"plan": planWithIssues(2)}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := foreachWorkflow(config.StepConfig{
		As:   "issue",
		Step: &config.StepConfig{Agent: "backend-dev"},
	})
	// Add a downstream step depending on the foreach.
	wf.Steps = append(wf.Steps, config.StepConfig{
		ID: "summarize", Agent: "architect", DependsOn: []string{"fix-each"},
	})

	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if !success {
		t.Fatal("expected success")
	}
	ran := false
	for _, req := range exec.seen {
		if req.Step.ID == "summarize" {
			ran = true
		}
	}
	if !ran {
		t.Error("expected summarize to run after foreach passed")
	}
}

func TestForeach_ConcurrentItems(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Concurrency = 4
	store := newFakeStore()
	exec := newConcurrentExecutor() // records start order
	exec.results["plan"] = planWithIssues(4)
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := foreachWorkflow(config.StepConfig{
		As:          "issue",
		Concurrency: 2, // run at most 2 items at a time
		Step:        &config.StepConfig{Agent: "backend-dev", Prompt: "Fix {{ issue.file }}"},
	})

	_, success, err := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success")
	}

	// All 4 items must have run.
	subRuns := 0
	exec.mu.Lock()
	for _, id := range exec.started {
		if strings.HasPrefix(id, "fix-each[") {
			subRuns++
		}
	}
	exec.mu.Unlock()
	if subRuns != 4 {
		t.Errorf("expected 4 sub-runs, got %d", subRuns)
	}
}

func TestForeach_ConcurrentFailFast(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Concurrency = 4
	store := newFakeStore()
	exec := newConcurrentExecutor()
	exec.results["plan"] = planWithIssues(6)
	exec.results["fix-each[0]"] = StepResult{Success: false}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := foreachWorkflow(config.StepConfig{
		As:          "issue",
		Concurrency: 3,
		FailFast:    true,
		Step:        &config.StepConfig{Agent: "backend-dev"},
	})

	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("expected failure (item 0 failed with fail_fast)")
	}
}

func TestForeach_InvalidItemsPathFails(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"plan": {Success: true, StructuredOutput: map[string]any{"notarray": "x"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := foreachWorkflow(config.StepConfig{As: "i", Step: &config.StepConfig{Agent: "backend-dev"}})
	wf.Steps[1].Items = "steps.plan.output.notarray" // not an array
	_, success, _ := eng.RunInstance(context.Background(), wf, model.InternalTask{ID: "c1"})
	if success {
		t.Fatal("expected failure when items path is not an array")
	}
}
