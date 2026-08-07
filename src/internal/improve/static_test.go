package improve

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

func TestNormalizeFailure(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		same bool
	}{
		{"ids and durations differ", "task 4821 timed out after 1800s", "task 9134 timed out after 900s", true},
		{"paths differ", "cannot read /home/alice/repo/x.go", "cannot read /home/bob/other/y.go", true},
		{"uuids differ", "run 550e8400-e29b-41d4-a716-446655440000 failed", "run 6ba7b810-9dad-11d1-80b4-00c04fd430c8 failed", true},
		{"hex shas differ", "commit a1b2c3d4e5f rejected", "commit f9e8d7c6b5a rejected", true},
		{"urls differ", "GET https://api.example.com/a failed", "GET https://api.other.org/b failed", true},
		{"timestamps differ", "at 2026-08-01T10:00:00Z the run died", "at 2026-07-14T22:31:09Z the run died", true},
		{"genuinely different failures", "permission denied", "out of credits", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			na, nb := NormalizeFailure(tc.a), NormalizeFailure(tc.b)
			if (na == nb) != tc.same {
				t.Errorf("normalize(%q)=%q vs normalize(%q)=%q: same=%v, want %v",
					tc.a, na, tc.b, nb, na == nb, tc.same)
			}
		})
	}
}

func TestParallelCandidatesFindsIndependentSteps(t *testing.T) {
	wf := config.WorkflowConfig{
		ID: "wf",
		Steps: []config.StepConfig{
			{ID: "lint", Agent: "eng", Prompt: "run the linter"},
			{ID: "typecheck", Agent: "eng", Prompt: "run the type checker"},
		},
	}
	got := ParallelCandidates(wf)
	if len(got) != 1 {
		t.Fatalf("want 1 candidate, got %d: %+v", len(got), got)
	}
	if got[0].First != "lint" || got[0].Second != "typecheck" {
		t.Errorf("pair = %s→%s, want lint→typecheck", got[0].First, got[0].Second)
	}
}

func TestParallelCandidatesRespectsDependencies(t *testing.T) {
	cases := []struct {
		name  string
		steps []config.StepConfig
	}{
		{
			"prompt names the earlier step",
			[]config.StepConfig{
				{ID: "plan", Agent: "eng", Prompt: "write a plan"},
				{ID: "build", Agent: "eng", Prompt: "implement what plan produced"},
			},
		},
		{
			"condition references the earlier step",
			[]config.StepConfig{
				{ID: "plan", Agent: "eng", Prompt: "write a plan"},
				{ID: "build", Agent: "eng", Prompt: "build", Condition: `plan.approved == true`},
			},
		},
		{
			"fail_when references the earlier step",
			[]config.StepConfig{
				{ID: "plan", Agent: "eng", Prompt: "plan"},
				{ID: "build", Agent: "eng", Prompt: "build", FailWhen: `plan.blocked`},
			},
		},
		{
			"second step reads shared state with an unknown producer",
			[]config.StepConfig{
				{ID: "a", Agent: "eng", Prompt: "do a"},
				{ID: "b", Agent: "eng", Prompt: "use the prior outputs"},
			},
		},
		{
			"env value references the earlier step",
			[]config.StepConfig{
				{ID: "a", Agent: "eng", Prompt: "do a"},
				{ID: "b", Agent: "eng", Prompt: "do b", Env: map[string]string{"REF": "${{ a.sha }}"}},
			},
		},
		{
			"first step has an explicit failure edge",
			[]config.StepConfig{
				{ID: "a", Agent: "eng", Prompt: "do a", OnFail: &config.StepOutcome{}},
				{ID: "b", Agent: "eng", Prompt: "do b"},
			},
		},
		{
			"first step publishes back to the source",
			[]config.StepConfig{
				{ID: "a", Agent: "eng", Prompt: "do a", Publish: "auto"},
				{ID: "b", Agent: "eng", Prompt: "do b"},
			},
		},
		{
			"first step spawns children",
			[]config.StepConfig{
				{ID: "a", Agent: "eng", Prompt: "decompose", Spawn: "await"},
				{ID: "b", Agent: "eng", Prompt: "do b"},
			},
		},
		{
			"control-flow step is never a candidate",
			[]config.StepConfig{
				{ID: "gate", Type: "approval", Message: "ok?"},
				{ID: "b", Agent: "eng", Prompt: "do b"},
			},
		},
		{
			"wait_for step is never a candidate",
			[]config.StepConfig{
				{ID: "ci", Type: "wait_for"},
				{ID: "b", Agent: "eng", Prompt: "do b"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParallelCandidates(config.WorkflowConfig{ID: "wf", Steps: tc.steps})
			if len(got) != 0 {
				t.Errorf("want no candidates (dependency present), got %+v", got)
			}
		})
	}
}

func TestDeadPathsReportsUnusedConfig(t *testing.T) {
	cfg := &config.Config{
		Workflows: []config.WorkflowConfig{{ID: "used"}, {ID: "never"}},
		Agents: []config.AgentConfig{
			{ID: "busy"},
			{ID: "idle", Fallbacks: []config.FallbackConfig{{Runner: "backup"}}},
		},
	}
	got := DeadPathsFor(cfg,
		map[string]bool{"used": true},
		map[string]bool{"busy": true},
		map[string]bool{"claude": true})

	if len(got.Workflows) != 1 || got.Workflows[0] != "never" {
		t.Errorf("Workflows = %v, want [never]", got.Workflows)
	}
	if len(got.Agents) != 1 || got.Agents[0] != "idle" {
		t.Errorf("Agents = %v, want [idle]", got.Agents)
	}
	if len(got.Fallbacks) != 1 || got.Fallbacks[0] != "backup" {
		t.Errorf("Fallbacks = %v, want [backup]", got.Fallbacks)
	}
}

func TestDeadPathsReportsEachFallbackOnce(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "a", Fallbacks: []config.FallbackConfig{{Runner: "backup"}}},
			{ID: "b", Fallbacks: []config.FallbackConfig{{Runner: "backup"}}},
		},
	}
	got := DeadPathsFor(cfg, nil, map[string]bool{"a": true, "b": true}, nil)
	if len(got.Fallbacks) != 1 {
		t.Errorf("Fallbacks = %v, want one entry for the shared runner", got.Fallbacks)
	}
}

func TestTurnCapsSkipsUncappedAgents(t *testing.T) {
	cfg := &config.Config{Agents: []config.AgentConfig{
		{ID: "capped", MaxTurns: 20},
		{ID: "uncapped", MaxTurns: 0},
	}}
	caps := TurnCaps(cfg)
	if caps["capped"] != 20 {
		t.Errorf("capped = %d, want 20", caps["capped"])
	}
	if _, ok := caps["uncapped"]; ok {
		t.Error("max_turns 0 means unlimited and must not be reported as a cap")
	}
}
