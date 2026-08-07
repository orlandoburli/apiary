package improve

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func baselineJSON(t *testing.T, m StepMetrics) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMeasureEffectReportsImprovement(t *testing.T) {
	db := seedDB(t)
	appliedAt := base.Add(-2 * time.Hour)

	addInstance(t, db, instanceOpts{id: "i1", workflowID: "impl", state: "done"})
	// Ten runs after the change: low failure, cheaper than the baseline.
	for i := range 10 {
		state := "passed"
		if i == 0 {
			state = "failed"
		}
		addStep(t, db, stepOpts{
			id: "s" + itoa(i), instanceID: "i1", stepID: "implement", agentID: "eng",
			state: state, startedAt: appliedAt.Add(time.Duration(i) * time.Minute),
			durationMs: 60_000, cost: 0.20, turns: 10,
		})
	}

	run := &LedgerRun{ID: "imp_1", AppliedAt: &appliedAt}
	findings := []LedgerFinding{{
		FindingID: "r1", Scope: "workflow:impl/step:implement", State: FindingApplied,
		Symptom: "fails often", MachineChecked: false,
		BaselineMetrics: baselineJSON(t, StepMetrics{
			WorkflowID: "impl", StepID: "implement", Runs: 40,
			FailRate: 0.40, MeanCostUSD: 0.50, DurationP95Ms: 120_000, MeanTurns: 25,
		}),
	}}

	effects, err := MeasureEffect(ctx(), db, run, findings, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("MeasureEffect: %v", err)
	}
	if len(effects) != 1 {
		t.Fatalf("want 1 effect, got %d", len(effects))
	}
	e := effects[0]
	if e.SampleBefore != 40 || e.SampleAfter != 10 {
		t.Errorf("samples = %d before / %d after, want 40 / 10", e.SampleBefore, e.SampleAfter)
	}
	if e.Insufficient {
		t.Error("10 runs is above MinRuns and should not be flagged insufficient")
	}

	byMetric := map[string]Delta{}
	for _, d := range e.Deltas {
		byMetric[d.Metric] = d
	}
	fail := byMetric["fail rate"]
	if fail.Before != 0.40 {
		t.Errorf("fail-rate baseline = %v, want 0.40", fail.Before)
	}
	if fail.After >= fail.Before {
		t.Errorf("fail rate should have dropped: %v → %v", fail.Before, fail.After)
	}
	// Lower is better for fail rate, so a drop is a positive change.
	if fail.Change() <= 0 {
		t.Errorf("a reduced fail rate must read as improvement, got %v", fail.Change())
	}

	out := RenderEffect(run, effects)
	if !strings.Contains(out, "better") {
		t.Errorf("the rendered table should mark the improvement:\n%s", out)
	}
	// The finding was an unvalidatable instruction change; the measurement is
	// the first evidence either way, and the output should say so.
	if !strings.Contains(out, "first evidence") {
		t.Errorf("a prose change's measurement should be framed as its first evidence:\n%s", out)
	}
}

func TestMeasureEffectDetectsRegression(t *testing.T) {
	db := seedDB(t)
	appliedAt := base.Add(-2 * time.Hour)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "impl", state: "done"})
	for i := range 8 {
		addStep(t, db, stepOpts{
			id: "s" + itoa(i), instanceID: "i1", stepID: "implement", agentID: "eng",
			state: "failed", startedAt: appliedAt.Add(time.Duration(i) * time.Minute),
			durationMs: 60_000, cost: 1.00,
		})
	}

	run := &LedgerRun{ID: "imp_1", AppliedAt: &appliedAt}
	findings := []LedgerFinding{{
		FindingID: "r1", Scope: "workflow:impl/step:implement", State: FindingApplied,
		BaselineMetrics: baselineJSON(t, StepMetrics{
			WorkflowID: "impl", StepID: "implement", Runs: 40, FailRate: 0.10, MeanCostUSD: 0.50,
		}),
	}}

	effects, err := MeasureEffect(ctx(), db, run, findings, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("MeasureEffect: %v", err)
	}
	out := RenderEffect(run, effects)
	if !strings.Contains(out, "worse") {
		t.Errorf("a change that made things worse must say so plainly:\n%s", out)
	}
}

