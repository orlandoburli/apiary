package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// TestEngine_RendersPromptTemplates is the #526 repro: input/event/memory/
// steps/cell placeholders in a step prompt (and summary_prompt) reach the
// executor rendered, using the same scope an `if:` condition sees.
func TestEngine_RendersPromptTemplates(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"plan": {Success: true, Output: "plan-output", StructuredOutput: map[string]any{"verdict": "approved"}},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{
		ID: "pr-review",
		Steps: []config.StepConfig{
			{
				ID:    "plan",
				Agent: "architect",
				Memory: &config.MemoryConfig{
					Write: []string{"verdict"},
				},
			},
			{
				ID:            "review",
				Agent:         "backend-dev",
				DependsOn:     []string{"plan"},
				Prompt:        "dry=${{ input.dry_run }} url=${{ event.pr_url }} verdict=${{ memory.verdict }} title=${{ cell.title }} state=${{ steps.plan.state }} out=${{ steps.plan.output }}",
				SummaryPrompt: "summary dry=${{ input.dry_run }}",
			},
		},
	}
	task := model.InternalTask{
		ID:    "C1",
		Title: "Review PR 78",
		Input: map[string]any{"dry_run": "no", "pr_url": "https://example.test/pull/78"},
	}
	event := map[string]string{"pr_url": "https://example.test/pull/78"}

	if _, _, err := eng.RunInstanceForEvent(context.Background(), wf, task, event); err != nil {
		t.Fatalf("RunInstanceForEvent: %v", err)
	}

	req := findStepRequest(t, exec, "review")
	want := "dry=no url=https://example.test/pull/78 verdict=approved title=Review PR 78 state=passed out=plan-output"
	if req.Prompt != want {
		t.Errorf("rendered prompt = %q, want %q", req.Prompt, want)
	}
	if req.Step.SummaryPrompt != "summary dry=no" {
		t.Errorf("rendered summary_prompt = %q, want %q", req.Step.SummaryPrompt, "summary dry=no")
	}
	if req.Inputs["dry_run"] != "no" {
		t.Errorf("StepRequest.Inputs[dry_run] = %q, want no", req.Inputs["dry_run"])
	}
}

// TestEngine_PromptTemplateLeavesUnresolvedPlaceholders verifies the rendering
// is non-destructive: an unknown path or root stays visible instead of blanking.
func TestEngine_PromptTemplateLeavesUnresolvedPlaceholders(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{
		ID: "render",
		Steps: []config.StepConfig{{
			ID:     "run",
			Agent:  "backend-dev",
			Prompt: "a=${{ input.missing }} b=${{ bogus.field }} c=${{ memory.x == \"y\" }} d=$LOG",
		}},
	}
	task := model.InternalTask{ID: "C1", Input: map[string]any{"present": "yes"}}

	if _, _, err := eng.RunInstance(context.Background(), wf, task); err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	req := findStepRequest(t, exec, "run")
	if req.Prompt != `a=${{ input.missing }} b=${{ bogus.field }} c=${{ memory.x == "y" }} d=$LOG` {
		t.Errorf("unresolved placeholders were mangled: %q", req.Prompt)
	}
}

// TestRenderPromptTemplate covers the renderer in isolation, including list and
// number rendering.
func TestRenderPromptTemplate(t *testing.T) {
	ctx := EvalContext{
		Cell:   model.SourceItem{Title: "T", Labels: []string{"a", "b"}},
		Memory: map[string]string{"verdict": "ok"},
		Steps:  map[string]StepState{"tests": {State: "passed", ExitCode: 3}},
		Event:  map[string]string{"author": "alice"},
		Input:  map[string]string{"dry_run": "no"},
		Task:   model.InternalTask{ID: "C1", Title: "Task title"},
	}
	got := renderPromptTemplate(
		"${{ input.dry_run }} ${{ event.author }} ${{ memory.verdict }} ${{ cell.title }} ${{ cell.labels }} ${{ steps.tests.state }} ${{ steps.tests.exit_code }} ${{ task.title }}",
		ctx,
	)
	want := "no alice ok T a, b passed 3 Task title"
	if got != want {
		t.Errorf("renderPromptTemplate = %q, want %q", got, want)
	}
}

