package improve

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

func baseCfg() *config.Config {
	return &config.Config{
		DefaultRunner: "claude",
		Runners: []config.RunnerConfig{
			{ID: "claude", Type: "cli", Provider: "claude"},
			{ID: "codex", Type: "cli", Provider: "codex", Models: []string{"gpt-5.5"}},
		},
		Agents: []config.AgentConfig{
			{ID: "engineer", Model: "sonnet"},
			{ID: "improver", Model: "opus", MaxTurns: 40},
		},
	}
}

func TestResolveAdvisorPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*config.Config)
		flags      AdvisorFlags
		wantAgent  string
		wantModel  string
		wantRunner string
	}{
		{
			name:       "--advisor wins over everything",
			mutate:     func(c *config.Config) { c.Settings.Improve.Agent = "improver" },
			flags:      AdvisorFlags{Advisor: "engineer"},
			wantAgent:  "engineer",
			wantModel:  "sonnet",
			wantRunner: "claude",
		},
		{
			name:       "ad-hoc runner/model wins over config",
			mutate:     func(c *config.Config) { c.Settings.Improve.Agent = "improver" },
			flags:      AdvisorFlags{Runner: "codex", Model: "gpt-5.5"},
			wantAgent:  "(ad-hoc)",
			wantModel:  "gpt-5.5",
			wantRunner: "codex",
		},
		{
			name:       "settings.improve.agent wins over the improver convention",
			mutate:     func(c *config.Config) { c.Settings.Improve.Agent = "engineer" },
			flags:      AdvisorFlags{},
			wantAgent:  "engineer",
			wantModel:  "sonnet",
			wantRunner: "claude",
		},
		{
			name:       "falls back to an agent named improver",
			mutate:     func(*config.Config) {},
			flags:      AdvisorFlags{},
			wantAgent:  "improver",
			wantModel:  "opus",
			wantRunner: "claude",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseCfg()
			tc.mutate(cfg)
			adv, err := ResolveAdvisor(cfg, tc.flags)
			if err != nil {
				t.Fatalf("ResolveAdvisor: %v", err)
			}
			if adv.AgentID != tc.wantAgent || adv.Model != tc.wantModel || adv.RunnerID != tc.wantRunner {
				t.Errorf("got %s/%s/%s, want %s/%s/%s",
					adv.AgentID, adv.RunnerID, adv.Model, tc.wantAgent, tc.wantRunner, tc.wantModel)
			}
		})
	}
}

// The command must never invent a model: `model` is required per agent and there
// is no global default, so a guess would bill the user for a model they never
// chose.
func TestResolveAdvisorErrorsWithoutAnAdvisor(t *testing.T) {
	cfg := baseCfg()
	cfg.Agents = []config.AgentConfig{{ID: "engineer", Model: "sonnet"}}

	_, err := ResolveAdvisor(cfg, AdvisorFlags{})
	if err == nil {
		t.Fatal("want an error when no advisor is configured")
	}
	msg := err.Error()
	for _, want := range []string{"--advisor", "--runner", "settings.improve.agent", "improver", "dump-evidence"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q so the user knows the way out; got:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "engineer") {
		t.Error("error should list the agents that do exist")
	}
}

func TestResolveAdvisorRejectsHalfAnAdHocPair(t *testing.T) {
	cfg := baseCfg()

	if _, err := ResolveAdvisor(cfg, AdvisorFlags{Model: "opus"}); err == nil {
		t.Error("--model without --runner must be rejected: the runner picks the adapter")
	}
	if _, err := ResolveAdvisor(cfg, AdvisorFlags{Runner: "claude"}); err == nil {
		t.Error("--runner without --model must be rejected: there is no default model")
	}
}

func TestResolveAdvisorRejectsUnknownNames(t *testing.T) {
	cfg := baseCfg()

	if _, err := ResolveAdvisor(cfg, AdvisorFlags{Advisor: "ghost"}); err == nil {
		t.Error("unknown advisor agent must be rejected")
	}
	if _, err := ResolveAdvisor(cfg, AdvisorFlags{Runner: "ghost", Model: "x"}); err == nil {
		t.Error("unknown runner must be rejected")
	}
}

func TestResolveAdvisorEnforcesRunnerModelList(t *testing.T) {
	cfg := baseCfg()
	// codex declares models: [gpt-5.5], so anything else is a config error.
	if _, err := ResolveAdvisor(cfg, AdvisorFlags{Runner: "codex", Model: "opus"}); err == nil {
		t.Error("a model absent from the runner's models list must be rejected")
	}
	// claude declares none, so it accepts anything.
	if _, err := ResolveAdvisor(cfg, AdvisorFlags{Runner: "claude", Model: "anything"}); err != nil {
		t.Errorf("a runner with no models list accepts any model: %v", err)
	}
}

