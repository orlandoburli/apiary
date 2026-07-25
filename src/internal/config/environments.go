package config

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvironmentConfig defines a named deployment environment. It carries overlays
// that are merged on top of the base configuration when the environment is
// resolved, so development, staging, and production can share a single
// apiary.yaml while differing in source endpoints, concurrency limits, and
// credentials references.
type EnvironmentConfig struct {
	Name        string              `yaml:"name"`
	Description string              `yaml:"description,omitempty"`
	// Sources carries per-source overlays: enable/disable a source or override
	// selected config keys. Secret values must not be inlined — use ${VAR}
	// references resolved at runtime from the environment's .env file.
	Sources  []EnvSourceOverlay  `yaml:"sources,omitempty"`
	Settings *EnvSettingsOverlay `yaml:"settings,omitempty"`
	// Env is a map of additional environment variable name → reference (e.g.
	// "${STAGING_API_KEY}") injected when this environment is active. Document
	// which variables each environment expects; never inline secret values here.
	Env     map[string]string `yaml:"env,omitempty"`
	Rollout *RolloutConfig    `yaml:"rollout,omitempty"`
}

// EnvSourceOverlay overrides one source's config for a given environment.
// Enabled can disable a source entirely; Config merges keys on top of the
// base source config (deep merge is not performed — top-level keys win).
type EnvSourceOverlay struct {
	ID      string         `yaml:"id"`
	Enabled *bool          `yaml:"enabled,omitempty"`
	Config  map[string]any `yaml:"config,omitempty"`
}

// EnvSettingsOverlay overrides selected global settings for an environment.
// Zero values are treated as "not set" and leave the base value unchanged.
type EnvSettingsOverlay struct {
	Concurrency int `yaml:"concurrency,omitempty"`
}

// RolloutConfig gates which source cells an environment processes. All
// criteria are AND-ed: a cell must match every non-empty criterion.
// An empty RolloutConfig means "process all cells".
type RolloutConfig struct {
	// Sources limits processing to these source IDs (empty = all sources).
	Sources []string `yaml:"sources,omitempty"`
	// Labels includes only cells carrying at least one of these labels
	// (case-insensitive OR match within the list).
	Labels []string `yaml:"labels,omitempty"`
	// Percentage is a hash-based fractional rollout (1–100). 0 means 100%.
	// The cell ID is hashed so the same cell always falls in the same bucket.
	Percentage int `yaml:"percentage,omitempty"`
}

// ResolveEnvironment returns a copy of c with the named environment's overlays
// applied. The base Config is never mutated. Returns an error if the
// environment is not defined.
func (c *Config) ResolveEnvironment(name string) (*Config, error) {
	var env *EnvironmentConfig
	for i := range c.Environments {
		if c.Environments[i].Name == name {
			env = &c.Environments[i]
			break
		}
	}
	if env == nil {
		return nil, fmt.Errorf("environment %q not defined", name)
	}

	// Deep copy via YAML round-trip so overlays never mutate the base config.
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("serialising config for environment resolution: %w", err)
	}
	var resolved Config
	if err := yaml.Unmarshal(data, &resolved); err != nil {
		return nil, fmt.Errorf("deserialising config for environment resolution: %w", err)
	}
	resolved.rawContent = c.rawContent

	// Apply settings overlay.
	if ov := env.Settings; ov != nil {
		if ov.Concurrency > 0 {
			resolved.Settings.Concurrency = ov.Concurrency
		}
	}

	// Track which sources are explicitly disabled.
	disabled := map[string]bool{}
	for _, sov := range env.Sources {
		if sov.Enabled != nil && !*sov.Enabled {
			disabled[sov.ID] = true
		}
	}

	// Apply source config overlays.
	for _, sov := range env.Sources {
		for i := range resolved.Sources {
			if resolved.Sources[i].ID != sov.ID {
				continue
			}
			if resolved.Sources[i].Config == nil {
				resolved.Sources[i].Config = make(map[string]any)
			}
			for k, v := range sov.Config {
				resolved.Sources[i].Config[k] = v
			}
		}
	}

	// Remove disabled sources.
	if len(disabled) > 0 {
		kept := resolved.Sources[:0]
		for _, s := range resolved.Sources {
			if !disabled[s.ID] {
				kept = append(kept, s)
			}
		}
		resolved.Sources = kept
	}

	return &resolved, nil
}

// Digest returns a 16-character hex fingerprint of the resolved configuration.
// It marshals the config to canonical YAML and takes the first 8 bytes of
// SHA-256. The digest changes whenever any field visible to the YAML encoder
// changes, making it suitable for audit records and change detection.
func (c *Config) Digest() string {
	data, err := yaml.Marshal(c)
	if err != nil {
		return "unknown"
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:8])
}

// GitRevision returns the short HEAD commit hash of the git repository
// containing configPath. Returns an empty string when unavailable (not a git
// repo, git binary not found, etc.) — callers must tolerate an empty value.
func GitRevision(configPath string) string {
	dir := "."
	if configPath != "" {
		dir = filepath.Dir(configPath)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