// Reporting a percentage off three runs would be worse than reporting nothing,
// because it reads as a result.
func TestMeasureEffectGuardsThinSamples(t *testing.T) {
	db := seedDB(t)
	appliedAt := base.Add(-time.Hour)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "impl", state: "done"})
	for i := range 2 {
		addStep(t, db, stepOpts{
			id: "s" + itoa(i), instanceID: "i1", stepID: "implement", agentID: "eng",
			state: "passed", startedAt: appliedAt.Add(time.Duration(i) * time.Minute), cost: 0.1,
		})
	}

	run := &LedgerRun{ID: "imp_1", AppliedAt: &appliedAt}
	findings := []LedgerFinding{{
		FindingID: "r1", Scope: "workflow:impl/step:implement", State: FindingApplied,
		BaselineMetrics: baselineJSON(t, StepMetrics{
			WorkflowID: "impl", StepID: "implement", Runs: 40, FailRate: 0.40,
		}),
	}}

	effects, err := MeasureEffect(ctx(), db, run, findings, base)
	if err != nil {
		t.Fatalf("MeasureEffect: %v", err)
	}
	if !effects[0].Insufficient {
		t.Fatal("2 runs is below MinRuns and must be flagged insufficient")
	}
	out := RenderEffect(run, effects)
	if !strings.Contains(out, "too few to conclude") {
		t.Errorf("a thin sample must be labelled, not presented as a result:\n%s", out)
	}
	if !strings.Contains(out, "not as a result") {
		t.Errorf("the caveat should be explicit:\n%s", out)
	}
}

func TestMeasureEffectHandlesScopeThatNeverRanAgain(t *testing.T) {
	db := seedDB(t)
	appliedAt := base.Add(-time.Hour)
	run := &LedgerRun{ID: "imp_1", AppliedAt: &appliedAt}
	findings := []LedgerFinding{{
		FindingID: "r1", Scope: "workflow:gone/step:missing", State: FindingApplied,
		BaselineMetrics: baselineJSON(t, StepMetrics{Runs: 10}),
	}}

	effects, err := MeasureEffect(ctx(), db, run, findings, base)
	if err != nil {
		t.Fatalf("MeasureEffect: %v", err)
	}
	if effects[0].Note == "" {
		t.Error("a scope with no runs since the change needs an explanation, not an empty table")
	}
	out := RenderEffect(run, effects)
	if !strings.Contains(out, "has not run since") {
		t.Errorf("output should explain the gap:\n%s", out)
	}
}

func TestMeasureEffectSkipsUnappliedFindings(t *testing.T) {
	db := seedDB(t)
	appliedAt := base.Add(-time.Hour)
	run := &LedgerRun{ID: "imp_1", AppliedAt: &appliedAt}
	findings := []LedgerFinding{
		{FindingID: "r1", Scope: "s", State: FindingProposed},
		{FindingID: "r2", Scope: "s", State: FindingRejected},
	}
	effects, err := MeasureEffect(ctx(), db, run, findings, base)
	if err != nil {
		t.Fatalf("MeasureEffect: %v", err)
	}
	if len(effects) != 0 {
		t.Errorf("only applied findings can be measured, got %d", len(effects))
	}
}

func TestMeasureEffectRequiresAnApply(t *testing.T) {
	db := seedDB(t)
	run := &LedgerRun{ID: "imp_1"} // never applied
	if _, err := MeasureEffect(ctx(), db, run, nil, base); err == nil {
		t.Error("measuring a run that was never applied must be an error")
	}
}

func TestDeltaChangeDirection(t *testing.T) {
	// Lower-is-better: a drop is improvement.
	drop := Delta{Before: 0.4, After: 0.1, LowerIsBetter: true}
	if drop.Change() <= 0 {
		t.Errorf("a drop in a lower-is-better metric is improvement, got %v", drop.Change())
	}
	rise := Delta{Before: 0.1, After: 0.4, LowerIsBetter: true}
	if rise.Change() >= 0 {
		t.Errorf("a rise in a lower-is-better metric is regression, got %v", rise.Change())
	}
	// A zero baseline cannot produce a ratio.
	if (Delta{Before: 0, After: 5}).Change() != 0 {
		t.Error("a zero baseline must not produce a change ratio")
	}
}
