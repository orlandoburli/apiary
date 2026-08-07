package config_test

import (
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

// withResolveCaps points config.SourceCapabilities at a registry where only
// "alerts" can report a resolved item.
func withResolveCaps(t *testing.T) {
	t.Helper()
	prev := config.SourceCapabilities
	config.SourceCapabilities = func(sourceType string) config.SourceCaps {
		if sourceType == "alerts" {
			return config.SourceCaps{Resolvable: true}
		}
		return config.SourceCaps{SetState: true, AddLabels: true, Approvals: true}
	}
	t.Cleanup(func() { config.SourceCapabilities = prev })
}

func resolveConfig(sources []config.SourceConfig) *config.Config {
	return &config.Config{
		Version: "1",
		Sources: sources,
		Workflows: []config.WorkflowConfig{{
			ID:    "investigate",
			Steps: []config.StepConfig{{ID: "s1", Agent: "sre"}},
		}},
	}
}

// A monitoring source that can report resolution accepts the flag.
func TestInterruptOnResolve_AcceptedOnCapableSource(t *testing.T) {
	withResolveCaps(t)

	cfg := resolveConfig([]config.SourceConfig{
		{ID: "prod-alerts", Type: "alerts", InterruptOnResolve: true},
	})
	errsNotContain(t, cfg.Validate(), "interrupt_on_resolve")
}

// A ticket source cannot tell a resolved item from a closed one; the flag would
// silently do nothing, so it is rejected rather than ignored.
func TestInterruptOnResolve_RejectedOnIncapableSource(t *testing.T) {
	withResolveCaps(t)

	cfg := resolveConfig([]config.SourceConfig{
		{ID: "tickets", Type: "ticket", InterruptOnResolve: true},
	})
	errsContain(t, cfg.Validate(), "interrupt_on_resolve is not supported by source type \"ticket\"")
}

// Leaving the flag off is always valid, whatever the source type.
func TestInterruptOnResolve_OffIsAlwaysValid(t *testing.T) {
	withResolveCaps(t)

	cfg := resolveConfig([]config.SourceConfig{
		{ID: "tickets", Type: "ticket"},
		{ID: "prod-alerts", Type: "alerts"},
	})
	errsNotContain(t, cfg.Validate(), "interrupt_on_resolve")
}
