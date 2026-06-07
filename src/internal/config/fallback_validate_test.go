package config_test

import (
	"strings"
	"testing"

	"github.com/orlandoburli/apiary/internal/config"
)

func baseCfgWithFallbacks(fbs []config.FallbackConfig) *config.Config {
	return &config.Config{
		Version: "1",
		Runners: []config.RunnerConfig{{ID: "claude", Type: "claude-cli"}, {ID: "opencode-go", Type: "cli", Provider: "opencode"}},
		Agents: []config.AgentConfig{{
			ID: "engineer", Model: "claude-sonnet-4-6", Runner: "claude", Fallbacks: fbs,
		}},
	}
}

func TestValidate_FallbackRunnerDefined(t *testing.T) {
	cfg := baseCfgWithFallbacks([]config.FallbackConfig{{Runner: "opencode-go", Model: "opencode-go/x"}})
	for _, err := range cfg.Validate() {
		if strings.Contains(err.Error(), "fallback") {
			t.Errorf("valid fallback flagged: %v", err)
		}
	}
}

func TestValidate_FallbackRunnerUndefined(t *testing.T) {
	cfg := baseCfgWithFallbacks([]config.FallbackConfig{{Runner: "ghost"}})
	found := false
	for _, err := range cfg.Validate() {
		if strings.Contains(err.Error(), `fallbacks[0]: runner "ghost" not defined`) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected undefined-fallback-runner error, got %v", cfg.Validate())
	}
}

func TestValidate_FallbackRunnerRequired(t *testing.T) {
	cfg := baseCfgWithFallbacks([]config.FallbackConfig{{Model: "only-model"}})
	found := false
	for _, err := range cfg.Validate() {
		if strings.Contains(err.Error(), "fallbacks[0]: runner is required") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected required-runner error, got %v", cfg.Validate())
	}
}
