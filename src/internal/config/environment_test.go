package config

import (
	"strings"
	"testing"
)

func TestForEnvironment_NotFound(t *testing.T) {
	cfg := &Config{Version: "1.0"}
	_, err := cfg.ForEnvironment("staging")
	if err == nil {
		t.Fatal("expected error for missing environment")
	}
	if !strings.Contains(err.Error(), "staging") {
		t.Errorf("error should mention the environment name: %v", err)
	}
}

func TestForEnvironment_AppliesOverlay(t *testing.T) {
	base := &Config{
		Version: "1.0",
		Sources: []SourceConfig{{ID: "gh", Type: "github", Config: map[string]any{"endpoint": "https://github.com"}}},
		Agents:  []AgentConfig{{ID: "eng", Model: "claude-3", Runner: "cli"}},
		Settings: Settings{
			Concurrency: 4,
			LogLevel:    "info",
		},
		Environments: map[string]EnvironmentOverlay{
			"staging": {
				Sources: []SourceOverlay{{
					ID:     "gh",
					Config: map[string]any{"endpoint": "https://staging.github.com"},
				}},
				Agents: []AgentOverlay{{
					ID:    "eng",
					Model: "claude-3-haiku",
					Env:   map[string]string{"GH_TOKEN": "${STAGING_GH_TOKEN}"},
				}},
				Settings: &EnvironmentSettingsOverlay{Concurrency: 2},
			},
		},
	}

	resolved, err := base.ForEnvironment("staging")
	if err != nil {
		t.Fatalf("ForEnvironment: %v", err)
	}

	// Source config should be merged.
	if len(resolved.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(resolved.Sources))
	}
	if got := resolved.Sources[0].Config["endpoint"]; got != "https://staging.github.com" {
		t.Errorf("source endpoint: got %v, want https://staging.github.com", got)
	}

	// Agent model should be overridden.
	if len(resolved.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(resolved.Agents))
	}
	if resolved.Agents[0].Model != "claude-3-haiku" {
		t.Errorf("agent model: got %q, want claude-3-haiku", resolved.Agents[0].Model)
	}
	if resolved.Agents[0].Env["GH_TOKEN"] != "${STAGING_GH_TOKEN}" {
		t.Errorf("agent env: got %q, want ${STAGING_GH_TOKEN}", resolved.Agents[0].Env["GH_TOKEN"])
	}
	// Runner should be inherited.
	if resolved.Agents[0].Runner != "cli" {
		t.Errorf("agent runner: got %q, want cli", resolved.Agents[0].Runner)
	}

	// Settings concurrency should be overridden.
	if resolved.Settings.Concurrency != 2 {
		t.Errorf("concurrency: got %d, want 2", resolved.Settings.Concurrency)
	}
	// Log level should be inherited.
	if resolved.Settings.LogLevel != "info" {
		t.Errorf("log_level: got %q, want info", resolved.Settings.LogLevel)
	}
}

func TestForEnvironment_EnabledSources(t *testing.T) {
	base := &Config{
		Version: "1.0",
		Sources: []SourceConfig{
			{ID: "github", Type: "github"},
			{ID: "jira", Type: "jira"},
		},
		Environments: map[string]EnvironmentOverlay{
			"dev": {EnabledSources: []string{"github"}},
		},
	}

	resolved, err := base.ForEnvironment("dev")
	if err != nil {
		t.Fatalf("ForEnvironment: %v", err)
	}
	if len(resolved.Sources) != 1 || resolved.Sources[0].ID != "github" {
		t.Errorf("expected only github source, got %v", resolved.Sources)
	}
	// Base should be unchanged.
	if len(base.Sources) != 2 {
		t.Error("base config was mutated by ForEnvironment")
	}
}

func TestForEnvironment_DoesNotMutateBase(t *testing.T) {
	base := &Config{
		Version: "1.0",
		Agents:  []AgentConfig{{ID: "eng", Model: "claude-3"}},
		Environments: map[string]EnvironmentOverlay{
			"prod": {Agents: []AgentOverlay{{ID: "eng", Model: "claude-opus"}}},
		},
	}

	_, err := base.ForEnvironment("prod")
	if err != nil {
		t.Fatalf("ForEnvironment: %v", err)
	}
	// Verify base was not mutated.
	if base.Agents[0].Model != "claude-3" {
		t.Errorf("base config mutated: agent model is now %q", base.Agents[0].Model)
	}
}

func TestValidateEnvironments_UnknownSource(t *testing.T) {
	cfg := &Config{
		Version: "1.0",
		Sources: []SourceConfig{{ID: "github", Type: "github"}},
		Environments: map[string]EnvironmentOverlay{
			"bad": {Sources: []SourceOverlay{{ID: "nonexistent"}}},
		},
	}
	errs := cfg.validateEnvironments()
	if len(errs) == 0 {
		t.Fatal("expected error for unknown source reference")
	}
}

func TestValidateEnvironments_UnknownAgent(t *testing.T) {
	cfg := &Config{
		Version: "1.0",
		Agents:  []AgentConfig{{ID: "eng", Model: "claude-3"}},
		Environments: map[string]EnvironmentOverlay{
			"bad": {Agents: []AgentOverlay{{ID: "no-such-agent"}}},
		},
	}
	errs := cfg.validateEnvironments()
	if len(errs) == 0 {
		t.Fatal("expected error for unknown agent reference")
	}
}

func TestValidateEnvironments_RolloutPercentage(t *testing.T) {
	cfg := &Config{
		Version: "1.0",
		Environments: map[string]EnvironmentOverlay{
			"bad": {Rollout: &RolloutPolicy{Percentage: 150}},
		},
	}
	errs := cfg.validateEnvironments()
	if len(errs) == 0 {
		t.Fatal("expected error for rollout percentage > 100")
	}
}

func TestDigest_StableForIdenticalConfigs(t *testing.T) {
	cfg1 := &Config{Version: "1.0", Settings: Settings{Concurrency: 4}}
	cfg2 := &Config{Version: "1.0", Settings: Settings{Concurrency: 4}}
	if Digest(cfg1) != Digest(cfg2) {
		t.Error("identical configs should produce the same digest")
	}
}

func TestDigest_DiffersForDifferentConfigs(t *testing.T) {
	cfg1 := &Config{Version: "1.0", Settings: Settings{Concurrency: 4}}
	cfg2 := &Config{Version: "1.0", Settings: Settings{Concurrency: 8}}
	if Digest(cfg1) == Digest(cfg2) {
		t.Error("different configs should produce different digests")
	}
}
