package improve

import (
	"math"
	"testing"
	"time"
)

func TestStepMetricsRatesAndTotals(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "review", state: "done"})
	addInstance(t, db, instanceOpts{id: "i2", workflowID: "review", state: "failed"})

	// 4 runs of review/lint: 2 passed, 1 failed, 1 cached skip.
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "lint", agentID: "eng",
		state: "passed", durationMs: 1000, tokens: 100, cost: 0.10, turns: 3, toolCalls: 5})
	addStep(t, db, stepOpts{id: "s2", instanceID: "i1", stepID: "lint", agentID: "eng",
		state: "passed", durationMs: 3000, tokens: 300, cost: 0.30, turns: 5, toolCalls: 9})
	addStep(t, db, stepOpts{id: "s3", instanceID: "i2", stepID: "lint", agentID: "eng",
		state: "failed", durationMs: 2000, tokens: 200, cost: 0.20, turns: 4, toolCalls: 7})
	addStep(t, db, stepOpts{id: "s4", instanceID: "i2", stepID: "lint", agentID: "eng",
		state: "skipped_cached", cached: true})

	steps, err := StepMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("StepMetricsFor: %v", err)
	}

	m := findStep(t, steps, "review", "lint")
	if m.Runs != 4 {
		t.Errorf("Runs = %d, want 4", m.Runs)
	}
	if m.Passed != 2 || m.Failed != 1 || m.SkippedCached != 1 {
		t.Errorf("counts = %d/%d/%d, want 2/1/1", m.Passed, m.Failed, m.SkippedCached)
	}
	if got, want := m.PassRate, 0.5; !closeTo(got, want) {
		t.Errorf("PassRate = %v, want %v", got, want)
	}
	if got, want := m.FailRate, 0.25; !closeTo(got, want) {
		t.Errorf("FailRate = %v, want %v", got, want)
	}
	if m.TotalTokens != 600 {
		t.Errorf("TotalTokens = %d, want 600", m.TotalTokens)
	}
	if !closeTo(m.TotalCostUSD, 0.60) {
		t.Errorf("TotalCostUSD = %v, want 0.60", m.TotalCostUSD)
	}
	// Only three runs recorded a duration; the cached skip has none.
	if m.DurationP50Ms != 2000 {
		t.Errorf("DurationP50Ms = %d, want 2000", m.DurationP50Ms)
	}
	if m.DurationP95Ms != 3000 {
		t.Errorf("DurationP95Ms = %d, want 3000", m.DurationP95Ms)
	}
	if !m.LowConfidence {
		t.Error("4 runs is below MinRuns; want LowConfidence")
	}
}

func TestStepMetricsCacheAndPromptRatios(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "big", agentID: "eng",
		inputTok: 1000, cacheRead: 750, outputTok: 100, prompt: string(make([]byte, 4000))})

	steps, err := StepMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("StepMetricsFor: %v", err)
	}
	m := findStep(t, steps, "wf", "big")
	if !closeTo(m.CacheReuseRatio, 0.75) {
		t.Errorf("CacheReuseRatio = %v, want 0.75", m.CacheReuseRatio)
	}
	if !closeTo(m.PromptWeightRatio, 40) {
		t.Errorf("PromptWeightRatio = %v, want 40", m.PromptWeightRatio)
	}
}

func TestStepMetricsZeroDenominatorsAreSafe(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	// A runner that reports no usage at all: every ratio denominator is zero.
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "quiet", agentID: "eng"})

	steps, err := StepMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("StepMetricsFor: %v", err)
	}
	m := findStep(t, steps, "wf", "quiet")
	if m.CacheReuseRatio != 0 || m.PromptWeightRatio != 0 || m.MeanTokens != 0 {
		t.Errorf("want zeroed ratios, got cache=%v prompt=%v tokens=%v",
			m.CacheReuseRatio, m.PromptWeightRatio, m.MeanTokens)
	}
}

