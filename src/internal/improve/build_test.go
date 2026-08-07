package improve

import (
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
)

func fixedClock() func() time.Time { return func() time.Time { return base } }

func TestBuildProducesCompletePack(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "review", state: "done", durationMs: 5000})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "lint", agentID: "eng",
		state: "passed", durationMs: 1000, tokens: 100, cost: 0.10})
	addExec(t, db, execOpts{agentID: "eng", runner: "claude", model: "opus",
		instanceID: "i1", stepID: "lint", status: "success", cost: 0.10})

	pack, err := Build(ctx(), db, Options{Window: testWindow(), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pack.Steps) != 1 || len(pack.Workflows) != 1 || len(pack.Agents) != 1 {
		t.Errorf("pack incomplete: %d steps, %d workflows, %d agents",
			len(pack.Steps), len(pack.Workflows), len(pack.Agents))
	}
	if pack.Digest == "" {
		t.Error("Digest must be set")
	}
}

func TestBuildDigestIsStableAcrossRuns(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "a", agentID: "eng", cost: 0.1})
	addStep(t, db, stepOpts{id: "s2", instanceID: "i1", stepID: "b", agentID: "eng", cost: 0.2})

	first, err := Build(ctx(), db, Options{Window: testWindow(), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := Build(ctx(), db, Options{Window: testWindow(), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if first.Digest != second.Digest {
		t.Errorf("digest is not stable: %s vs %s", first.Digest, second.Digest)
	}
}

func TestBuildDigestIgnoresGenerationTimeAndWindowBounds(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "a", agentID: "eng"})

	early, err := Build(ctx(), db, Options{Window: testWindow(), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// A different generation time and a wider window that still selects exactly
	// the same rows must not change the digest — otherwise "same evidence" could
	// never be recognised across two runs.
	wider := Window{Start: base.Add(-48 * time.Hour), End: base.Add(48 * time.Hour)}
	later, err := Build(ctx(), db, Options{Window: wider,
		Clock: func() time.Time { return base.Add(time.Hour) }})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if early.Digest != later.Digest {
		t.Errorf("digest changed with generation time / window bounds: %s vs %s",
			early.Digest, later.Digest)
	}
}

func TestBuildDigestChangesWithData(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "a", agentID: "eng"})

	before, err := Build(ctx(), db, Options{Window: testWindow(), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	addStep(t, db, stepOpts{id: "s2", instanceID: "i1", stepID: "a", agentID: "eng", state: "failed"})
	after, err := Build(ctx(), db, Options{Window: testWindow(), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if before.Digest == after.Digest {
		t.Error("digest must change when the underlying rows change")
	}
}

func TestBuildAnnotatesFromConfig(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "lint", agentID: "eng"})
	addExec(t, db, execOpts{agentID: "eng", runner: "claude", instanceID: "i1", stepID: "lint", turns: 10})

	cfg := &config.Config{
		Workflows: []config.WorkflowConfig{
			{ID: "wf", Steps: []config.StepConfig{
				{ID: "lint", Agent: "eng", Prompt: "lint it"},
				{ID: "typecheck", Agent: "eng", Prompt: "type check it"},
			}},
			{ID: "orphan"},
		},
		Agents: []config.AgentConfig{{ID: "eng", MaxTurns: 10}, {ID: "ghost"}},
	}

	pack, err := Build(ctx(), db, Options{Window: testWindow(), Config: cfg, Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(pack.Dead.Workflows) != 1 || pack.Dead.Workflows[0] != "orphan" {
		t.Errorf("Dead.Workflows = %v, want [orphan]", pack.Dead.Workflows)
	}
	if len(pack.Dead.Agents) != 1 || pack.Dead.Agents[0] != "ghost" {
		t.Errorf("Dead.Agents = %v, want [ghost]", pack.Dead.Agents)
	}

	wf := pack.Workflows[0]
	if len(wf.DeadSteps) != 1 || wf.DeadSteps[0] != "typecheck" {
		t.Errorf("DeadSteps = %v, want [typecheck]", wf.DeadSteps)
	}
	if len(wf.ParallelCandidates) != 1 {
		t.Errorf("ParallelCandidates = %v, want the lint→typecheck pair", wf.ParallelCandidates)
	}
	// The single execution ran exactly to the configured 10-turn cap.
	if !closeTo(pack.Agents[0].MaxTurnsSaturation, 1) {
		t.Errorf("MaxTurnsSaturation = %v, want 1", pack.Agents[0].MaxTurnsSaturation)
	}
	if pack.Agents[0].ConfiguredMaxTurns != 10 {
		t.Errorf("ConfiguredMaxTurns = %d, want 10", pack.Agents[0].ConfiguredMaxTurns)
	}
}

func TestBuildWithoutConfigStillWorks(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "a", agentID: "eng"})

	pack, err := Build(ctx(), db, Options{Window: testWindow(), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Build without config: %v", err)
	}
	if len(pack.Steps) != 1 {
		t.Errorf("want the step metrics regardless of config, got %d", len(pack.Steps))
	}
	if len(pack.Dead.Workflows) != 0 {
		t.Error("without config there is nothing to call dead")
	}
}

func TestParseDurationDays(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"0.5d", 12 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"", 0, true},
		{"0d", 0, true},
		{"-3d", 0, true},
		{"-1h", 0, true},
		{"banana", 0, true},
		{"7days", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDurationDays(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseDurationDays(%q) = %v, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDurationDays(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseDurationDays(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseWindowEndsAtNow(t *testing.T) {
	w, err := ParseWindow("7d", base)
	if err != nil {
		t.Fatalf("ParseWindow: %v", err)
	}
	if !w.End.Equal(base.UTC()) {
		t.Errorf("End = %v, want %v", w.End, base.UTC())
	}
	if got := w.End.Sub(w.Start); got != 7*24*time.Hour {
		t.Errorf("span = %v, want 168h", got)
	}
}

func TestSummaryFlagsThinWindows(t *testing.T) {
	db := seedDB(t)
	addInstance(t, db, instanceOpts{id: "i1", workflowID: "wf", state: "done"})
	addStep(t, db, stepOpts{id: "s1", instanceID: "i1", stepID: "a", agentID: "eng", cost: 0.5})

	pack, err := Build(ctx(), db, Options{Window: testWindow(), Clock: fixedClock()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	summary := pack.Summary()
	if !strings.Contains(summary, "too thin") {
		t.Errorf("a one-run window must say so plainly; got:\n%s", summary)
	}
}

func TestRankHotspotsPrioritisesCostAndFailure(t *testing.T) {
	steps := []StepMetrics{
		{WorkflowID: "wf", StepID: "cheap-reliable", Runs: 10, TotalCostUSD: 0.10, FailRate: 0},
		{WorkflowID: "wf", StepID: "costly-reliable", Runs: 10, TotalCostUSD: 5.00, FailRate: 0},
		{WorkflowID: "wf", StepID: "cheap-flaky", Runs: 10, TotalCostUSD: 1.00, FailRate: 0.9},
		{WorkflowID: "wf", StepID: "never-ran", Runs: 0},
	}
	got := RankHotspots(steps, 3)
	if len(got) != 3 {
		t.Fatalf("want 3 hotspots, got %d", len(got))
	}
	if got[0].StepID != "costly-reliable" {
		t.Errorf("top hotspot = %s, want costly-reliable", got[0].StepID)
	}
	if got[1].StepID != "cheap-flaky" {
		t.Errorf("second hotspot = %s, want cheap-flaky (failure weighting)", got[1].StepID)
	}
	for _, h := range got {
		if h.StepID == "never-ran" {
			t.Error("a step with no runs is not a hotspot")
		}
	}
}

func TestElideKeepsHeadAndTail(t *testing.T) {
	s := strings.Repeat("a", 500) + strings.Repeat("z", 500)
	got := elide(s, 200)
	if len(got) > 250 {
		t.Errorf("elided length = %d, want near the 200 budget", len(got))
	}
	if !strings.HasPrefix(got, "aaa") {
		t.Error("head must be preserved")
	}
	if !strings.HasSuffix(got, "zzz") {
		t.Error("tail must be preserved")
	}
	if !strings.Contains(got, "elided") {
		t.Error("elision must be visible in the output")
	}
	if short := elide("tiny", 200); short != "tiny" {
		t.Errorf("under-budget input must pass through unchanged, got %q", short)
	}
}
