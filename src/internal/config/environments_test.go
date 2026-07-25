package config

import (
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestResolveEnvironment_NotFound(t *testing.T) {
	cfg := &Config{Sources: []SourceConfig{{ID: "gh"}}}
	_, err := cfg.ResolveEnvironment("missing")
	if err == nil {
		t.Fatal("expected error for missing environment")
	}
}

func TestResolveEnvironment_DisablesSource(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{
			{ID: "gh", Type: "github"},
			{ID: "jira", Type: "jira"},
		},
		Environments: []EnvironmentConfig{
			{
				Name: "staging",
				Sources: []EnvSourceOverlay{
					{ID: "jira", Enabled: boolPtr(false)},
				},
			},
		},
	}
	resolved, err := cfg.ResolveEnvironment("staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(resolved.Sources))
	}
	if resolved.Sources[0].ID != "gh" {
		t.Fatalf("expected source gh, got %s", resolved.Sources[0].ID)
	}
	// Base config must not be mutated.
	if len(cfg.Sources) != 2 {
		t.Fatal("base config sources were mutated")
	}
}

func TestResolveEnvironment_OverridesSourceConfig(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{
			{ID: "gh", Type: "github", Config: map[string]any{"repo": "owner/base"}},
		},
		Environments: []EnvironmentConfig{
			{
				Name: "staging",
				Sources: []EnvSourceOverlay{
					{ID: "gh", Config: map[string]any{"repo": "owner/staging"}},
				},
			},
		},
	}
	resolved, err := cfg.ResolveEnvironment("staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Sources[0].Config["repo"] != "owner/staging" {
		t.Fatalf("expected owner/staging, got %v", resolved.Sources[0].Config["repo"])
	}
	if cfg.Sources[0].Config["repo"] != "owner/base" {
		t.Fatal("base config source config was mutated")
	}
}

func TestResolveEnvironment_OverridesConcurrency(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{{ID: "gh", Type: "github"}},
		Settings: Settings{Concurrency: 4},
		Environments: []EnvironmentConfig{
			{
				Name:     "production",
				Settings: &EnvSettingsOverlay{Concurrency: 8},
			},
		},
	}
	resolved, err := cfg.ResolveEnvironment("production")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Settings.Concurrency != 8 {
		t.Fatalf("expected concurrency 8, got %d", resolved.Settings.Concurrency)
	}
	if cfg.Settings.Concurrency != 4 {
		t.Fatal("base config settings were mutated")
	}
}

func TestDigest_Stable(t *testing.T) {
	cfg := &Config{
		Version: "1.0",
		Sources: []SourceConfig{{ID: "gh", Type: "github"}},
	}
	d1 := cfg.Digest()
	d2 := cfg.Digest()
	if d1 != d2 {
		t.Fatalf("digest is not stable: %s != %s", d1, d2)
	}
	if len(d1) != 16 {
		t.Fatalf("expected 16-char digest, got %d: %s", len(d1), d1)
	}
}

func TestDigest_ChangesOnMutation(t *testing.T) {
	cfg := &Config{Version: "1.0", Sources: []SourceConfig{{ID: "gh"}}}
	d1 := cfg.Digest()
	cfg.Sources[0].ID = "github"
	d2 := cfg.Digest()
	if d1 == d2 {
		t.Fatal("digest did not change after mutation")
	}
}

func TestValidateEnvironments_DuplicateName(t *testing.T) {
	cfg := &Config{
		Version: "1.0",
		Sources: []SourceConfig{{ID: "gh", Type: "github"}},
		Runners: []RunnerConfig{{ID: "r1", Type: "cli", Provider: "claude"}},
		Agents: []AgentConfig{{ID: "a1", Model: "claude-opus-4-5", Runner: "r1"}},
		Environments: []EnvironmentConfig{
			{Name: "staging"},
			{Name: "staging"},
		},
	}
	errs := cfg.validateEnvironments()
	if len(errs) == 0 {
		t.Fatal("expected duplicate name error")
	}
}

func TestValidateEnvironments_UnknownSource(t *testing.T) {
	cfg := &Config{
		Version: "1.0",
		Sources: []SourceConfig{{ID: "gh", Type: "github"}},
		Environments: []EnvironmentConfig{
			{
				Name: "staging",
				Sources: []EnvSourceOverlay{{ID: "unknown"}},
			},
		},
	}
	errs := cfg.validateEnvironments()
	if len(errs) == 0 {
		t.Fatal("expected unknown source error")
	}
}

func TestValidateEnvironments_InvalidPercentage(t *testing.T) {
	cfg := &Config{
		Version: "1.0",
		Sources: []SourceConfig{{ID: "gh", Type: "github"}},
		Environments: []EnvironmentConfig{
			{
				Name:    "staging",
				Rollout: &RolloutConfig{Percentage: 150},
			},
		},
	}
	errs := cfg.validateEnvironments()
	if len(errs) == 0 {
		t.Fatal("expected percentage out of range error")
	}
}