func TestEffortModelsOverrideAgentModel(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.Improve.EffortModels = map[string]string{"quick": "haiku"}

	quick, err := ResolveAdvisor(cfg, AdvisorFlags{Effort: EffortQuick})
	if err != nil {
		t.Fatalf("ResolveAdvisor: %v", err)
	}
	if quick.Model != "haiku" {
		t.Errorf("quick model = %q, want haiku", quick.Model)
	}

	// An effort with no mapping falls through to the agent's own model.
	deep, err := ResolveAdvisor(cfg, AdvisorFlags{Effort: EffortDeep})
	if err != nil {
		t.Fatalf("ResolveAdvisor: %v", err)
	}
	if deep.Model != "opus" {
		t.Errorf("deep model = %q, want the agent's own opus", deep.Model)
	}
}

func TestAdvisorInheritsFallbacks(t *testing.T) {
	cfg := baseCfg()
	cfg.Settings.DefaultFallbacks = []config.FallbackConfig{{Runner: "codex", Model: "gpt-5.5"}}

	adv, err := ResolveAdvisor(cfg, AdvisorFlags{})
	if err != nil {
		t.Fatalf("ResolveAdvisor: %v", err)
	}
	if len(adv.Fallbacks) != 1 || adv.Fallbacks[0].Runner != "codex" {
		t.Errorf("advisor should inherit settings.default_fallbacks, got %+v", adv.Fallbacks)
	}

	// An agent's own chain takes precedence over the global default.
	cfg.Agents[1].Fallbacks = []config.FallbackConfig{{Runner: "claude"}}
	adv, err = ResolveAdvisor(cfg, AdvisorFlags{})
	if err != nil {
		t.Fatalf("ResolveAdvisor: %v", err)
	}
	if len(adv.Fallbacks) != 1 || adv.Fallbacks[0].Runner != "claude" {
		t.Errorf("agent fallbacks should win over the default, got %+v", adv.Fallbacks)
	}
}

func TestApplyProfileOverlaysAgents(t *testing.T) {
	cfg := baseCfg()
	cfg.Profiles = map[string]map[string]config.ProfileConfig{
		"cheap": {"improver": {Runner: "codex", Model: "gpt-5.5"}},
	}

	applied, found := config.ApplyProfile(cfg, "cheap")
	if !found || applied != 1 {
		t.Fatalf("ApplyProfile = %d,%v; want 1,true", applied, found)
	}
	adv, err := ResolveAdvisor(cfg, AdvisorFlags{})
	if err != nil {
		t.Fatalf("ResolveAdvisor: %v", err)
	}
	if adv.RunnerID != "codex" || adv.Model != "gpt-5.5" {
		t.Errorf("profile overlay not applied: got %s/%s", adv.RunnerID, adv.Model)
	}
}

func TestApplyProfileUnknownNameIsReported(t *testing.T) {
	cfg := baseCfg()
	if _, found := config.ApplyProfile(cfg, "nope"); found {
		t.Error("an unknown profile must report found=false so the caller can warn")
	}
	// An empty name is a no-op, not an error.
	if _, found := config.ApplyProfile(cfg, ""); !found {
		t.Error("an empty profile name is a no-op")
	}
}

func TestParseEffort(t *testing.T) {
	for _, s := range []string{"quick", "standard", "deep"} {
		if _, err := ParseEffort(s); err != nil {
			t.Errorf("ParseEffort(%q): %v", s, err)
		}
	}
	if e, err := ParseEffort(""); err != nil || e != EffortStandard {
		t.Errorf(`ParseEffort("") = %q,%v; want standard,nil`, e, err)
	}
	if _, err := ParseEffort("exhaustive"); err == nil {
		t.Error("an unknown effort must be rejected")
	}
}

func TestEffortKnobsScale(t *testing.T) {
	quick, std, deep := EffortQuick.Expand(), EffortStandard.Expand(), EffortDeep.Expand()

	if quick.TranscriptsPerHotspot != 0 {
		t.Error("quick reads no transcripts — it is aggregates only")
	}
	if !(std.TranscriptsPerHotspot < deep.TranscriptsPerHotspot) {
		t.Error("deep must read more transcripts than standard")
	}
	if !(quick.WorkspaceBreadth < std.WorkspaceBreadth && std.WorkspaceBreadth < deep.WorkspaceBreadth) {
		t.Error("workspace breadth must widen with effort")
	}
	if deep.Critic != true || std.Critic != false {
		t.Error("the critic pass is a deep-effort feature")
	}
}
