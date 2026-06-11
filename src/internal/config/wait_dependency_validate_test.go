package config_test

import (
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
)

// depWaitConfig returns a config with one workflow whose first step is a
// wait_for with the given wait config.
func depWaitConfig(wait *config.WaitForConfig) *config.Config {
	cfg := baseWorkflowConfig()
	cfg.Workflows = []config.WorkflowConfig{
		{ID: "wf", Steps: []config.StepConfig{
			{ID: "await", Type: config.StepTypeWaitFor, WaitFor: wait},
			{ID: "impl", Agent: "architect", DependsOn: []string{"await"}},
		}},
	}
	return cfg
}

func TestWaitFor_DependencyKindValid(t *testing.T) {
	assertNoError(t, depWaitConfig(&config.WaitForConfig{
		Kind:            "dependency",
		SatisfiedWhen:   []string{"merged", "done"},
		BlockerLinkType: "Blocks",
		OnTimeout:       "hold",
	}))
}

func TestWaitFor_UnknownKindRejected(t *testing.T) {
	assertError(t, depWaitConfig(&config.WaitForConfig{Kind: "weather"}),
		`kind "weather" not supported (valid: ci, dependency)`)
}

func TestWaitFor_SatisfiedWhenInvalidValue(t *testing.T) {
	assertError(t, depWaitConfig(&config.WaitForConfig{
		Kind: "dependency", SatisfiedWhen: []string{"closed"},
	}), `satisfied_when value "closed" not supported`)
}

func TestWaitFor_SatisfiedWhenOnlyForDependency(t *testing.T) {
	assertError(t, depWaitConfig(&config.WaitForConfig{
		Kind: "ci", SatisfiedWhen: []string{"merged"},
	}), `satisfied_when is only valid with kind "dependency"`)
}

func TestWaitFor_BlockerLinkTypeOnlyForDependency(t *testing.T) {
	assertError(t, depWaitConfig(&config.WaitForConfig{
		Kind: "ci", BlockerLinkType: "Blocks",
	}), `blocker_link_type is only valid with kind "dependency"`)
}

func TestWaitFor_OnTimeoutInvalidValue(t *testing.T) {
	assertError(t, depWaitConfig(&config.WaitForConfig{
		Kind: "dependency", OnTimeout: "explode",
	}), `invalid wait_for on_timeout "explode"`)
}

func TestWaitFor_DependencyRequiresCapableSource(t *testing.T) {
	// The capability hook is injected by cli in production; simulate a config
	// whose only source's adapter cannot list blockers.
	prev := config.SourceSupportsDependencyWait
	defer func() { config.SourceSupportsDependencyWait = prev }()

	config.SourceSupportsDependencyWait = func(string) bool { return false }
	assertError(t, depWaitConfig(&config.WaitForConfig{Kind: "dependency"}),
		"no configured source supports it")

	config.SourceSupportsDependencyWait = func(string) bool { return true }
	assertNoError(t, depWaitConfig(&config.WaitForConfig{Kind: "dependency"}))
}

func TestWaitFor_DependencyDefaults(t *testing.T) {
	dep := &config.WaitForConfig{Kind: "dependency"}
	if d := dep.ParsedMaxDuration(); d != 0 {
		t.Errorf("dependency default max_duration = %v, want 0 (no deadline)", d)
	}
	if a := dep.TimeoutAction(); a != config.OnTimeoutHold {
		t.Errorf("dependency default on_timeout = %q, want hold", a)
	}
	if got := dep.EffectiveSatisfiedWhen(); len(got) != 2 || got[0] != "merged" || got[1] != "done" {
		t.Errorf("default satisfied_when = %v, want [merged done]", got)
	}

	ci := &config.WaitForConfig{Kind: "ci"}
	if d := ci.ParsedMaxDuration(); d != 2*time.Hour {
		t.Errorf("ci default max_duration = %v, want 2h", d)
	}
	if a := ci.TimeoutAction(); a != config.OnTimeoutFail {
		t.Errorf("ci default on_timeout = %q, want fail", a)
	}

	override := &config.WaitForConfig{Kind: "dependency", MaxDuration: "168h", OnTimeout: "fail"}
	if d := override.ParsedMaxDuration(); d != 168*time.Hour {
		t.Errorf("explicit max_duration = %v, want 168h", d)
	}
	if a := override.TimeoutAction(); a != config.OnTimeoutFail {
		t.Errorf("explicit on_timeout = %q, want fail", a)
	}
}