func TestStepMetricsFailoverAndFailureKinds(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addInstance(t, db, instanceOpts{id: "i2", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "impl", agentID: "eng"})
	addStep(t, db, stepOpts{id: "s2", instanceID: "i2", stepID: "impl", agentID: "eng"})

	// i1's step needed two attempts (rate limit on the first); i2's took one.
	addExec(t, db, execOpts{agentID: "eng", instanceID: "i1", stepID: "impl",
		attempt: 1, status: "failed", failureKind: "rate_limited"})
	addExec(t, db, execOpts{agentID: "eng", instanceID: "i1", stepID: "impl",
		attempt: 2, status: "success", failureKind: "none"})
	addExec(t, db, execOpts{agentID: "eng", instanceID: "i2", stepID: "impl",
		attempt: 1, status: "success", failureKind: "none"})

	steps, err := StepMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("StepMetricsFor: %v", err)
	}
	m := findStep(t, steps, "wf", "impl")
	if !closeTo(m.FailoverRate, 0.5) {
		t.Errorf("FailoverRate = %v, want 0.5 (1 of 2 runs failed over)", m.FailoverRate)
	}
	if m.FailureKinds["rate_limited"] != 1 {
		t.Errorf("FailureKinds = %v, want rate_limited:1", m.FailureKinds)
	}
	if _, ok := m.FailureKinds["none"]; ok {
		t.Error(`"none" is not a failure kind and must not be counted`)
	}
}

// The `attempt` column is a per-task counter, not a per-step retry count:
// beginExecution sets it to the task's last attempt + 1, so the fifth step of a
// workflow carries attempt=5 having never failed over. Counting attempt>1 as a
// failover marks nearly every step in every workflow as one.
func TestFailoverIgnoresInheritedAttemptNumbers(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "fifth", agentID: "eng"})

	// One invocation, but it is the task's fifth execution overall.
	addExec(t, db, execOpts{agentID: "eng", instanceID: "i1", stepID: "fifth",
		attempt: 5, status: "success"})

	steps, err := StepMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("StepMetricsFor: %v", err)
	}
	if m := findStep(t, steps, "wf", "fifth"); m.FailoverRate != 0 {
		t.Errorf("FailoverRate = %v, want 0: a high attempt number inherited from "+
			"the task's history is not a failover", m.FailoverRate)
	}
}

func TestReworkLoopsDetectsRepeatedSteps(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addInstance(t, db, instanceOpts{id: "i2", workflowID: "wf", state: "done"})

	// i1 ran "review" three times (two loop-backs), i2 ran it once.
	addStep(t, db, stepOpts{id: "a", instanceID: "i1", stepID: "review", state: "failed", cost: 0.10})
	addStep(t, db, stepOpts{id: "b", instanceID: "i1", stepID: "review", state: "failed", cost: 0.10})
	addStep(t, db, stepOpts{id: "c", instanceID: "i1", stepID: "review", state: "passed", cost: 0.10})
	addStep(t, db, stepOpts{id: "d", instanceID: "i2", stepID: "review", state: "passed", cost: 0.10})

	loops, err := ReworkLoopsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("ReworkLoopsFor: %v", err)
	}
	got := loops["wf"]
	if len(got) != 1 {
		t.Fatalf("want 1 rework loop, got %d: %+v", len(got), got)
	}
	if got[0].StepID != "review" {
		t.Errorf("StepID = %q, want review", got[0].StepID)
	}
	if got[0].Instances != 1 {
		t.Errorf("Instances = %d, want 1 (only i1 looped)", got[0].Instances)
	}
	if got[0].TotalRepeats != 2 {
		t.Errorf("TotalRepeats = %d, want 2", got[0].TotalRepeats)
	}
	if !closeTo(got[0].WastedCostUSD, 0.20) {
		t.Errorf("WastedCostUSD = %v, want 0.20", got[0].WastedCostUSD)
	}
}

func TestReworkLoopsIgnoresSingleRuns(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "a", instanceID: "i1", stepID: "one", state: "passed"})
	addStep(t, db, stepOpts{id: "b", instanceID: "i1", stepID: "two", state: "passed"})

	loops, err := ReworkLoopsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("ReworkLoopsFor: %v", err)
	}
	if len(loops) != 0 {
		t.Errorf("want no rework loops, got %+v", loops)
	}
}