// TestEngine_ConditionAndPromptShareInputScope verifies the new input root is
// available to `if:` conditions exactly as it is to prompt templates.
func TestEngine_ConditionAndPromptShareInputScope(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dryRun  string
		ranStep bool
	}{
		{"condition true", "no", true},
		{"condition false", "yes", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseCfg()
			store := newFakeStore()
			exec := &fakeExecutor{results: map[string]StepResult{}}
			eng := testEngine(cfg, store, exec, &fakeSide{})

			wf := config.WorkflowConfig{
				ID: "post",
				Steps: []config.StepConfig{{
					ID:        "post",
					Agent:     "backend-dev",
					Condition: `input.dry_run == "no"`,
					Prompt:    "dry=${{ input.dry_run }}",
				}},
			}
			task := model.InternalTask{ID: "C1", Input: map[string]any{"dry_run": tc.dryRun}}
			if _, _, err := eng.RunInstance(context.Background(), wf, task); err != nil {
				t.Fatalf("RunInstance: %v", err)
			}

			exec.mu.Lock()
			ran := len(exec.seen) > 0
			exec.mu.Unlock()
			if ran != tc.ranStep {
				t.Fatalf("step ran = %v, want %v (condition should read input.dry_run)", ran, tc.ranStep)
			}
			if tc.ranStep {
				if req := findStepRequest(t, exec, "post"); req.Prompt != "dry="+tc.dryRun {
					t.Errorf("prompt = %q, want dry=%s", req.Prompt, tc.dryRun)
				}
			}
		})
	}
}

// TestEngine_RendersPromptInParallelChild verifies dispatch-time rendering
// reaches children that run on separate worker goroutines (they never touch the
// scheduler-owned step-state map themselves).
func TestEngine_RendersPromptInParallelChild(t *testing.T) {
	cfg := baseCfg()
	store := newFakeStore()
	exec := &fakeExecutor{results: map[string]StepResult{
		"plan": {Success: true, Output: "planned"},
	}}
	eng := testEngine(cfg, store, exec, &fakeSide{})

	wf := config.WorkflowConfig{
		ID: "parallel-render",
		Steps: []config.StepConfig{
			{ID: "plan", Agent: "architect"},
			{
				ID:        "fan",
				Type:      config.StepTypeParallel,
				DependsOn: []string{"plan"},
				SubSteps: []config.StepConfig{{
					ID:     "one",
					Agent:  "backend-dev",
					Prompt: "plan=${{ steps.plan.output }} dry=${{ input.dry_run }}",
				}},
			},
		},
	}
	task := model.InternalTask{ID: "C1", Input: map[string]any{"dry_run": "no"}}

	if _, _, err := eng.RunInstance(context.Background(), wf, task); err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	req := findStepRequest(t, exec, "one")
	if req.Prompt != "plan=planned dry=no" {
		t.Errorf("parallel child prompt = %q, want %q", req.Prompt, "plan=planned dry=no")
	}
}

func findStepRequest(t *testing.T, exec *fakeExecutor, stepID string) StepRequest {
	t.Helper()
	exec.mu.Lock()
	defer exec.mu.Unlock()
	for _, req := range exec.seen {
		if req.Step.ID == stepID {
			return req
		}
	}
	t.Fatalf("no StepRequest recorded for step %q (seen %s)", stepID, seenStepIDs(exec.seen))
	return StepRequest{}
}

func seenStepIDs(reqs []StepRequest) string {
	ids := make([]string, len(reqs))
	for i, r := range reqs {
		ids[i] = r.Step.ID
	}
	return strings.Join(ids, ",")
}
