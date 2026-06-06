package workflow

import (
	"context"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
)

// TestE2E_ClassifyImplementReviewQA runs the full pipeline that the v2 authoring
// spec describes as the canonical example:
//
//	classify → implement → review (reject→loop) → qa → done
//
// The workflow is authored in v2 (if/reject_when/on_reject) and lowered to DAG
// IR before execution. The review step rejects once (looping back to implement)
// then passes, after which qa runs and the workflow completes.
func TestE2E_ClassifyImplementReviewQA(t *testing.T) {
	ctx := context.Background()
	client := realDB(t)

	// seqExec is scripted: review fails on attempt 1, passes on attempt 2.
	exec := newSeqExecutor()
	exec.scripts["classify"] = []StepResult{
		{
			Success:          true,
			Output:           "track=implement",
			StructuredOutput: map[string]any{"track": "implement"},
			Summary:          "track decided",
		},
	}
	exec.scripts["implement"] = []StepResult{
		{Success: true, Output: "PR opened", Summary: "impl done"},
	}
	exec.scripts["review"] = []StepResult{
		// First attempt: reject (verdict=rejected)
		{
			Success:          true,
			StructuredOutput: map[string]any{"verdict": "rejected"},
			Summary:          "needs revision",
		},
		// Second attempt: approve
		{
			Success:          true,
			StructuredOutput: map[string]any{"verdict": "approved"},
			Summary:          "LGTM",
		},
	}
	exec.scripts["qa"] = []StepResult{
		{Success: true, Output: "tests pass", Summary: "qa ok"},
	}

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "investigator", Model: "claude-opus-4-8"},
			{ID: "engineer", Model: "claude-sonnet-4-6"},
			{ID: "reviewer", Model: "claude-sonnet-4-6"},
			{ID: "qa-agent", Model: "claude-sonnet-4-6"},
		},
		Settings: config.Settings{Concurrency: 1},
	}
	eng := NewEngine(cfg, client, exec)

	// Build a v2-authored workflow, lower it, then run it.
	rawWF := config.WorkflowConfig{
		ID: "classify-impl-review-qa",
		Steps: []config.StepConfig{
			{
				ID:    "classify",
				Agent: "investigator",
				Output: &config.OutputSchema{
					Type: "object",
					Properties: map[string]config.SchemaField{
						"track": {Type: "string"},
					},
				},
			},
			{
				ID:    "implement",
				Agent: "engineer",
			},
			{
				ID:         "review",
				Agent:      "reviewer",
				RejectWhen: `${{ memory.verdict == "rejected" }}`,
				OnReject:   &config.OnRejectConfig{RestartFrom: "implement", Max: 2},
				Output: &config.OutputSchema{
					Type: "object",
					Properties: map[string]config.SchemaField{
						"verdict": {Type: "string"},
					},
				},
			},
			{
				ID:    "qa",
				Agent: "qa-agent",
			},
		},
	}

	lowered, err := config.LowerV2Workflow(rawWF)
	if err != nil {
		t.Fatalf("LowerV2Workflow: %v", err)
	}

	instID, success, err := eng.RunInstance(ctx, lowered, model.Cell{ID: "issue-42"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatalf("expected workflow to succeed, got failure (instID=%s)", instID)
	}

	// Verify step runs persisted in DB.
	runs, err := client.ListStepRuns(ctx, instID)
	if err != nil {
		t.Fatalf("ListStepRuns: %v", err)
	}

	// Expect: classify, implement, review(fail), implement(retry), review(pass), qa
	// Step IDs in DB depend on seqExecutor scripts: 6 step runs total.
	if len(runs) < 4 {
		t.Fatalf("expected at least 4 step runs (classify, implement, review×2, implement×retry, qa), got %d: %v", len(runs), stepIDs(runs))
	}

	// The last run should be qa.
	last := runs[len(runs)-1]
	if last.StepID != "qa" {
		t.Errorf("expected last step to be qa, got %q", last.StepID)
	}
	if last.State != "passed" {
		t.Errorf("expected qa step state=passed, got %q", last.State)
	}

	// classify and review must each appear at least once.
	ran := map[string]int{}
	for _, r := range runs {
		ran[r.StepID]++
	}
	for _, want := range []string{"classify", "implement", "review", "qa"} {
		if ran[want] == 0 {
			t.Errorf("expected step %q to run at least once, got %v", want, ran)
		}
	}
	// implement runs twice (original + retry after review rejection).
	if ran["implement"] < 2 {
		t.Errorf("expected implement to run at least 2 times (retry), got %d", ran["implement"])
	}
	// review runs twice (rejected + approved).
	if ran["review"] < 2 {
		t.Errorf("expected review to run at least 2 times (rejected + approved), got %d", ran["review"])
	}
}

// TestE2E_V2ConditionSkipsTrack verifies that the v2 `if:` guard (lowered to
// `condition`) correctly skips the complex track when the classifier picks
// "simple". The simple track runs and complex track is skipped.
func TestE2E_V2ConditionSkipsTrack(t *testing.T) {
	ctx := context.Background()
	client := realDB(t)

	exec := &fakeExecutor{results: map[string]StepResult{
		"classify": {
			Success:          true,
			StructuredOutput: map[string]any{"track": "simple"},
			Summary:          "track=simple",
		},
		"simple-impl":  {Success: true, Output: "simple done"},
		"complex-impl": {Success: true, Output: "complex done"},
	}}

	cfg := &config.Config{
		Agents:   []config.AgentConfig{{ID: "ag", Model: "m"}},
		Settings: config.Settings{Concurrency: 1},
	}
	eng := NewEngine(cfg, client, exec)

	// V2 workflow with two mutually exclusive conditional steps after classify.
	// simple-impl runs (track=simple), complex-impl is skipped.
	rawWF := config.WorkflowConfig{
		ID: "track-wf",
		Steps: []config.StepConfig{
			{
				ID:    "classify",
				Agent: "ag",
				Output: &config.OutputSchema{
					Type:       "object",
					Properties: map[string]config.SchemaField{"track": {Type: "string"}},
				},
				Memory: &config.MemoryConfig{Write: []string{"track"}},
			},
			{
				ID:    "simple-impl",
				Agent: "ag",
				If:    `${{ memory.track == "simple" }}`,
			},
			{
				ID:    "complex-impl",
				Agent: "ag",
				If:    `${{ memory.track == "complex" }}`,
			},
		},
	}

	lowered, err := config.LowerV2Workflow(rawWF)
	if err != nil {
		t.Fatalf("LowerV2Workflow: %v", err)
	}

	_, success, err := eng.RunInstance(ctx, lowered, model.Cell{ID: "c1"})
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if !success {
		t.Fatal("expected success (skipped steps don't fail the workflow)")
	}

	exec.mu.Lock()
	ids := executedIDs(exec.seen)
	exec.mu.Unlock()

	ran := map[string]bool{}
	for _, id := range ids {
		ran[id] = true
	}

	if !ran["classify"] {
		t.Error("expected classify to run")
	}
	if !ran["simple-impl"] {
		t.Error("expected simple-impl to run (track=simple)")
	}
	if ran["complex-impl"] {
		t.Error("expected complex-impl to be skipped (track=simple)")
	}
}