func TestWindowExcludesOutOfRangeRows(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "in", instanceID: "i1", stepID: "s", startedAt: base})
	addStep(t, db, stepOpts{id: "old", instanceID: "i1", stepID: "s", startedAt: base.Add(-72 * time.Hour)})
	addStep(t, db, stepOpts{id: "future", instanceID: "i1", stepID: "s", startedAt: base.Add(72 * time.Hour)})

	steps, err := StepMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("StepMetricsFor: %v", err)
	}
	if m := findStep(t, steps, "wf", "s"); m.Runs != 1 {
		t.Errorf("Runs = %d, want 1 (window must exclude the old and future rows)", m.Runs)
	}
}

func TestScopeFiltersByWorkflowAndAgent(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "alpha", state: "done"})
	addInstance(t, db, instanceOpts{id: "i2", workflowID: "beta", state: "done"})
	addStep(t, db, stepOpts{id: "a", instanceID: "i1", stepID: "s", agentID: "eng"})
	addStep(t, db, stepOpts{id: "b", instanceID: "i2", stepID: "s", agentID: "eng"})
	addStep(t, db, stepOpts{id: "c", instanceID: "i2", stepID: "s", agentID: "reviewer"})

	byWorkflow, err := StepMetricsFor(ctx(), db, testWindow(), Scope{Workflows: []string{"alpha"}})
	if err != nil {
		t.Fatalf("StepMetricsFor: %v", err)
	}
	if len(byWorkflow) != 1 || byWorkflow[0].WorkflowID != "alpha" {
		t.Errorf("workflow scope leaked: %v", stepKeys(byWorkflow))
	}

	byAgent, err := StepMetricsFor(ctx(), db, testWindow(), Scope{Agents: []string{"reviewer"}})
	if err != nil {
		t.Fatalf("StepMetricsFor: %v", err)
	}
	if len(byAgent) != 1 || byAgent[0].AgentID != "reviewer" {
		t.Errorf("agent scope leaked: %+v", byAgent)
	}
}

func TestWorkflowMetricsStatesAndCost(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done", durationMs: 10000})
	addInstance(t, db, instanceOpts{id: "i2", workflowID: "wf", state: "failed", durationMs: 30000})
	addStep(t, db, stepOpts{id: "a", instanceID: "i1", stepID: "s", cost: 1.00})
	addStep(t, db, stepOpts{id: "b", instanceID: "i2", stepID: "s", cost: 0.50})

	got, err := WorkflowMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("WorkflowMetricsFor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 workflow, got %d", len(got))
	}
	w := got[0]
	if w.Instances != 2 {
		t.Errorf("Instances = %d, want 2", w.Instances)
	}
	if w.ByState["done"] != 1 || w.ByState["failed"] != 1 {
		t.Errorf("ByState = %v", w.ByState)
	}
	if !closeTo(w.TotalCostUSD, 1.50) {
		t.Errorf("TotalCostUSD = %v, want 1.50", w.TotalCostUSD)
	}
	// One instance reached "done", so the whole spend is attributed to it.
	if !closeTo(w.CostPerCompletedUSD, 1.50) {
		t.Errorf("CostPerCompletedUSD = %v, want 1.50", w.CostPerCompletedUSD)
	}
}

func TestAgentMetricsGroupsByRunnerAndModel(t *testing.T) {
	db := seedDB(t)
	addExec(t, db, execOpts{agentID: "eng", runner: "claude", model: "opus", status: "success", durationMs: 1000, cost: 1.0})
	addExec(t, db, execOpts{agentID: "eng", runner: "claude", model: "opus", status: "failed", durationMs: 3000, cost: 1.0})
	addExec(t, db, execOpts{agentID: "eng", runner: "claude", model: "haiku", status: "success", durationMs: 500, cost: 0.1})

	got, err := AgentMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("AgentMetricsFor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows (one per model), got %d: %+v", len(got), got)
	}
	for _, m := range got {
		switch m.Model {
		case "opus":
			if m.Runs != 2 || !closeTo(m.SuccessRate, 0.5) {
				t.Errorf("opus: runs=%d success=%v, want 2 / 0.5", m.Runs, m.SuccessRate)
			}
			if m.MeanDurationMs != 2000 {
				t.Errorf("opus MeanDurationMs = %d, want 2000", m.MeanDurationMs)
			}
		case "haiku":
			if m.Runs != 1 || !closeTo(m.SuccessRate, 1) {
				t.Errorf("haiku: runs=%d success=%v, want 1 / 1", m.Runs, m.SuccessRate)
			}
		}
	}
}

func TestTurnCapSaturation(t *testing.T) {
	db := seedDB(t)
	// eng is capped at 10 turns; 2 of 4 runs stopped exactly there.
	addExec(t, db, execOpts{agentID: "eng", turns: 10})
	addExec(t, db, execOpts{agentID: "eng", turns: 10})
	addExec(t, db, execOpts{agentID: "eng", turns: 4})
	addExec(t, db, execOpts{agentID: "eng", turns: 7})
	// uncapped agent must not appear at all.
	addExec(t, db, execOpts{agentID: "free", turns: 99})

	got, err := TurnCapSaturationFor(ctx(), db, testWindow(), Scope{}, map[string]int{"eng": 10})
	if err != nil {
		t.Fatalf("TurnCapSaturationFor: %v", err)
	}
	if !closeTo(got["eng"], 0.5) {
		t.Errorf("eng saturation = %v, want 0.5", got["eng"])
	}
	if _, ok := got["free"]; ok {
		t.Error("agent without a configured cap must not be reported")
	}
}

func TestWaitMetricsCountsPolls(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addInstance(t, db, instanceOpts{id: "i2", workflowID: "wf", state: "failed"})

	for i := range 5 {
		addPoll(t, db, "i1", "ci", "pending", base.Add(time.Duration(i)*time.Minute))
	}
	addPoll(t, db, "i1", "ci", "passed", base.Add(6*time.Minute))
	addPoll(t, db, "i2", "ci", "timeout", base)

	got, err := WaitMetricsFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("WaitMetricsFor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 wait row, got %d: %+v", len(got), got)
	}
	w := got[0]
	if w.Waits != 2 {
		t.Errorf("Waits = %d, want 2", w.Waits)
	}
	if w.TotalPolls != 7 {
		t.Errorf("TotalPolls = %d, want 7", w.TotalPolls)
	}
	if w.MaxPolls != 6 {
		t.Errorf("MaxPolls = %d, want 6", w.MaxPolls)
	}
	if w.Timeouts != 1 {
		t.Errorf("Timeouts = %d, want 1", w.Timeouts)
	}
	if w.TerminalStatus["passed"] != 1 {
		t.Errorf("TerminalStatus = %v, want passed:1", w.TerminalStatus)
	}
}

func TestFailureClustersGroupAndRank(t *testing.T) {
	db := seedDB(t)
	addExec(t, db, execOpts{agentID: "eng", status: "failed", errMessage: "task 4821 timed out after 1800s"})
	addExec(t, db, execOpts{agentID: "eng", status: "failed", errMessage: "task 9134 timed out after 900s"})
	addExec(t, db, execOpts{agentID: "rev", status: "failed", errMessage: "task 7 timed out after 60s"})
	addExec(t, db, execOpts{agentID: "eng", status: "failed", errMessage: "permission denied"})
	addExec(t, db, execOpts{agentID: "eng", status: "success", errMessage: ""})

	got, err := FailureClustersFor(ctx(), db, testWindow(), Scope{})
	if err != nil {
		t.Fatalf("FailureClustersFor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 clusters, got %d: %+v", len(got), got)
	}
	// Most frequent first.
	if got[0].Count != 3 {
		t.Errorf("top cluster count = %d, want 3", got[0].Count)
	}
	if len(got[0].Agents) != 2 {
		t.Errorf("top cluster agents = %v, want both eng and rev", got[0].Agents)
	}
	if got[1].Count != 1 {
		t.Errorf("second cluster count = %d, want 1", got[1].Count)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	cases := []struct {
		name   string
		sorted []int64
		p      float64
		want   int64
	}{
		{"empty", nil, 0.5, 0},
		{"single", []int64{42}, 0.95, 42},
		{"median odd", []int64{1, 2, 3}, 0.5, 2},
		{"median even", []int64{1, 2, 3, 4}, 0.5, 2},
		{"p95 of ten", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.95, 10},
		{"p0 clamps low", []int64{5, 6}, 0, 5},
		{"p1 clamps high", []int64{5, 6}, 1, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentile(tc.sorted, tc.p); got != tc.want {
				t.Errorf("percentile(%v, %v) = %d, want %d", tc.sorted, tc.p, got, tc.want)
			}
		})
	}
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
